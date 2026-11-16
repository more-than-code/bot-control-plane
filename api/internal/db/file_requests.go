package db

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// File requests (api-v1 v1.30). The table has no content column; a fill
// writes the file into app.project_files in the same transaction.
//
// Lock order: the project row, then request rows. File put and delete take
// the project row first too, so a fill racing a file create or delete waits
// instead of deadlocking, and its existence and count checks hold.

func (d *DB) CreateFileRequest(ctx context.Context, hash []byte, in store.FileRequest) error {
	if !catalog.ValidFileName(in.Name) {
		return store.ErrInvalid
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback(ctx)
	if err := fileRequestTable.lockCreates(ctx, tx, in.UserID); err != nil {
		return err
	}
	if err := lockLiveProject(ctx, tx, in.ProjectID); err != nil {
		return err
	}
	var n int
	var exists bool
	if err := tx.QueryRow(ctx, `
SELECT count(*), COALESCE(bool_or(name = $2), false)
FROM app.project_files WHERE project_id = $1::uuid
`, in.ProjectID.String(), in.Name).Scan(&n, &exists); err != nil {
		return mapErr(err)
	}
	if exists {
		return store.ErrFileExists
	}
	if n >= store.MaxProjectFiles {
		return store.ErrTooManyFiles
	}
	// After the project lock (the order fill takes): the 7-day cleanup
	// (api-v1 v1.30 item 7), the replace of this user's open request for the
	// name, and the open limit.
	if err := fileRequestTable.openSlot(ctx, tx, in.RequestMeta, store.ErrTooManyFileRequests, in.Name); err != nil {
		return err
	}
	if err := exec(ctx, tx, `
INSERT INTO app.file_requests (id_hash, user_id, project_id, name, secret, reason, state, created_at, expires_at)
VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, 'pending', $7, $8)
`, hash, in.UserID.String(), in.ProjectID.String(), in.Name, in.Secret, in.Reason, in.CreatedAt, in.ExpiresAt); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit(ctx))
}

const fileRequestColumns = `r.user_id, r.project_id, r.name, r.secret, r.reason, r.state, r.created_at, r.expires_at, r.done_at`

func (d *DB) GetFileRequest(ctx context.Context, hash []byte) (store.FileRequest, error) {
	return scanFileRequest(d.pool.QueryRow(ctx, `
SELECT `+fileRequestColumns+`
FROM app.file_requests r JOIN app.projects p ON p.id = r.project_id
WHERE r.id_hash = $1 AND p.deleted_at IS NULL
`, hash))
}

func (d *DB) FillFileRequest(ctx context.Context, hash []byte, userID uuid.UUID, at time.Time, in store.FileWrite) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback(ctx)
	req, err := lockFileRequest(ctx, tx, hash, userID, at)
	if err != nil {
		return err
	}
	if !dbFileWriteOK(req.Name, in) {
		return store.ErrInvalid
	}
	// lockFileRequest holds the project row's lock.
	if err := writeProjectFileTx(ctx, tx, req.ProjectID, req.Name, in, userID, at, true); err != nil {
		return err
	}
	if err := exec(ctx, tx, `
UPDATE app.file_requests SET state = 'filled', done_at = $2 WHERE id_hash = $1
`, hash, at); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit(ctx))
}

func (d *DB) DeclineFileRequest(ctx context.Context, hash []byte, userID uuid.UUID, at time.Time) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback(ctx)
	if _, err := lockFileRequest(ctx, tx, hash, userID, at); err != nil {
		return err
	}
	if err := exec(ctx, tx, `
UPDATE app.file_requests SET state = 'declined', done_at = $2 WHERE id_hash = $1
`, hash, at); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit(ctx))
}

// lockFileRequest locks a request row of userID that can still be filled or
// declined: pending, unexpired, of a live project. It locks the project row
// before the request row (the order of file put and delete), and reads the
// request again under its lock.
func lockFileRequest(ctx context.Context, tx pgx.Tx, hash []byte, userID uuid.UUID, at time.Time) (store.FileRequest, error) {
	first, err := scanFileRequest(tx.QueryRow(ctx, `
SELECT `+fileRequestColumns+`
FROM app.file_requests r JOIN app.projects p ON p.id = r.project_id
WHERE r.id_hash = $1 AND p.deleted_at IS NULL
`, hash))
	if err != nil {
		return store.FileRequest{}, err
	}
	if first.UserID != userID {
		return store.FileRequest{}, store.ErrNotFound
	}
	if err := lockLiveProject(ctx, tx, first.ProjectID); err != nil {
		return store.FileRequest{}, err
	}
	req, err := scanFileRequest(tx.QueryRow(ctx, `
SELECT `+fileRequestColumns+`
FROM app.file_requests r
WHERE r.id_hash = $1
FOR UPDATE
`, hash))
	if err != nil {
		return store.FileRequest{}, err
	}
	if req.UserID != userID {
		return store.FileRequest{}, store.ErrNotFound
	}
	if err := req.OpenErr(at); err != nil {
		return store.FileRequest{}, err
	}
	return req, nil
}

// lockLiveProject takes the project row lock that file put and delete take.
// A missing or deleted project is ErrNotFound.
func lockLiveProject(ctx context.Context, tx pgx.Tx, projectID uuid.UUID) error {
	var id string
	err := tx.QueryRow(ctx, `
SELECT id::text FROM app.projects
WHERE id = $1::uuid AND deleted_at IS NULL
FOR UPDATE
`, projectID.String()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	return mapErr(err)
}

func scanFileRequest(row pgx.Row) (store.FileRequest, error) {
	var f store.FileRequest
	err := row.Scan(&f.UserID, &f.ProjectID, &f.Name, &f.Secret, &f.Reason, &f.State, &f.CreatedAt, &f.ExpiresAt, &f.DoneAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.FileRequest{}, store.ErrNotFound
	}
	if err != nil {
		return store.FileRequest{}, mapErr(err)
	}
	return f, nil
}
