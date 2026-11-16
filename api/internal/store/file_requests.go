package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// File requests (api-v1 v1.30): an agent asks for a managed file, and the
// user who asked adds it in the console. A request never holds content.
// Lifetime, limit (counted apart from value requests), states and
// retention are the shared ones (requests.go); the done state is
// RequestFilled.

var (
	// ErrFileExists is a file request create or fill for a file name the
	// project already has. Nothing is written and a request stays pending.
	ErrFileExists = errors.New("file already exists")
	// ErrTooManyFileRequests is a create when the user already has
	// MaxOpenRequests open file requests.
	ErrTooManyFileRequests = errors.New("too many open file requests; fill or wait for the ones you have")
)

// FileRequest is one stored file request: the shared part, the file name
// and the secret flag the agent asked for.
type FileRequest struct {
	RequestMeta
	Name   string
	Secret bool
}

// FileRequestStore is the persistence of file requests. Every method
// treats a request of a deleted project as missing.
type FileRequestStore interface {
	// CreateFileRequest stores a pending request in one transaction. The
	// project must be live (ErrNotFound). The file must not exist
	// (ErrFileExists), and the project must have fewer than MaxProjectFiles
	// files (ErrTooManyFiles). A pending, unexpired request of in.UserID for
	// the same project and name expires at in.CreatedAt (one open request
	// per user and name). in.UserID must have fewer than MaxOpenRequests
	// other pending file requests unexpired at in.CreatedAt
	// (ErrTooManyFileRequests). Rows 7 days past expiry are removed. It
	// writes no file.
	CreateFileRequest(ctx context.Context, hash []byte, in FileRequest) error
	// GetFileRequest returns one request. An unknown hash is ErrNotFound.
	GetFileRequest(ctx context.Context, hash []byte) (FileRequest, error)
	// FillFileRequest locks the project and the request and, in one
	// transaction, creates the file from in (sealed by the caller when
	// in.IsSecret) and marks the request filled at at. It returns only an
	// error, so a nil answer always means committed. A request of another
	// user is ErrNotFound; filled or declined is ErrRequestUsed; pending past
	// its expiry is ErrRequestExpired. A file that exists by now is
	// ErrFileExists and a project at MaxProjectFiles is ErrTooManyFiles; both
	// leave the request pending.
	FillFileRequest(ctx context.Context, hash []byte, userID uuid.UUID, at time.Time, in FileWrite) error
	// DeclineFileRequest marks a pending, unexpired request of userID
	// declined. Errors as for FillFileRequest, without the file checks.
	DeclineFileRequest(ctx context.Context, hash []byte, userID uuid.UUID, at time.Time) error
}
