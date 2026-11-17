package db

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Env set creation with key names only (api-v1 v1.27 item 4). The plan
// table is app.env_set_confirmations (migration 0017).

func (d *DB) PutEnvSetConfirmation(ctx context.Context, hash []byte, c store.EnvSetConfirmation) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback(ctx)
	// A new plan drops rows that expired more than store.RequestKeep ago
	// (spec-release-v0239 F4).
	if err := exec(ctx, tx, `
DELETE FROM app.env_set_confirmations WHERE expires_at < $1
`, c.CreatedAt.Add(-store.RequestKeep)); err != nil {
		return mapErr(err)
	}
	if err := exec(ctx, tx, `
INSERT INTO app.env_set_confirmations (
    token_hash, user_id, project_id, set_name, body, request_hash,
    idempotency_key, created_at, expires_at
) VALUES ($1, $2::uuid, $3::uuid, $4, $5::jsonb, $6, $7, $8, $9)
`, hash, c.UserID.String(), c.ProjectID.String(), c.SetName, string(c.Body),
		c.RequestHash, c.IdempotencyKey, c.CreatedAt, c.ExpiresAt); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit(ctx))
}

const envSetConfirmationCols = `
SELECT user_id::text, project_id::text, set_name, body, request_hash,
       idempotency_key, created_at, expires_at, used_at
FROM app.env_set_confirmations
WHERE token_hash = $1`

func (d *DB) GetEnvSetConfirmation(ctx context.Context, hash []byte) (store.EnvSetConfirmation, error) {
	return scanEnvSetConfirmation(d.pool.QueryRow(ctx, envSetConfirmationCols, hash))
}

func scanEnvSetConfirmation(row pgx.Row) (store.EnvSetConfirmation, error) {
	var c store.EnvSetConfirmation
	var userID, projectID string
	if err := row.Scan(&userID, &projectID, &c.SetName, &c.Body, &c.RequestHash,
		&c.IdempotencyKey, &c.CreatedAt, &c.ExpiresAt, &c.UsedAt); err != nil {
		return store.EnvSetConfirmation{}, mapErr(err)
	}
	var err error
	if c.UserID, err = parseID(userID); err != nil {
		return store.EnvSetConfirmation{}, err
	}
	if c.ProjectID, err = parseID(projectID); err != nil {
		return store.EnvSetConfirmation{}, err
	}
	return c, nil
}

// CreateEnvSetNames locks the plan (when confirming), creates the set and
// its values, and marks the plan used in one transaction. A conflict rolls
// back, so the plan stays usable.
func (d *DB) CreateEnvSetNames(ctx context.Context, projectID uuid.UUID, name string, keys []store.EnvKeyCreate, confirm []byte, actor uuid.UUID, at time.Time) (store.EnvSet, error) {
	if !catalog.ValidName(name) || !distinctCreateKeys(keys) {
		return store.EnvSet{}, store.ErrInvalid
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return store.EnvSet{}, mapErr(err)
	}
	defer tx.Rollback(ctx)
	if confirm != nil {
		c, err := scanEnvSetConfirmation(tx.QueryRow(ctx, envSetConfirmationCols+` FOR UPDATE`, confirm))
		if err != nil {
			return store.EnvSet{}, err
		}
		if c.UserID != actor || c.ProjectID != projectID || c.SetName != name || c.UsedAt != nil || !c.ExpiresAt.After(at) {
			return store.EnvSet{}, store.ErrNotFound
		}
	}
	tag, err := execTag(ctx, tx, `
INSERT INTO app.env_sets (project_id, name, updated_at, updated_by)
VALUES ($1::uuid, $2, $3, $4::uuid)
ON CONFLICT (project_id, name) DO NOTHING
`, projectID.String(), name, at, actor.String())
	if err != nil {
		return store.EnvSet{}, mapErr(err)
	}
	if tag == "INSERT 0 0" {
		return store.EnvSet{}, store.ErrConflict
	}
	for _, k := range keys {
		if err := insertEnvCreateValue(ctx, tx, projectID, name, k); err != nil {
			return store.EnvSet{}, err
		}
	}
	if confirm != nil {
		// Marked, not deleted, so a second confirm can say the plan was
		// used (api-v1 v1.32 item 1). The next plan insert prunes it after expiry.
		if err := exec(ctx, tx, `UPDATE app.env_set_confirmations SET used_at = $2 WHERE token_hash = $1`, confirm, at); err != nil {
			return store.EnvSet{}, mapErr(err)
		}
	}
	if err := d.rehashTx(ctx, tx, projectID, name, at); err != nil {
		return store.EnvSet{}, err
	}
	set, err := envSetView(ctx, tx, projectID, name)
	if err != nil {
		return store.EnvSet{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.EnvSet{}, mapErr(err)
	}
	return set, nil
}

// distinctCreateKeys reports whether every key name is valid and appears
// once. The memstore applies the same rule.
func distinctCreateKeys(keys []store.EnvKeyCreate) bool {
	seen := map[string]bool{}
	for _, k := range keys {
		if catalog.ValidateEnvKey(k.Key) != nil || seen[k.Key] {
			return false
		}
		seen[k.Key] = true
	}
	return true
}

// insertEnvCreateValue writes a generated secret (reveal_pending and
// generated), a placeholder (an empty value with needs_value), or a plain
// key with its value (api-v1 v1.32 item 2).
func insertEnvCreateValue(ctx context.Context, tx pgx.Tx, projectID uuid.UUID, name string, k store.EnvKeyCreate) error {
	if k.Generated || k.Secret {
		return mapErr(exec(ctx, tx, `
INSERT INTO app.env_values (project_id, set_name, key, is_secret, value, nonce, ciphertext, key_version, reveal_pending, needs_value, generated)
VALUES ($1::uuid, $2, $3, true, NULL, $4, $5, $6, $7, $8, $7)
`, projectID.String(), name, k.Key, k.Nonce, k.Ciphertext, k.KeyVersion, k.Generated, !k.Generated))
	}
	value := ""
	if k.Value != nil {
		value = *k.Value
	}
	return mapErr(exec(ctx, tx, `
INSERT INTO app.env_values (project_id, set_name, key, is_secret, value, nonce, ciphertext, key_version, reveal_pending, needs_value, generated)
VALUES ($1::uuid, $2, $3, false, $4, NULL, NULL, NULL, false, $5, false)
`, projectID.String(), name, k.Key, value, k.Value == nil))
}
