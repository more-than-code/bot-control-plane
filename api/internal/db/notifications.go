package db

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Notification read marks (api-v1 v1.37 item 3, migration 0024).

func (d *DB) MarkNotificationsRead(ctx context.Context, userID uuid.UUID, keys, keep []string, at time.Time) error {
	for _, k := range keys {
		if !store.ValidNotificationKey(k) {
			return store.ErrInvalid
		}
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback(ctx)
	// COALESCE: a nil keep is NULL, and key <> ALL(NULL) would keep every row.
	if _, err := tx.Exec(ctx, `
DELETE FROM app.notification_reads
WHERE user_id = $1::uuid AND read_at < $2
  AND key <> ALL(COALESCE($3::text[], '{}'::text[]))
`, userID.String(), at.Add(-store.NotificationReadRetention), keep); err != nil {
		return mapErr(err)
	}
	// DISTINCT: one upsert may not touch the same row twice.
	if _, err := tx.Exec(ctx, `
INSERT INTO app.notification_reads (user_id, key, read_at)
SELECT $1::uuid, k, $3
FROM (SELECT DISTINCT unnest($2::text[]) AS k) AS u
ON CONFLICT (user_id, key) DO UPDATE SET read_at = EXCLUDED.read_at
`, userID.String(), keys, at); err != nil {
		return mapErr(err)
	}
	// Cap: the newest MaxNotificationMarks stay; COLLATE "C" orders keys
	// by bytes, as memstore does.
	if _, err := tx.Exec(ctx, `
DELETE FROM app.notification_reads
WHERE user_id = $1::uuid
  AND key <> ALL(COALESCE($2::text[], '{}'::text[]))
  AND key IN (
    SELECT key FROM app.notification_reads
    WHERE user_id = $1::uuid
    ORDER BY read_at DESC, key COLLATE "C" DESC
    OFFSET $3
  )
`, userID.String(), keep, store.MaxNotificationMarks); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit(ctx))
}

func (d *DB) NotificationReads(ctx context.Context, userID uuid.UUID, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := d.pool.Query(ctx, `
SELECT key FROM app.notification_reads
WHERE user_id = $1::uuid AND key = ANY($2::text[])
`, userID.String(), keys)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, mapErr(err)
		}
		out[k] = true
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

func (d *DB) NoticeHostUpdates(ctx context.Context, hostIDs []uuid.UUID, version string, at time.Time) (map[uuid.UUID]time.Time, error) {
	out := map[uuid.UUID]time.Time{}
	if len(hostIDs) == 0 {
		return out, nil
	}
	// Sorted ids and row locks taken in id order: two lists with
	// overlapping hosts lock them in the same order and cannot deadlock.
	ids := make([]string, len(hostIDs))
	for i, id := range hostIDs {
		ids[i] = id.String()
	}
	sort.Strings(ids)
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
DELETE FROM app.host_update_notices
WHERE (host_id, version) IN (
  SELECT host_id, version FROM app.host_update_notices
  WHERE host_id = ANY($1::uuid[]) AND version <> $2
  ORDER BY host_id, version
  FOR UPDATE
)
`, ids, version); err != nil {
		return nil, mapErr(err)
	}
	// Only hosts that exist: a host deleted meanwhile is skipped, not an error.
	if _, err := tx.Exec(ctx, `
INSERT INTO app.host_update_notices (host_id, version, noticed_at)
SELECT h.id, $2, $3 FROM app.hosts h WHERE h.id = ANY($1::uuid[])
ORDER BY h.id
ON CONFLICT (host_id, version) DO NOTHING
`, ids, version, at); err != nil {
		return nil, mapErr(err)
	}
	rows, err := tx.Query(ctx, `
SELECT host_id::text, noticed_at FROM app.host_update_notices
WHERE host_id = ANY($1::uuid[]) AND version = $2
`, ids, version)
	if err != nil {
		return nil, mapErr(err)
	}
	for rows.Next() {
		var id string
		var t time.Time
		if err := rows.Scan(&id, &t); err != nil {
			rows.Close()
			return nil, mapErr(err)
		}
		out[uuid.MustParse(id)] = t.UTC()
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, mapErr(tx.Commit(ctx))
}

// Release first-served times (migration 0038, api-v1 v1.57).

func (d *DB) RecordReleaseServed(ctx context.Context, version string, at time.Time) error {
	if !store.ValidReleaseVersion(version) {
		return store.ErrInvalid
	}
	_, err := d.pool.Exec(ctx, `
INSERT INTO app.release_first_served (version, first_served_at) VALUES ($1, $2)
ON CONFLICT (version) DO NOTHING
`, version, at)
	return mapErr(err)
}

func (d *DB) ReleasesServed(ctx context.Context, versions []string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	if len(versions) == 0 {
		return out, nil
	}
	rows, err := d.pool.Query(ctx, `
SELECT version, first_served_at FROM app.release_first_served WHERE version = ANY($1::text[])
`, versions)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v string
		var t time.Time
		if err := rows.Scan(&v, &t); err != nil {
			return nil, mapErr(err)
		}
		out[v] = t.UTC()
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}
