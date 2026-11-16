package memstore

import (
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Helpers shared by the request types (value_requests.go, file_requests.go).
// Each type keeps its rows in its own map, keyed by hashKey(id hash), so
// the open limit is counted per type.

type request interface{ Meta() store.RequestMeta }

// pruneRequestsLocked removes rows store.RequestKeep past their expiry, as
// the Postgres store does on every create.
func pruneRequestsLocked[T request](rows map[string]T, at time.Time) {
	for k, r := range rows {
		if r.Meta().ExpiresAt.Before(at.Add(-store.RequestKeep)) {
			delete(rows, k)
		}
	}
}

// openRequestsLocked counts userID's pending requests at at, except the one
// sameSubject matches (at most one open request per user and subject):
// that one is returned as replaced, for the caller to expire if its create
// succeeds.
func openRequestsLocked[T request](rows map[string]T, userID uuid.UUID, at time.Time, sameSubject func(T) bool) (open int, replaced string) {
	for k, r := range rows {
		meta := r.Meta()
		if meta.UserID != userID || meta.StateAt(at) != store.RequestPending {
			continue
		}
		if sameSubject(r) {
			replaced = k
			continue
		}
		open++
	}
	return open, replaced
}

// answerableLocked is nil for a stored request of userID, of a live
// project, that is pending and unexpired at at. Anything else is
// ErrNotFound, ErrRequestExpired or ErrRequestUsed.
func (m *Mem) answerableLocked(meta store.RequestMeta, found bool, userID uuid.UUID, at time.Time) error {
	if !found || !m.projectAliveLocked(meta.ProjectID) || meta.UserID != userID {
		return store.ErrNotFound
	}
	return meta.OpenErr(at)
}
