package db

import (
	"context"
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
