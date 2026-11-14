package db

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// HostFacts is the caller's role in the host's org and their membership
// in every live project with access to the host (api-v1 v1.24 item 2).
func (d *DB) HostFacts(ctx context.Context, userID, hostID uuid.UUID) (store.HostFacts, error) {
	var facts store.HostFacts
	var orgText string
	err := d.pool.QueryRow(ctx, `
SELECT h.org_id::text,
       COALESCE((
           SELECT m.role FROM app.org_members m
           JOIN app.orgs o ON o.id = m.org_id AND o.deleted_at IS NULL
           WHERE m.org_id = h.org_id AND m.user_id = $1::uuid
       ), '')
FROM app.hosts h
WHERE h.id = $2::uuid
`, userID.String(), hostID.String()).Scan(&orgText, &facts.OrgRole)
	if err != nil {
		return store.HostFacts{}, mapErr(err)
	}
	if facts.OrgID, err = parseID(orgText); err != nil {
		return store.HostFacts{}, err
	}
	rows, err := d.pool.Query(ctx, `
SELECT a.project_id::text, a.level, m.role
FROM app.host_access a
JOIN app.projects p ON p.id = a.project_id AND p.deleted_at IS NULL
JOIN app.project_members m ON m.project_id = a.project_id AND m.user_id = $1::uuid
WHERE a.host_id = $2::uuid
ORDER BY a.project_id
`, userID.String(), hostID.String())
	if err != nil {
		return store.HostFacts{}, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var projectText string
		var a store.MemberAccess
		if err := rows.Scan(&projectText, &a.Level, &a.Role); err != nil {
			return store.HostFacts{}, mapErr(err)
		}
		if a.ProjectID, err = parseID(projectText); err != nil {
			return store.HostFacts{}, err
		}
		facts.Access = append(facts.Access, a)
	}
	if err := rows.Err(); err != nil {
		return store.HostFacts{}, mapErr(err)
	}
	return facts, nil
}

const accessCols = `a.host_id::text, a.project_id::text, p.name, a.level, a.granted_by::text, a.granted_at`

func (d *DB) ListHostAccess(ctx context.Context, hostID uuid.UUID) ([]store.HostAccess, error) {
	var exists bool
	if err := d.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.hosts WHERE id = $1::uuid)`, hostID.String()).Scan(&exists); err != nil {
		return nil, mapErr(err)
	}
	if !exists {
		return nil, store.ErrNotFound
	}
	rows, err := d.pool.Query(ctx, `
SELECT `+accessCols+`
FROM app.host_access a
JOIN app.projects p ON p.id = a.project_id
WHERE a.host_id = $1::uuid
ORDER BY p.name COLLATE "C", a.project_id
`, hostID.String())
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.HostAccess{}
	for rows.Next() {
		item, err := scanAccess(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

func (d *DB) PutHostAccess(ctx context.Context, hostID, projectID uuid.UUID, level string, grantedBy uuid.UUID, at time.Time) (store.HostAccess, bool, error) {
	if !store.ValidAccessLevel(level) {
		return store.HostAccess{}, false, store.ErrInvalid
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return store.HostAccess{}, false, mapErr(err)
	}
	defer tx.Rollback(ctx)
	var ok bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM app.hosts h
    JOIN app.projects p ON p.org_id = h.org_id AND p.id = $2::uuid AND p.deleted_at IS NULL
    WHERE h.id = $1::uuid
)
`, hostID.String(), projectID.String()).Scan(&ok); err != nil {
		return store.HostAccess{}, false, mapErr(err)
	}
	if !ok {
		return store.HostAccess{}, false, store.ErrNotFound
	}
	var inserted bool
	if err := tx.QueryRow(ctx, `
INSERT INTO app.host_access (host_id, project_id, level, granted_by, granted_at)
VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5)
ON CONFLICT (host_id, project_id) DO UPDATE SET level = EXCLUDED.level
RETURNING (xmax = 0)
`, hostID.String(), projectID.String(), level, grantedBy.String(), at).Scan(&inserted); err != nil {
		return store.HostAccess{}, false, mapErr(err)
	}
	item, err := scanAccess(tx.QueryRow(ctx, `
SELECT `+accessCols+`
FROM app.host_access a
JOIN app.projects p ON p.id = a.project_id
WHERE a.host_id = $1::uuid AND a.project_id = $2::uuid
`, hostID.String(), projectID.String()))
	if err != nil {
		return store.HostAccess{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.HostAccess{}, false, mapErr(err)
	}
	return item, inserted, nil
}

