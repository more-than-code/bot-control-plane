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

// Connect requests (api-v1 v1.31). The table has no token column; a
// connect writes the token into app.registries in the same transaction.

// connectRequestTable is the shared request SQL for this table. One open
// request per user and project, so there is no subject column.
var connectRequestTable = requestTable{name: "app.registry_connect_requests", lockPrefix: "registry_connect_requests:"}

const connectRequestCols = `r.user_id, r.project_id, r.reason, r.state, r.created_at, r.expires_at, r.done_at`

func (d *DB) CreateConnectRequest(ctx context.Context, hash []byte, in store.ConnectRequest) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback(ctx)
	if err := connectRequestTable.lockCreates(ctx, tx, in.UserID); err != nil {
		return err
	}
	if err := requireLiveProject(ctx, tx, in.ProjectID); err != nil {
		return err
	}
	// The 7-day cleanup (item 8), the replace of this user's open request
	// for the project, and the open limit.
	if err := connectRequestTable.openSlot(ctx, tx, in.RequestMeta, store.ErrTooManyConnectRequests); err != nil {
		return err
	}
	if err := exec(ctx, tx, `
INSERT INTO app.registry_connect_requests (id_hash, user_id, project_id, reason, state, created_at, expires_at)
VALUES ($1, $2::uuid, $3::uuid, $4, 'pending', $5, $6)
`, hash, in.UserID.String(), in.ProjectID.String(), in.Reason, in.CreatedAt, in.ExpiresAt); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit(ctx))
}

func (d *DB) GetConnectRequest(ctx context.Context, hash []byte) (store.ConnectRequest, error) {
	return scanConnectRequest(d.pool.QueryRow(ctx, `
SELECT `+connectRequestCols+`
FROM app.registry_connect_requests r JOIN app.projects p ON p.id = r.project_id
WHERE r.id_hash = $1 AND p.deleted_at IS NULL
`, hash))
}

func (d *DB) DeclineConnectRequest(ctx context.Context, hash []byte, userID uuid.UUID, at time.Time) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback(ctx)
	req, err := lockConnectRequest(ctx, tx, hash)
	if err != nil {
		return err
	}
	if req.UserID != userID {
		return store.ErrNotFound
	}
	if err := req.OpenErr(at); err != nil {
		return err
	}
	if err := exec(ctx, tx, `
UPDATE app.registry_connect_requests SET state = 'declined', done_at = $2 WHERE id_hash = $1
`, hash, at); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit(ctx))
}

// ConnectRegistry writes the login and marks the bound request connected
// in one transaction (api-v1 v1.31 item 6). The request row is locked
// first, so a decline racing the callback either lands before (and the
// request is left declined) or waits and finds it connected.
func (d *DB) ConnectRegistry(ctx context.Context, projectID uuid.UUID, host string, in store.RegistryWrite, actor uuid.UUID, at time.Time, requestHash []byte) (store.Registry, error) {
	if !catalog.ValidRegistryHost(host) {
		return store.Registry{}, store.ErrInvalid
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return store.Registry{}, mapErr(err)
	}
	defer tx.Rollback(ctx)
	req, err := lockConnectRequest(ctx, tx, requestHash)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Registry{}, err
	}
	found := err == nil
	reg, err := putRegistryRow(ctx, tx, projectID, host, in, actor, at)
	if err != nil {
		return store.Registry{}, err
	}
	if found && req.UserID == actor && req.ProjectID == projectID && req.OpenErr(at) == nil {
		if err := exec(ctx, tx, `
UPDATE app.registry_connect_requests SET state = 'connected', done_at = $2 WHERE id_hash = $1
`, requestHash, at); err != nil {
			return store.Registry{}, mapErr(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Registry{}, mapErr(err)
	}
	return reg, nil
}

// lockConnectRequest reads and locks one request of a live project.
func lockConnectRequest(ctx context.Context, tx pgx.Tx, hash []byte) (store.ConnectRequest, error) {
	return scanConnectRequest(tx.QueryRow(ctx, `
SELECT `+connectRequestCols+`
FROM app.registry_connect_requests r JOIN app.projects p ON p.id = r.project_id
WHERE r.id_hash = $1 AND p.deleted_at IS NULL
FOR UPDATE OF r
`, hash))
}

func scanConnectRequest(row pgx.Row) (store.ConnectRequest, error) {
	var c store.ConnectRequest
	err := row.Scan(&c.UserID, &c.ProjectID, &c.Reason, &c.State, &c.CreatedAt, &c.ExpiresAt, &c.DoneAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ConnectRequest{}, store.ErrNotFound
	}
	if err != nil {
		return store.ConnectRequest{}, mapErr(err)
	}
	return c, nil
}
