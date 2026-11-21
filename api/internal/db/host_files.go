package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Host config files (api-v1 v1.44, migration 0029).

func (d *DB) SaveHostFileVersion(ctx context.Context, in store.HostFileSave) (int, error) {
	if !store.ValidHostFileSave(in) {
		return 0, store.ErrInvalid
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return 0, mapErr(err)
	}
	defer tx.Rollback(ctx)
	// One save at a time per file: the next number, the base check and the
	// prune below then never race (the primary key stays a backstop).
	if err := lockHostFile(ctx, tx, in.HostID, in.Name); err != nil {
		return 0, mapErr(err)
	}
	protect := 0
	if in.Protect != nil {
		protect = in.Protect()
	}
	var latest int
	if err := tx.QueryRow(ctx, `
SELECT COALESCE(max(version), 0) FROM app.host_file_versions WHERE host_id = $1::uuid AND name = $2
`, in.HostID.String(), in.Name).Scan(&latest); err != nil {
		return 0, mapErr(err)
	}
	if in.BaseVersion != nil && *in.BaseVersion != latest {
		return 0, store.ErrConflict
	}
	version := latest + 1
	var sha, plain any
	var nonce, cipher []byte
	var keyVersion any
	if in.Secret {
		n, c, kv, err := in.Seal(version)
		if err != nil {
			return 0, err
		}
		nonce, cipher, keyVersion = n, c, kv
	} else {
		sha, plain = in.SHA256, in.Plain
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO app.host_file_versions (host_id, name, version, secret, size, sha256, content, nonce, content_cipher, key_version, created_by, created_at)
VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::uuid, $12)
`, in.HostID.String(), in.Name, version, in.Secret, in.Size, sha, plain, nonce, cipher, keyVersion, in.CreatedBy.String(), in.At); err != nil {
		return 0, mapErr(err)
	}
	if _, err := tx.Exec(ctx, `
DELETE FROM app.host_file_versions
WHERE host_id = $1::uuid AND name = $2 AND version <= $3 AND version <> $4
  AND version IS DISTINCT FROM (
      SELECT a.version FROM app.host_file_applies a
      WHERE a.host_id = $1::uuid AND a.name = $2 AND a.outcome IN ('succeeded', 'unchanged')
      ORDER BY a.at DESC, a.id DESC
      LIMIT 1
  )
`, in.HostID.String(), in.Name, version-store.HostFileKeep, protect); err != nil {
		return 0, mapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, mapErr(err)
	}
	return version, nil
}

func (d *DB) ListHostFileVersions(ctx context.Context, hostID uuid.UUID, name string) ([]store.HostFileVersion, error) {
	rows, err := d.pool.Query(ctx, `
SELECT version, secret, size, COALESCE(sha256, ''), created_by::text, created_at
FROM app.host_file_versions
WHERE host_id = $1::uuid AND name = $2
ORDER BY version DESC
`, hostID.String(), name)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.HostFileVersion{}
	for rows.Next() {
		v := store.HostFileVersion{HostID: hostID, Name: name}
		var by string
		if err := rows.Scan(&v.Version, &v.Secret, &v.Size, &v.SHA256, &by, &v.CreatedAt); err != nil {
			return nil, mapErr(err)
		}
		if v.CreatedBy, err = parseID(by); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, mapErr(rows.Err())
}

// lockHostFile takes the per-file advisory lock for the transaction: saves
// and version reads of one file never interleave.
func lockHostFile(ctx context.Context, q queryer, hostID uuid.UUID, name string) error {
	return exec(ctx, q, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "host_file:"+hostID.String()+":"+name)
}

func (d *DB) GetHostFileVersion(ctx context.Context, hostID uuid.UUID, name string, version int) (store.HostFileVersion, error) {
	v := store.HostFileVersion{HostID: hostID, Name: name}
	var by string
	var plain *string
	var keyVersion *int
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return store.HostFileVersion{}, mapErr(err)
	}
	defer tx.Rollback(ctx)
	if err := lockHostFile(ctx, tx, hostID, name); err != nil {
		return store.HostFileVersion{}, mapErr(err)
	}
	err = tx.QueryRow(ctx, `
SELECT version, secret, size, COALESCE(sha256, ''), content, nonce, content_cipher, key_version, created_by::text, created_at
FROM app.host_file_versions
WHERE host_id = $1::uuid AND name = $2 AND ($3 <= 0 OR version = $3)
ORDER BY version DESC
LIMIT 1
`, hostID.String(), name, version).Scan(&v.Version, &v.Secret, &v.Size, &v.SHA256, &plain, &v.Nonce, &v.Ciphertext, &keyVersion, &by, &v.CreatedAt)
	if err != nil {
		return store.HostFileVersion{}, mapErr(err)
	}
	if plain != nil {
		v.Plain = *plain
	}
	if keyVersion != nil {
		v.KeyVersion = *keyVersion
	}
	if v.CreatedBy, err = parseID(by); err != nil {
		return store.HostFileVersion{}, err
	}
	return v, nil
}

func (d *DB) RecordHostFileApply(ctx context.Context, in store.HostFileApply) error {
	if !store.ValidHostFileName(in.Name) || !store.ValidHostFileOutcome(in.Outcome) || in.Version < 1 {
		return store.ErrInvalid
	}
	_, err := d.pool.Exec(ctx, `
INSERT INTO app.host_file_applies (id, host_id, name, version, outcome, stage, reason, took_ms, applied_by, at)
VALUES ($1::uuid, $2::uuid, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), $8, $9::uuid, $10)
`, uuid.NewString(), in.HostID.String(), in.Name, in.Version, in.Outcome, in.Stage, in.Reason, in.TookMS, in.By.String(), in.At)
	return mapErr(err)
}

func (d *DB) HostFileStates(ctx context.Context, hostID uuid.UUID) ([]store.HostFileState, error) {
	rows, err := d.pool.Query(ctx, `