func (d *DB) DeleteHostAccess(ctx context.Context, hostID, projectID uuid.UUID, reassignOrg bool) (store.HostAccess, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return store.HostAccess{}, mapErr(err)
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.hosts WHERE id = $1::uuid)`, hostID.String()).Scan(&exists); err != nil {
		return store.HostAccess{}, mapErr(err)
	}
	if !exists {
		return store.HostAccess{}, store.ErrNotFound
	}
	item, err := scanAccess(tx.QueryRow(ctx, `
SELECT `+accessCols+`
FROM app.host_access a
JOIN app.projects p ON p.id = a.project_id
WHERE a.host_id = $1::uuid AND a.project_id = $2::uuid
FOR UPDATE OF a
`, hostID.String(), projectID.String()))
	if err != nil {
		return store.HostAccess{}, err
	}
	var n int
	if err := tx.QueryRow(ctx, `
SELECT count(*) FROM app.services
WHERE host_id = $1::uuid AND project_id = $2::uuid AND gone_at IS NULL
`, hostID.String(), projectID.String()).Scan(&n); err != nil {
		return store.HostAccess{}, mapErr(err)
	}
	if n > 0 && !reassignOrg {
		return store.HostAccess{}, store.ErrConflict
	}
	if n > 0 {
		if err := exec(ctx, tx, `
UPDATE app.services
SET project_id = NULL, catalog_entry = NULL,
    source = CASE WHEN source = 'owned' THEN 'discovered' ELSE source END
WHERE host_id = $1::uuid AND project_id = $2::uuid AND gone_at IS NULL
`, hostID.String(), projectID.String()); err != nil {
			return store.HostAccess{}, mapErr(err)
		}
	}
	if err := exec(ctx, tx, `
DELETE FROM app.host_access WHERE host_id = $1::uuid AND project_id = $2::uuid
`, hostID.String(), projectID.String()); err != nil {
		return store.HostAccess{}, mapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return store.HostAccess{}, mapErr(err)
	}
	return item, nil
}

// HostProjectAccess is the project's level on the host, or "" (none, or a
// deleted project). A missing host is ErrNotFound.
func (d *DB) HostProjectAccess(ctx context.Context, projectID, hostID uuid.UUID) (string, error) {
	var level *string
	err := d.pool.QueryRow(ctx, `
SELECT (
    SELECT a.level FROM app.host_access a
    JOIN app.projects p ON p.id = a.project_id AND p.deleted_at IS NULL
    WHERE a.host_id = h.id AND a.project_id = $1::uuid
)
FROM app.hosts h
WHERE h.id = $2::uuid
`, projectID.String(), hostID.String()).Scan(&level)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", store.ErrNotFound
	}
	if err != nil {
		return "", mapErr(err)
	}
	if level == nil {
		return "", nil
	}
	return *level, nil
}

func scanAccess(row pgx.Row) (store.HostAccess, error) {
	var item store.HostAccess
	var hostText, projectText string
	var grantedBy *string
	if err := row.Scan(&hostText, &projectText, &item.ProjectName, &item.Level, &grantedBy, &item.GrantedAt); err != nil {
		return store.HostAccess{}, mapErr(err)
	}
	var err error
	if item.HostID, err = parseID(hostText); err != nil {
		return store.HostAccess{}, err
	}
	if item.ProjectID, err = parseID(projectText); err != nil {
		return store.HostAccess{}, err
	}
	if item.GrantedBy, err = parseOptID(grantedBy); err != nil {
		return store.HostAccess{}, err
	}
	return item, nil
}
