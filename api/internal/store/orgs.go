package store

import (
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// OrgSlugRe is the org slug grammar (api-v1 v1.24 item 1).
var OrgSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// MaxOrgSlug is the org slug length cap for Unique.
const MaxOrgSlug = 64

// OrgSlug derives an org slug from a name, capped at MaxOrgSlug. An empty
// result becomes "org".
func OrgSlug(name string) string {
	s := Slug(name)
	if !strings.ContainsFunc(strings.ToLower(name), func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
	}) {
		s = "org"
	}
	if len(s) > MaxOrgSlug {
		s = strings.Trim(s[:MaxOrgSlug], "-")
	}
	if s == "" {
		s = "org"
	}
	return s
}

// PersonalOrgSlug is the default-rule slug for a user's personal org
// (migration 0015 backfill and first sign-in, D-8): "u-" and the first 12
// hex digits of the user id.
func PersonalOrgSlug(userID uuid.UUID) string {
	return "u-" + strings.ReplaceAll(userID.String(), "-", "")[:12]
}

// PersonalOrgName is "<name>'s org", or "<email local part>'s org" when the
// user has no name.
func PersonalOrgName(name, email string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		n = email
		if i := strings.IndexByte(n, '@'); i > 0 {
			n = n[:i]
		}
	}
	if n == "" {
		n = "My"
	}
	const suffix = "'s org"
	if r := []rune(n); len(r) > 80-len(suffix) {
		n = string(r[:80-len(suffix)])
	}
	return n + suffix
}
