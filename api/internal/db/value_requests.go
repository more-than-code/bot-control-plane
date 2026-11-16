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

// Value requests (api-v1 v1.28). The table has no value column; a fill
// writes the value into app.env_values in the same transaction.

func (d *DB) CreateValueRequest(ctx context.Context, hash []byte, in store.ValueRequest) error {
	if !catalog.ValidEnvSetName(in.Set) {
		return store.ErrInvalid
	}
	if err := catalog.ValidateEnvKey(in.Key); err != nil {
		return store.ErrInvalid
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback(ctx)
	if err := valueRequestTable.lockCreates(ctx, tx, in.UserID); err != nil {
		return err
	}
	if err := requireLiveProject(ctx, tx, in.ProjectID); err != nil {
		return err
	}
	// The set row is locked so a fill or delete of this set waits.
	if err := lockEnvSet(ctx, tx, in.ProjectID, in.Set); err != nil {
		return err
	}
	if _, err := keyOpenRow(ctx, tx, in.ProjectID, in.Set, in.Key); err != nil {
		return err
	}
	// After the set lock (the order fill takes): the 7-day cleanup (api-v1
	// v1.28 item 7), the replace of this user's open request for the key
	// (rev 3), and the open limit.
	if err := valueRequestTable.openSlot(ctx, tx, in.RequestMeta, store.ErrTooManyRequests, in.Set, in.Key); err != nil {
		return err
	}
	if err := exec(ctx, tx, `
INSERT INTO app.env_value_requests (id_hash, user_id, project_id, set_name, key, reason, state, created_at, expires_at)
VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, 'pending', $7, $8)
`, hash, in.UserID.String(), in.ProjectID.String(), in.Set, in.Key, in.Reason, in.CreatedAt, in.ExpiresAt); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit(ctx))
}

func (d *DB) GetValueRequest(ctx context.Context, hash []byte) (store.ValueRequest, error) {
	return scanValueRequest(d.pool.QueryRow(ctx, `
SELECT r.user_id, r.project_id, r.set_name, r.key, r.reason, r.state, r.created_at, r.expires_at, r.done_at
FROM app.env_value_requests r JOIN app.projects p ON p.id = r.project_id
WHERE r.id_hash = $1 AND p.deleted_at IS NULL
`, hash))
}

func (d *DB) FillValueRequest(ctx context.Context, hash []byte, userID uuid.UUID, at time.Time, in store.EnvKeyPut) (store.EnvKey, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return store.EnvKey{}, mapErr(err)
	}
	defer tx.Rollback(ctx)
	req, err := lockValueRequest(ctx, tx, hash, userID, at)
	if err != nil {
		return store.EnvKey{}, err
	}
	if in.Key != req.Key {
		return store.EnvKey{}, store.ErrInvalid
	}
	// lockValueRequest holds the set row's lock.
	exists, err := keyOpenRow(ctx, tx, req.ProjectID, req.Set, req.Key)
	if err != nil {
		return store.EnvKey{}, err
	}
	// A secret key that is not a placeholder is a rotation (v1.33 item 3).
	replaced := false
	if exists {
		if err := tx.QueryRow(ctx, `
SELECT is_secret AND NOT needs_value FROM app.env_values
WHERE project_id = $1::uuid AND set_name = $2 AND key = $3
`, req.ProjectID.String(), req.Set, req.Key).Scan(&replaced); err != nil {
			return store.EnvKey{}, mapErr(err)
		}
	}
	// Written secret, whatever a placeholder was (plain to secret is the
	// allowed direction); upsertEnvValue sets every v1.27 flag.
	if err := upsertEnvValue(ctx, tx, req.ProjectID, req.Set, req.Key, true, "", in.Nonce, in.Ciphertext, in.KeyVersion, !exists, in.Generated); err != nil {
		return store.EnvKey{}, err
	}
	pid := req.ProjectID.String()
	if err := exec(ctx, tx, `
UPDATE app.env_sets SET updated_at = $3, updated_by = $4::uuid
WHERE project_id = $1::uuid AND name = $2
`, pid, req.Set, at, userID.String()); err != nil {
		return store.EnvKey{}, mapErr(err)
	}
	if err := d.rehashTx(ctx, tx, req.ProjectID, req.Set, at); err != nil {
		return store.EnvKey{}, err
	}
	if err := exec(ctx, tx, `
UPDATE app.env_value_requests SET state = 'filled', done_at = $2 WHERE id_hash = $1
`, hash, at); err != nil {
		return store.EnvKey{}, mapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return store.EnvKey{}, mapErr(err)
	}
	return store.EnvKey{Key: req.Key, IsSecret: true, RevealPending: in.Generated, Generated: in.Generated, Replaced: replaced}, nil
}

