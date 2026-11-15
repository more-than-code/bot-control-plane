package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Value requests (api-v1 v1.28): an agent asks for an env value, and the
// user who asked fills it in the console. A request never holds a value.
const (
	// ValueRequestTTL is how long a value request can be filled.
	ValueRequestTTL = 15 * time.Minute
	// MaxOpenValueRequests is the most pending, unexpired requests one user may have.
	MaxOpenValueRequests = 20
)

// Stored value request states. "expired" is ValueRequestExpired, computed
// for a pending row whose expiry has passed; it is never stored.
const (
	ValueRequestPending  = "pending"
	ValueRequestFilled   = "filled"
	ValueRequestDeclined = "declined"
	ValueRequestExpired  = "expired"
)

var (
	// ErrRequestUsed is a fill or decline of a request that is filled or declined.
	ErrRequestUsed = errors.New("this request was already used")
	// ErrRequestExpired is a fill or decline of a pending request past its expiry.
	ErrRequestExpired = errors.New("this request has expired; ask the agent for a new one")
	// ErrTooManyRequests is a create when the user already has MaxOpenValueRequests open.
	ErrTooManyRequests = errors.New("too many open value requests; fill or wait for the ones you have")
)

// ValueRequest is one stored request. The id is stored only as its SHA-256.
type ValueRequest struct {
	UserID    uuid.UUID
	ProjectID uuid.UUID
	Set       string
	Key       string
	Reason    string
	State     string
	CreatedAt time.Time
	ExpiresAt time.Time
	DoneAt    *time.Time
}

// StateAt is the request state at a time: a pending request whose expiry
// has passed is expired.
func (v ValueRequest) StateAt(at time.Time) string {
	if v.State == ValueRequestPending && !at.Before(v.ExpiresAt) {
		return ValueRequestExpired
	}
	return v.State
}

// ValueRequestStore is the persistence of value requests. Every method
// treats a request of a deleted project as missing.
type ValueRequestStore interface {
	// CreateValueRequest stores a pending request in one transaction. The
	// set must exist (ErrNotFound). The key must be absent from the set or a
	// needs_value placeholder (ErrKeyExists). A pending, unexpired request of
	// in.UserID for the same project, set and key expires at in.CreatedAt
	// (one open request per user and key). in.UserID must have fewer than
	// MaxOpenValueRequests other pending requests unexpired at in.CreatedAt
	// (ErrTooManyRequests). It writes no env value.
	CreateValueRequest(ctx context.Context, hash []byte, in ValueRequest) error
	// GetValueRequest returns one request. An unknown hash is ErrNotFound.
	GetValueRequest(ctx context.Context, hash []byte) (ValueRequest, error)
	// FillValueRequest locks the request and, in one transaction, writes
	// in as a secret key (sealed; in.Plain is not stored), clears
	// needs_value, sets reveal_pending and generated from in.Generated,
	// re-hashes referencing entries, and marks the request filled at at.
	// in.Key must be the request's key. A request of another user is
	// ErrNotFound; filled or declined is ErrRequestUsed; pending past its
	// expiry is ErrRequestExpired; a key that now has a value is
	// ErrKeyExists and leaves the request pending.
	FillValueRequest(ctx context.Context, hash []byte, userID uuid.UUID, at time.Time, in EnvKeyPut) (EnvKey, error)
	// DeclineValueRequest marks a pending, unexpired request of userID
	// declined. Errors as for FillValueRequest, without ErrKeyExists.
	DeclineValueRequest(ctx context.Context, hash []byte, userID uuid.UUID, at time.Time) error
}
