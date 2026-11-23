// Package agentversion compares bot-agent versions written as X.Y.Z
// (digits only) for the update hint of api-v1 v1.34.
package agentversion

import (
	"regexp"
	"strconv"
	"strings"
)

// releaseRe is a bot-agent release version (agent-protocol v1.20 item 5):
// each part 1 to 4 digits, the form the host's helper accepts.
var releaseRe = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)

// ValidRelease reports whether s is a release version the upgrade path
// accepts (api-v1 v1.48). It is stricter than Valid.
func ValidRelease(s string) bool { return releaseRe.MatchString(s) }

// Parse reads "X.Y.Z" with each part one or more ASCII digits that fit an
// int. Anything else, including a leading "v" or a suffix, is not ok.
func Parse(s string) (v [3]int, ok bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		if p == "" || len(p) > 9 {
			return v, false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return v, false
			}
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// Valid reports whether s is X.Y.Z.
func Valid(s string) bool {
	_, ok := Parse(s)
	return ok
}

// Older reports whether host is a lower X.Y.Z than latest by major, minor,
// then patch. It is false when either does not parse.
func Older(host, latest string) bool {
	h, ok := Parse(host)
	if !ok {
		return false
	}
	l, ok := Parse(latest)
	if !ok {
		return false
	}
	for i := 0; i < 3; i++ {
		if h[i] != l[i] {
			return h[i] < l[i]
		}
	}
	return false
}