WITH names AS (
    SELECT name FROM app.host_file_versions WHERE host_id = $1::uuid
    UNION
    SELECT name FROM app.host_file_applies WHERE host_id = $1::uuid
)
SELECT n.name,
       (SELECT max(v.version) FROM app.host_file_versions v WHERE v.host_id = $1::uuid AND v.name = n.name),
       (SELECT a.version FROM app.host_file_applies a
         WHERE a.host_id = $1::uuid AND a.name = n.name AND a.outcome IN ('succeeded', 'unchanged')
         ORDER BY a.at DESC, a.id DESC LIMIT 1),
       l.version, l.outcome, COALESCE(l.stage, ''), COALESCE(l.reason, ''), COALESCE(l.took_ms, 0), l.applied_by::text, l.at
FROM names n
LEFT JOIN LATERAL (
    SELECT a.version, a.outcome, a.stage, a.reason, a.took_ms, a.applied_by, a.at
    FROM app.host_file_applies a
    WHERE a.host_id = $1::uuid AND a.name = n.name
    ORDER BY a.at DESC, a.id DESC
    LIMIT 1
) l ON true
ORDER BY n.name
`, hostID.String())
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.HostFileState{}
	for rows.Next() {
		var s store.HostFileState
		var lastVersion *int
		var outcome, by *string
		var stage, reason string
		var took int
		var at *time.Time
		if err := rows.Scan(&s.Name, &s.LatestVersion, &s.AppliedVersion, &lastVersion, &outcome, &stage, &reason, &took, &by, &at); err != nil {
			return nil, mapErr(err)
		}
		if lastVersion != nil && outcome != nil && by != nil && at != nil {
			a := store.HostFileApply{HostID: hostID, Name: s.Name, Version: *lastVersion, Outcome: *outcome, Stage: stage, Reason: reason, TookMS: took, At: *at}
			if a.By, err = parseID(*by); err != nil {
				return nil, err
			}
			s.LastApply = &a
		}
		out = append(out, s)
	}
	return out, mapErr(rows.Err())
}

func (d *DB) GetHostFilesStatus(ctx context.Context, hostID uuid.UUID) (*store.HostFilesStatus, error) {
	var raw []byte
	if err := d.pool.QueryRow(ctx, `SELECT host_files_status FROM app.hosts WHERE id = $1::uuid`, hostID.String()).Scan(&raw); err != nil {
		return nil, mapErr(err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var s store.HostFilesStatus
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, errors.New("host files status unreadable")
	}
	return &s, nil
}

func (d *DB) SetHostFilesStatus(ctx context.Context, hostID uuid.UUID, status store.HostFilesStatus) error {
	if status.Files == nil {
		status.Files = []store.HostFileStatus{}
	}
	raw, err := json.Marshal(status)
	if err != nil {
		return err
	}
	tag, err := d.pool.Exec(ctx, `UPDATE app.hosts SET host_files_status = $2::jsonb WHERE id = $1::uuid`, hostID.String(), raw)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *DB) ListHostFilesStatuses(ctx context.Context) ([]store.HostFilesStatusRow, error) {
	rows, err := d.pool.Query(ctx, `
SELECT id::text, org_id::text, name, host_files_status
FROM app.hosts
WHERE revoked_at IS NULL AND host_files_status IS NOT NULL
ORDER BY id
`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.HostFilesStatusRow{}
	for rows.Next() {
		var id, org string
		var raw []byte
		var r store.HostFilesStatusRow
		if err := rows.Scan(&id, &org, &r.Name, &raw); err != nil {
			return nil, mapErr(err)
		}
		if r.HostID, err = parseID(id); err != nil {
			return nil, err
		}
		if r.OrgID, err = parseID(org); err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &r.Status) != nil {
			continue
		}
		out = append(out, r)
	}
	return out, mapErr(rows.Err())
}
