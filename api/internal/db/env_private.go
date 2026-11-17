package db

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.33: entry-private env sets (item 6, migration 0022), confirmed
// env set changes (items 1 and 2) and the open-requests list (item 8).

// syncPrivateEnv writes an entry's private set "@<entry>" inside the
// catalog write transaction: it creates the set on first use, inserts the
// new keys, removes keys that are no longer listed, and deletes the set
// when no key is left. A nil p leaves the set as it is.
func syncPrivateEnv(ctx context.Context, tx pgx.Tx, entry store.CatalogEntry, p *store.PrivateEnv, actor uuid.UUID, at time.Time) error {
	if p == nil {
		return nil
	}
	name := catalog.PrivateSet(entry.Name)
	pid := entry.ProjectID.String()
	if len(p.Keys) == 0 {
		return mapErr(exec(ctx, tx, `
DELETE FROM app.env_sets WHERE project_id = $1::uuid AND name = $2 AND entry_id = $3::uuid
`, pid, name, entry.ID.String()))
	}
	if err := exec(ctx, tx, `
INSERT INTO app.env_sets (project_id, name, updated_at, updated_by, entry_id)
VALUES ($1::uuid, $2, $3, $4::uuid, $5::uuid)
ON CONFLICT (project_id, name) DO NOTHING
`, pid, name, at, actor.String(), entry.ID.String()); err != nil {
		return mapErr(err)
	}
	// The set row is locked, and must be this entry's (a leftover of an
	// earlier entry with the same name is gone with that entry's cascade).
	var owner *string
	if err := tx.QueryRow(ctx, `
SELECT entry_id::text FROM app.env_sets
WHERE project_id = $1::uuid AND name = $2
FOR UPDATE
`, pid, name).Scan(&owner); err != nil {
		return mapErr(err)
	}
	if owner == nil || *owner != entry.ID.String() {
		return store.ErrConflict
	}
	changed := false
	tag, err := execTag(ctx, tx, `
DELETE FROM app.env_values
WHERE project_id = $1::uuid AND set_name = $2 AND NOT (key = ANY($3::text[]))
`, pid, name, p.Keys)
	if err != nil {
		return mapErr(err)
	}
	if tag != "DELETE 0" {
		changed = true
	}
	for _, k := range p.Add {
		var exists bool
		if err := tx.QueryRow(ctx, `
SELECT EXISTS (SELECT 1 FROM app.env_values WHERE project_id = $1::uuid AND set_name = $2 AND key = $3)
`, pid, name, k.Key).Scan(&exists); err != nil {
			return mapErr(err)
		}
		if exists {
			// An existing private key keeps its value (no regenerate).
			continue
		}
		k.Secret = true
		k.Value = nil
		if err := insertEnvCreateValue(ctx, tx, entry.ProjectID, name, k); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		return mapErr(exec(ctx, tx, `
UPDATE app.env_sets SET updated_at = $3, updated_by = $4::uuid
WHERE project_id = $1::uuid AND name = $2
`, pid, name, at, actor.String()))
	}
	return nil
}

