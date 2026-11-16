package db

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// requestTable is the SQL that the request types share: the per-user create
// lock, the 7-day cleanup, replace-on-create and the open count. Every field
// is a constant below, never input, so building SQL from them is safe.
type requestTable struct {
	// name is the table, schema-qualified.
	name string
	// lockPrefix keys the per-user advisory lock of creates.
	lockPrefix string
	// subject is the columns that, with user and project, name one open request.
	subject []string
}

var (
	valueRequestTable = requestTable{name: "app.env_value_requests", lockPrefix: "env_value_requests:", subject: []string{"set_name", "key"}}
	fileRequestTable  = requestTable{name: "app.file_requests", lockPrefix: "file_requests:", subject: []string{"name"}}
)

// lockCreates makes one user's creates of this type run one at a time, so
// the open count cannot be passed by concurrent calls. An advisory lock
// needs no table privilege.
func (t requestTable) lockCreates(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	return mapErr(exec(ctx, tx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text || $2::text, 0))`, t.lockPrefix, userID.String()))
}

// prune removes rows store.RequestKeep past their expiry. Callers run it
// after their own locks, in the order an answer takes them, and it skips
// rows another transaction holds, so it never waits on an answer.
func (t requestTable) prune(ctx context.Context, tx pgx.Tx, at time.Time) error {
	return mapErr(exec(ctx, tx, `
DELETE FROM `+t.name+` WHERE id_hash IN (
    SELECT id_hash FROM `+t.name+` WHERE expires_at < $1
    FOR UPDATE SKIP LOCKED
)`, at.Add(-store.RequestKeep)))
}

// replaceOpen expires userID's pending, unexpired request for the same
// project and subject at at (one open request per user and subject), so
// openCount no longer counts it. A refused create rolls this back too.
// subject holds one value per t.subject column, in order.
func (t requestTable) replaceOpen(ctx context.Context, tx pgx.Tx, userID, projectID uuid.UUID, at time.Time, subject ...any) error {
	where := ""
	args := []any{userID.String(), projectID.String(), at}
	for i, col := range t.subject {
		args = append(args, subject[i])
		where += " AND " + col + " = $" + strconv.Itoa(len(args))
	}
	return mapErr(exec(ctx, tx, `
UPDATE `+t.name+` SET expires_at = $3
WHERE user_id = $1::uuid AND project_id = $2::uuid`+where+`
  AND state = 'pending' AND expires_at > $3`, args...))
}

// openCount is userID's pending requests of this type unexpired at at.
func (t requestTable) openCount(ctx context.Context, tx pgx.Tx, userID uuid.UUID, at time.Time) (int, error) {
	var open int
	err := tx.QueryRow(ctx, `
SELECT count(*) FROM `+t.name+`
WHERE user_id = $1::uuid AND state = 'pending' AND expires_at > $2`, userID.String(), at).Scan(&open)
	return open, mapErr(err)
}

// openSlot runs the shared create steps after the caller's locks and
// checks: prune, replace, then the open limit (tooMany when reached).
func (t requestTable) openSlot(ctx context.Context, tx pgx.Tx, in store.RequestMeta, tooMany error, subject ...any) error {
	if err := t.prune(ctx, tx, in.CreatedAt); err != nil {
		return err
	}
	if err := t.replaceOpen(ctx, tx, in.UserID, in.ProjectID, in.CreatedAt, subject...); err != nil {
		return err
	}
	open, err := t.openCount(ctx, tx, in.UserID, in.CreatedAt)
	if err != nil {
		return err
	}
	if open >= store.MaxOpenRequests {
		return tooMany
	}
	return nil
}
