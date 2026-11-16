package store

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Requests: an agent asks for something (an env value, a managed file),
// and the user who asked answers on a console page. Every request type
// shares the lifetime, the per-user open limit (counted per type), the
// states and the 7-day retention below; each type embeds RequestMeta and
// names its own done state.
const (
	// RequestTTL is how long a request can be answered.
	RequestTTL = 15 * time.Minute
	// MaxOpenRequests is the most pending, unexpired requests of one type one user may have.
	MaxOpenRequests = 20
	// RequestKeep is how long a row stays after it expires; then it is removed.
	RequestKeep = 7 * 24 * time.Hour
)

// Request states. RequestExpired is computed for a pending row whose expiry
// has passed and is never stored. RequestFilled is the done state of value
// and file requests; another type names its own.
const (
	RequestPending  = "pending"
	RequestDeclined = "declined"
	RequestExpired  = "expired"
	RequestFilled   = "filled"
)

var (
	// ErrRequestUsed is an answer to a request that is done or declined.
	ErrRequestUsed = errors.New("this request was already used")
	// ErrRequestExpired is an answer to a pending request past its expiry.
	ErrRequestExpired = errors.New("this request has expired; ask the agent for a new one")
)

// RequestMeta is what every request type stores besides its subject. The
// id is stored only as its SHA-256.
type RequestMeta struct {
	UserID    uuid.UUID
	ProjectID uuid.UUID
	Reason    string
	State     string
	CreatedAt time.Time
	ExpiresAt time.Time
	DoneAt    *time.Time
}

// StateAt is the request state at a time: a pending request whose expiry
// has passed is expired.
func (r RequestMeta) StateAt(at time.Time) string {
	if r.State == RequestPending && !at.Before(r.ExpiresAt) {
		return RequestExpired
	}
	return r.State
}

// Meta returns the shared part of a request; it is promoted to every type
// that embeds RequestMeta, so code can handle any request type.
func (r RequestMeta) Meta() RequestMeta { return r }

// OpenErr is nil for a request that can still be answered at at, and
// ErrRequestExpired or ErrRequestUsed otherwise.
func (r RequestMeta) OpenErr(at time.Time) error {
	switch r.StateAt(at) {
	case RequestPending:
		return nil
	case RequestExpired:
		return ErrRequestExpired
	default:
		return ErrRequestUsed
	}
}