// ApplyEnvSetChange implements store.Store.ApplyEnvSetChange.
func (d *DB) ApplyEnvSetChange(ctx context.Context, projectID uuid.UUID, name string, ch store.EnvSetChange, confirm []byte, seal store.EnvSealer, actor uuid.UUID, at time.Time) (store.EnvSet, error) {
	if !catalog.ValidName(name) {
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
	if err := requireLiveProject(ctx, tx, projectID); err != nil {
		return store.EnvSet{}, err
	}
	if err := lockEnvSet(ctx, tx, projectID, name); err != nil {
		return store.EnvSet{}, err
	}
	if ch.StateHash != "" {
		mats, err := envMaterialRows(ctx, tx, projectID, name, "FOR UPDATE")
		if err != nil {
			return store.EnvSet{}, err
		}
		if store.EnvStateHash(mats) != ch.StateHash {
			return store.EnvSet{}, store.ErrEnvSetChanged
		}
	}
	pid := projectID.String()
	var set store.EnvSet
	if ch.Delete {
		if err := d.envSetUnused(ctx, tx, projectID, name); err != nil {
			return store.EnvSet{}, err
		}
		if err := exec(ctx, tx, `DELETE FROM app.env_sets WHERE project_id = $1::uuid AND name = $2`, pid, name); err != nil {
			return store.EnvSet{}, mapErr(err)
		}
		set = store.EnvSet{Name: name}
	} else {
		if err := applyEnvKeyChanges(ctx, tx, projectID, name, ch, seal); err != nil {
			return store.EnvSet{}, err
		}
		if err := exec(ctx, tx, `
UPDATE app.env_sets SET updated_at = $3, updated_by = $4::uuid
WHERE project_id = $1::uuid AND name = $2
`, pid, name, at, actor.String()); err != nil {
			return store.EnvSet{}, mapErr(err)
		}
		if err := d.rehashTx(ctx, tx, projectID, name, at); err != nil {
			return store.EnvSet{}, err
		}
		set, err = envSetView(ctx, tx, projectID, name)
		if err != nil {
			return store.EnvSet{}, err
		}
	}
	if confirm != nil {
		if err := exec(ctx, tx, `UPDATE app.env_set_confirmations SET used_at = $2 WHERE token_hash = $1`, confirm, at); err != nil {
			return store.EnvSet{}, mapErr(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return store.EnvSet{}, mapErr(err)
	}
	return set, nil
}

// envSetUnused is ErrConflict when an image or program entry of the project
// names the set in env_sets or secret_env (the DeleteEnvSet rule).
func (d *DB) envSetUnused(ctx context.Context, q queryer, projectID uuid.UUID, name string) error {
	var used bool
	if err := q.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM app.catalog_entries e
    WHERE e.project_id = $1::uuid AND e.kind IN ('image', 'program')
      AND (
        COALESCE(e.spec->'env_sets', '[]'::jsonb) ? $2
        OR EXISTS (
            SELECT 1 FROM jsonb_each_text(COALESCE(e.spec->'secret_env', '{}'::jsonb)) kv
            WHERE split_part(kv.value, '.', 1) = $2
        )
      )
)
`, projectID.String(), name).Scan(&used); err != nil {
		return mapErr(err)
	}
	if used {
		return store.ErrConflict
	}
	return nil
}

// applyEnvKeyChanges applies the key actions of one change under the set
// lock. Each key's row is locked and checked first, so a key that changed
// since the plan is ErrConflict before anything is written.
func applyEnvKeyChanges(ctx context.Context, tx pgx.Tx, projectID uuid.UUID, name string, ch store.EnvSetChange, seal store.EnvSealer) error {
	pid := projectID.String()
	rows, err := tx.Query(ctx, `
SELECT key, is_secret, COALESCE(value, ''), needs_value FROM app.env_values
WHERE project_id = $1::uuid AND set_name = $2
FOR UPDATE
`, pid, name)
	if err != nil {
		return mapErr(err)
	}
	type row struct {
		secret     bool
		value      string
		needsValue bool
	}
	have := map[string]row{}
	for rows.Next() {
		var key, value string
		var secret, needsValue bool
		if err := rows.Scan(&key, &secret, &value, &needsValue); err != nil {
			rows.Close()
			return mapErr(err)
		}
		have[key] = row{secret: secret, value: value, needsValue: needsValue}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return mapErr(err)
	}
	for key := range ch.Set {
		if r, ok := have[key]; !ok || r.secret {
			return store.ErrConflict
		}
	}
	for _, k := range ch.Add {
		if _, ok := have[k.Key]; ok {
			return store.ErrConflict
		}
	}
	for _, key := range append(append([]string{}, ch.Remove...), ch.MakeSecret...) {
		if _, ok := have[key]; !ok {
			return store.ErrConflict
		}
	}
	// make_secret never seals an empty value; a needs_value placeholder may
	// still become a secret placeholder (spec-release-v0239 F6).
	for _, key := range ch.MakeSecret {
		if r := have[key]; !r.secret && !r.needsValue && r.value == "" {
			return &store.EmptySecretKeyError{Key: key}
		}
	}
	setKeys := make([]string, 0, len(ch.Set))
	for key := range ch.Set {
		setKeys = append(setKeys, key)
	}
	sort.Strings(setKeys)
	for _, key := range setKeys {
		if err := upsertEnvValue(ctx, tx, projectID, name, key, false, ch.Set[key], nil, nil, 0, false, false); err != nil {
			return err
		}
	}
	for _, k := range ch.Add {
		if err := insertEnvCreateValue(ctx, tx, projectID, name, k); err != nil {
			return err
		}
	}
	if len(ch.Remove) > 0 {
		if err := exec(ctx, tx, `
DELETE FROM app.env_values WHERE project_id = $1::uuid AND set_name = $2 AND key = ANY($3::text[])
`, pid, name, ch.Remove); err != nil {
			return mapErr(err)
		}
	}
	for _, key := range ch.MakeSecret {
		r := have[key]
		if r.secret {
			continue
		}
		sealed, err := seal(key, r.value)
		if err != nil {
			return err
		}
		if err := exec(ctx, tx, `
UPDATE app.env_values
SET is_secret = true, value = NULL, nonce = $4, ciphertext = $5, key_version = $6,
    reveal_pending = false, generated = false
WHERE project_id = $1::uuid AND set_name = $2 AND key = $3
`, pid, name, key, sealed.Nonce, sealed.Ciphertext, sealed.KeyVersion); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// ListOpenRequests implements store.Store.ListOpenRequests with one query
// over the three request tables.
func (d *DB) ListOpenRequests(ctx context.Context, userID uuid.UUID, at time.Time, max int) ([]store.OpenRequest, error) {
	rows, err := d.pool.Query(ctx, `
SELECT r.kind, r.id_hash, r.project_id::text, p.name, r.subject, r.created_at, r.expires_at
FROM (
    SELECT 'value' AS kind, id_hash, project_id, set_name || '.' || key AS subject, created_at, expires_at
    FROM app.env_value_requests WHERE user_id = $1::uuid AND state = 'pending' AND expires_at > $2
    UNION ALL
    SELECT 'file', id_hash, project_id, name, created_at, expires_at
    FROM app.file_requests WHERE user_id = $1::uuid AND state = 'pending' AND expires_at > $2
    UNION ALL
    SELECT 'connect', id_hash, project_id, 'ghcr.io', created_at, expires_at
    FROM app.registry_connect_requests WHERE user_id = $1::uuid AND state = 'pending' AND expires_at > $2
) r
JOIN app.projects p ON p.id = r.project_id AND p.deleted_at IS NULL
ORDER BY r.created_at DESC, r.id_hash
LIMIT $3
`, userID.String(), at, max)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.OpenRequest{}
	for rows.Next() {
		var item store.OpenRequest
		var pid string
		if err := rows.Scan(&item.Type, &item.Hash, &pid, &item.ProjectName, &item.Subject, &item.CreatedAt, &item.ExpiresAt); err != nil {
			return nil, mapErr(err)
		}
		if item.ProjectID, err = parseID(pid); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}
