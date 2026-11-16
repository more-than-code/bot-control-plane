package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Value requests (api-v1 v1.28): an agent asks for an env value, and the
// user who asked fills it in the console. A request never holds a value.
// Lifetime, limit, states and retention are the shared ones (requests.go);
// the done state is RequestFilled.

// ErrTooManyRequests is a create when the user already has MaxOpenRequests
// open value requests.
var ErrTooManyRequests = errors.New("too many open value requests; fill or wait for the ones you have")

// ValueRequest is one stored value request: the shared part and the set and key.
type ValueRequest struct {
	RequestMeta
	Set string
	Key string
}

// ValueRequestStore is the persistence of value requests. Every method
// treats a request of a deleted project as missing.
type ValueRequestStore interface {
	// CreateValueRequest stores a pending request in one transaction. The
	// set must exist (ErrNotFound). The key must be absent from the set or a
	// needs_value placeholder (ErrKeyExists). A pending, unexpired request of
	// in.UserID for the same project, set and key expires at in.CreatedAt
	// (one open request per user and key). in.UserID must have fewer than
	// MaxOpenRequests other pending requests unexpired at in.CreatedAt
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