func (d *DB) DeclineValueRequest(ctx context.Context, hash []byte, userID uuid.UUID, at time.Time) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback(ctx)
	if _, err := lockValueRequest(ctx, tx, hash, userID, at); err != nil {
		return err
	}
	if err := exec(ctx, tx, `
UPDATE app.env_value_requests SET state = 'declined', done_at = $2 WHERE id_hash = $1
`, hash, at); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit(ctx))
}

// lockValueRequest locks a request row of userID that can still be filled
// or declined: pending, unexpired, of a live project. It locks the set row
// before the request row, the order a set delete takes (the set, then the
// cascade to its requests), so a fill racing a set delete waits instead of
// deadlocking. The request is read again under its lock.
func lockValueRequest(ctx context.Context, tx pgx.Tx, hash []byte, userID uuid.UUID, at time.Time) (store.ValueRequest, error) {
	first, err := scanValueRequest(tx.QueryRow(ctx, `
SELECT r.user_id, r.project_id, r.set_name, r.key, r.reason, r.state, r.created_at, r.expires_at, r.done_at
FROM app.env_value_requests r JOIN app.projects p ON p.id = r.project_id
WHERE r.id_hash = $1 AND p.deleted_at IS NULL
`, hash))
	if err != nil {
		return store.ValueRequest{}, err
	}
	if first.UserID != userID {
		return store.ValueRequest{}, store.ErrNotFound
	}
	if err := lockEnvSet(ctx, tx, first.ProjectID, first.Set); err != nil {
		return store.ValueRequest{}, err
	}
	req, err := scanValueRequest(tx.QueryRow(ctx, `
SELECT r.user_id, r.project_id, r.set_name, r.key, r.reason, r.state, r.created_at, r.expires_at, r.done_at
FROM app.env_value_requests r JOIN app.projects p ON p.id = r.project_id
WHERE r.id_hash = $1 AND p.deleted_at IS NULL
FOR UPDATE OF r
`, hash))
	if err != nil {
		return store.ValueRequest{}, err
	}
	if req.UserID != userID {
		return store.ValueRequest{}, store.ErrNotFound
	}
	if err := req.OpenErr(at); err != nil {
		return store.ValueRequest{}, err
	}
	return req, nil
}

func scanValueRequest(row pgx.Row) (store.ValueRequest, error) {
	var v store.ValueRequest
	err := row.Scan(&v.UserID, &v.ProjectID, &v.Set, &v.Key, &v.Reason, &v.State, &v.CreatedAt, &v.ExpiresAt, &v.DoneAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ValueRequest{}, store.ErrNotFound
	}
	if err != nil {
		return store.ValueRequest{}, mapErr(err)
	}
	return v, nil
}

// keyOpenRow locks the key's row and reports whether it exists. A plain key
// with a value is ErrKeyExists; a secret key with a value may be replaced
// (api-v1 v1.33 item 3, a rotation).
func keyOpenRow(ctx context.Context, tx pgx.Tx, projectID uuid.UUID, set, key string) (bool, error) {
	var needs, secret bool
	err := tx.QueryRow(ctx, `
SELECT needs_value, is_secret FROM app.env_values
WHERE project_id = $1::uuid AND set_name = $2 AND key = $3
FOR UPDATE
`, projectID.String(), set, key).Scan(&needs, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, mapErr(err)
	}
	if !needs && !secret {
		return true, store.ErrKeyExists
	}
	return true, nil
}

// requireLiveProject is ErrNotFound for a missing or deleted project.
func requireLiveProject(ctx context.Context, tx pgx.Tx, projectID uuid.UUID) error {
	var alive bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (SELECT 1 FROM app.projects WHERE id = $1::uuid AND deleted_at IS NULL)
`, projectID.String()).Scan(&alive); err != nil {
		return mapErr(err)
	}
	if !alive {
		return store.ErrNotFound
	}
	return nil
}
