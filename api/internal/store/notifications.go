package store

import (
	"context"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Notification read marks (api-v1 v1.37 item 3, migration 0024).

// NotificationReadRetention is how long a read mark is kept: a write prunes
// the user's marks older than this.
const NotificationReadRetention = 90 * 24 * time.Hour

// MaxNotificationKey is the longest key in characters.
const MaxNotificationKey = 200

// MaxNotificationMarks is how many read marks a user keeps: a write deletes
// the oldest beyond this (by read_at, then key), never a key in keep.
const MaxNotificationMarks = 500

// releaseVersionRE is the 0038 check: X.Y.Z, each part 1 to 4 digits.
var releaseVersionRE = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)

// ValidReleaseVersion is a version RecordReleaseServed accepts.
func ValidReleaseVersion(v string) bool { return releaseVersionRE.MatchString(v) }

// ValidNotificationKey is 1 to 200 characters of valid UTF-8 with no
// control character (U+0000–U+001F, U+007F).
func ValidNotificationKey(key string) bool {
	if !utf8.ValidString(key) {
		return false
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	n := utf8.RuneCountInString(key)
	return n >= 1 && n <= MaxNotificationKey
}

// NotificationStore keeps which notification keys a user marked read.
type NotificationStore interface {
	// MarkNotificationsRead deletes the user's marks older than
	// NotificationReadRetention before at whose key is not in keep (the
	// user's current items, so a long-standing item never turns unread
	// again), then upserts a mark at at for each key (duplicates are one
	// mark), then deletes the user's oldest marks (by read_at, then key)
	// beyond MaxNotificationMarks whose key is not in keep, all in one
	// transaction. A key that fails ValidNotificationKey is ErrInvalid and
	// nothing is written; an unknown user is ErrNotFound.
	MarkNotificationsRead(ctx context.Context, userID uuid.UUID, keys, keep []string, at time.Time) error
	// NotificationReads returns the subset of keys the user marked read.
	NotificationReads(ctx context.Context, userID uuid.UUID, keys []string) (map[string]bool, error)
	// NoticeHostUpdates returns, for each host, when the API first saw it
	// behind version (migration 0035): a host with no row for version gets
	// one at at, and its rows for other versions are deleted; an existing
	// row keeps its time. Unknown hosts are skipped.
	NoticeHostUpdates(ctx context.Context, hostIDs []uuid.UUID, version string, at time.Time) (map[uuid.UUID]time.Time, error)
	// RecordReleaseServed records that this server served version at at,
	// unless the version already has a record (migration 0038, api-v1
	// v1.57): the first time is kept. A version that fails
	// ValidReleaseVersion is ErrInvalid and nothing is written.
	RecordReleaseServed(ctx context.Context, version string, at time.Time) error
	// ReleasesServed returns the first-served time of each listed version
	// that has one.
	ReleasesServed(ctx context.Context, versions []string) (map[string]time.Time, error)
}
