package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Connect requests (api-v1 v1.31): an agent asks the person to Connect
// GitHub for a project, and the person who asked approves it in the
// console. A request never holds a token. Lifetime, limit (counted apart
// from the other types), states and retention are the shared ones
// (requests.go); the done state is RequestConnected.

// RequestConnected is the done state of a connect request.
const RequestConnected = "connected"

// ErrTooManyConnectRequests is a create when the user already has
// MaxOpenRequests open connect requests.
var ErrTooManyConnectRequests = errors.New("too many open connect requests; finish or wait for the ones you have")

// ConnectRequest is one stored connect request. The registry is always
// ghcr.io, so only the shared part is stored.
type ConnectRequest struct {
	RequestMeta
}

// ConnectRequestStore is the persistence of connect requests. Every method
// treats a request of a deleted project as missing.
type ConnectRequestStore interface {
	// CreateConnectRequest stores a pending request in one transaction. A
	// pending, unexpired request of in.UserID for the same project expires
	// at in.CreatedAt (one open request per user and project). in.UserID
	// must have fewer than MaxOpenRequests other pending connect requests
	// unexpired at in.CreatedAt (ErrTooManyConnectRequests). Rows 7 days
	// past their expiry are removed.
	CreateConnectRequest(ctx context.Context, hash []byte, in ConnectRequest) error
	// GetConnectRequest returns one request. An unknown hash is ErrNotFound.
	GetConnectRequest(ctx context.Context, hash []byte) (ConnectRequest, error)
	// DeclineConnectRequest marks a pending, unexpired request of userID
	// declined. A request of another user is ErrNotFound; connected or
	// declined is ErrRequestUsed; pending past its expiry is ErrRequestExpired.
	DeclineConnectRequest(ctx context.Context, hash []byte, userID uuid.UUID, at time.Time) error
	// ConnectRegistry is PutRegistry for a callback whose state names a
	// connect request (api-v1 v1.31 item 6). In one transaction it writes
	// the registry login and, when the request with requestHash is of
	// actor and projectID, pending and unexpired at at, marks it connected
	// with done_at. A request that is no longer pending (or is missing) is
	// left as it is, and the login is still written.
	ConnectRegistry(ctx context.Context, projectID uuid.UUID, host string, in RegistryWrite, actor uuid.UUID, at time.Time, requestHash []byte) (Registry, error)
}
