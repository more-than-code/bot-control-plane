package db

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Problems (api-v1 v1.41, migration 0027).

const problemCols = `id::text, kind, subject, org_id::text, project_id::text, host_id::text, entry_id::text,
       level, title, detail, opened_at, updated_at, resolved_at`

func (d *DB) OpenProblem(ctx context.Context, in store.ProblemOpen, at time.Time) (store.Problem, error) {
	if !store.ValidProblemOpen(in) {
		return store.Problem{}, store.ErrInvalid
	}
	// Most reports repeat the open row word for word: answer it without a
	// transaction or a write. Anything else takes the locked path below.
	same, err := scanProblem(d.pool.QueryRow(ctx, `
SELECT `+problemCols+`
FROM app.problems
WHERE kind = $1 AND subject = $2 AND resolved_at IS NULL AND level = $3 AND title = $4 AND detail = $5
`, in.Kind, in.Subject, in.Level, in.Title, in.Detail))
	if err == nil {
		return same, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.Problem{}, err
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return store.Problem{}, mapErr(err)
	}
	defer tx.Rollback(ctx)
	// One detector at a time per problem: the open row, the reopen and the
	// insert below then never race (the unique index stays a backstop).
	if err := exec(ctx, tx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "problem:"+in.Kind+":"+in.Subject); err != nil {
		return store.Problem{}, mapErr(err)
	}
	p, err := scanProblem(tx.QueryRow(ctx, `
UPDATE app.problems SET level = $3, title = $4, detail = $5, updated_at = $6
WHERE kind = $1 AND subject = $2 AND resolved_at IS NULL
RETURNING `+problemCols, in.Kind, in.Subject, in.Level, in.Title, in.Detail, at))
	if errors.Is(err, store.ErrNotFound) {
		p, err = scanProblem(tx.QueryRow(ctx, `
UPDATE app.problems SET level = $3, title = $4, detail = $5, updated_at = $6, resolved_at = NULL
WHERE id = (
    SELECT id FROM app.problems
    WHERE kind = $1 AND subject = $2 AND resolved_at >= $7
    ORDER BY resolved_at DESC, id DESC
    LIMIT 1
)
RETURNING `+problemCols, in.Kind, in.Subject, in.Level, in.Title, in.Detail, at, at.Add(-store.ProblemReopenWindow)))
	}
	if errors.Is(err, store.ErrNotFound) {
		p, err = scanProblem(tx.QueryRow(ctx, `
INSERT INTO app.problems (id, kind, subject, org_id, project_id, host_id, entry_id, level, title, detail, opened_at, updated_at)
VALUES ($1::uuid, $2, $3, $4::uuid, $5::uuid, $6::uuid, $7::uuid, $8, $9, $10, $11, $11)
RETURNING `+problemCols, uuid.NewString(), in.Kind, in.Subject, in.OrgID.String(), optID(in.ProjectID), optID(in.HostID), optID(in.EntryID),
			in.Level, in.Title, in.Detail, at))
	}
	if err != nil {
		return store.Problem{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Problem{}, mapErr(err)
	}
	return p, nil
}

func (d *DB) ResolveProblem(ctx context.Context, kind, subject, title, detail string, at time.Time) (bool, error) {
	tag, err := d.pool.Exec(ctx, `
UPDATE app.problems
SET resolved_at = $3, updated_at = $3,
    title = COALESCE(NULLIF($4, ''), title), detail = COALESCE(NULLIF($5, ''), detail)
WHERE kind = $1 AND subject = $2 AND resolved_at IS NULL
`, kind, subject, at, title, detail)
	if err != nil {
		return false, mapErr(err)
	}
	return tag.RowsAffected() > 0, nil
}

func (d *DB) ListProblems(ctx context.Context, f store.ProblemFilter) ([]store.Problem, error) {
	orgs := make([]string, len(f.OrgIDs))
	for i, id := range f.OrgIDs {
		orgs[i] = id.String()
	}
	projects := make([]string, len(f.ProjectIDs))
	for i, id := range f.ProjectIDs {
		projects[i] = id.String()
	}
	// A null $5 keeps no resolved row: resolved_at >= NULL is not true.
	rows, err := d.pool.Query(ctx, `
SELECT `+problemCols+`
FROM app.problems
WHERE ($1 OR org_id = ANY($2::uuid[]) OR project_id = ANY($3::uuid[]))
  AND ($4 = '' OR kind = $4)
  AND (resolved_at IS NULL OR resolved_at >= $5)
ORDER BY COALESCE(resolved_at, updated_at) DESC, id ASC
`, f.All, orgs, projects, f.Kind, optTime(f.ResolvedSince))
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.Problem{}
	for rows.Next() {
		p, err := scanProblem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, mapErr(rows.Err())
}

func (d *DB) PruneProblems(ctx context.Context, before time.Time) (int64, error) {
	tag, err := d.pool.Exec(ctx, `DELETE FROM app.problems WHERE resolved_at < $1`, before)
	if err != nil {
		return 0, mapErr(err)
	}
	return tag.RowsAffected(), nil
}

func (d *DB) ListProblemHosts(ctx context.Context) ([]store.ProblemHost, error) {
	rows, err := d.pool.Query(ctx, `
SELECT id::text, org_id::text, name, always_on, last_seen_at
FROM app.hosts
WHERE revoked_at IS NULL
ORDER BY id
`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.ProblemHost{}
	for rows.Next() {
		var id, org string
		var h store.ProblemHost
		if err := rows.Scan(&id, &org, &h.Name, &h.AlwaysOn, &h.LastSeenAt); err != nil {
			return nil, mapErr(err)
		}
		h.ID, h.OrgID = uuid.MustParse(id), uuid.MustParse(org)
		out = append(out, h)
	}
	return out, mapErr(rows.Err())
}

func (d *DB) ListRegistryLogins(ctx context.Context) ([]store.RegistryLogin, error) {
	rows, err := d.pool.Query(ctx, `
SELECT r.project_id::text, p.org_id::text, r.host
FROM app.registries AS r
JOIN app.projects AS p ON p.id = r.project_id AND p.deleted_at IS NULL
ORDER BY r.project_id, r.host
`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.RegistryLogin{}
	for rows.Next() {
		var pid, org string
		var l store.RegistryLogin
		if err := rows.Scan(&pid, &org, &l.Host); err != nil {
			return nil, mapErr(err)
		}
		l.ProjectID, l.OrgID = uuid.MustParse(pid), uuid.MustParse(org)
		out = append(out, l)
	}
	return out, mapErr(rows.Err())
}

func scanProblem(row pgx.Row) (store.Problem, error) {
	var p store.Problem
	var id, org string
	var project, host, entry *string
	if err := row.Scan(&id, &p.Kind, &p.Subject, &org, &project, &host, &entry,
		&p.Level, &p.Title, &p.Detail, &p.OpenedAt, &p.UpdatedAt, &p.ResolvedAt); err != nil {
		return store.Problem{}, mapErr(err)
	}
	p.ID, p.OrgID = uuid.MustParse(id), uuid.MustParse(org)
	for _, f := range []struct {
		dst **uuid.UUID
		src *string
	}{{&p.ProjectID, project}, {&p.HostID, host}, {&p.EntryID, entry}} {
		v, err := parseOptID(f.src)
		if err != nil {
			return store.Problem{}, err
		}
		*f.dst = v
	}
	p.OpenedAt, p.UpdatedAt = p.OpenedAt.UTC(), p.UpdatedAt.UTC()
	if p.ResolvedAt != nil {
		t := p.ResolvedAt.UTC()
		p.ResolvedAt = &t
	}
	return p, nil
}

// RetentionJobs reads TimescaleDB's own views (api-v1 v1.58). A job with no
// stats row yet has zero runs and no times. -infinity (never) and other
// non-finite times read as nil.
func (d *DB) RetentionJobs(ctx context.Context) ([]store.RetentionJob, error) {
	rows, err := d.pool.Query(ctx, `
SELECT j.job_id, j.hypertable_name, coalesce(s.last_run_status, ''),
       s.last_successful_finish, coalesce(s.total_runs, 0), coalesce(s.total_failures, 0), s.next_start
FROM timescaledb_information.jobs j
LEFT JOIN timescaledb_information.job_stats s ON s.job_id = j.job_id
WHERE j.proc_name = 'policy_retention' AND j.hypertable_schema = 'metrics'
ORDER BY j.hypertable_name, j.job_id
`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.RetentionJob{}
	for rows.Next() {
		var j store.RetentionJob
		var lastOK, next pgtype.Timestamptz
		if err := rows.Scan(&j.JobID, &j.Hypertable, &j.LastStatus, &lastOK, &j.TotalRuns, &j.TotalFailures, &next); err != nil {
			return nil, mapErr(err)
		}
		j.LastSuccess, j.NextStart = finiteTime(lastOK), finiteTime(next)
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

func finiteTime(t pgtype.Timestamptz) *time.Time {
	if !t.Valid || t.InfinityModifier != pgtype.Finite {
		return nil
	}
	u := t.Time.UTC()
	return &u
}
