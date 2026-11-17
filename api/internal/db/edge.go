package db

import (
	"context"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Domains, routes and host edges (api-v1 v1.35, migration 0023).

const domainCols = `d.id::text, d.org_id::text, d.name, d.token, d.status, d.verified_at,
       d.last_check_at, d.failures, d.created_by::text, d.created_at`

func scanDomain(row pgx.Row) (store.OrgDomain, error) {
	var d store.OrgDomain
	var id, org, by string
	if err := row.Scan(&id, &org, &d.Name, &d.Token, &d.Status, &d.VerifiedAt,
		&d.LastCheckAt, &d.Failures, &by, &d.CreatedAt); err != nil {
		return store.OrgDomain{}, mapErr(err)
	}
	var err error
	if d.ID, err = parseID(id); err != nil {
		return store.OrgDomain{}, err
	}
	if d.OrgID, err = parseID(org); err != nil {
		return store.OrgDomain{}, err
	}
	if d.CreatedBy, err = parseID(by); err != nil {
		return store.OrgDomain{}, err
	}
	d.Assignments = []store.DomainAssignment{}
	return d, nil
}

func collectDomains(ctx context.Context, q queryer, sql string, args ...any) ([]store.OrgDomain, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.OrgDomain{}
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}

// withAssignments fills Assignments for each domain (live projects only).
// With projectID set it keeps only that project's rows.
func withAssignments(ctx context.Context, q queryer, domains []store.OrgDomain, projectID *uuid.UUID) error {
	if len(domains) == 0 {
		return nil
	}
	ids := make([]string, len(domains))
	index := map[string]int{}
	for i, d := range domains {
		ids[i] = d.ID.String()
		index[d.ID.String()] = i
	}
	var project any
	if projectID != nil {
		project = projectID.String()
	}
	rows, err := q.Query(ctx, `
SELECT a.domain_id::text, a.project_id::text, p.name, a.pattern
FROM app.domain_assignments a JOIN app.projects p ON p.id = a.project_id AND p.deleted_at IS NULL
WHERE a.domain_id = ANY($1::uuid[]) AND ($2::uuid IS NULL OR a.project_id = $2::uuid)
ORDER BY p.name, a.project_id::text, a.pattern
`, ids, project)
	if err != nil {
		return mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var domain, pid string
		var a store.DomainAssignment
		if err := rows.Scan(&domain, &pid, &a.ProjectName, &a.Pattern); err != nil {
			return mapErr(err)
		}
		if a.ProjectID, err = parseID(pid); err != nil {
			return err
		}
		i := index[domain]
		domains[i].Assignments = append(domains[i].Assignments, a)
	}
	return mapErr(rows.Err())
}

func (d *DB) ListOrgDomains(ctx context.Context, orgID uuid.UUID) ([]store.OrgDomain, error) {
	out, err := collectDomains(ctx, d.pool, `
SELECT `+domainCols+`
FROM app.org_domains d JOIN app.orgs o ON o.id = d.org_id AND o.deleted_at IS NULL
WHERE d.org_id = $1::uuid
ORDER BY d.name
`, orgID.String())
	if err != nil {
		return nil, err
	}
	return out, withAssignments(ctx, d.pool, out, nil)
}

func getDomain(ctx context.Context, q queryer, orgID, domainID uuid.UUID, lock bool) (store.OrgDomain, error) {
	sql := `
SELECT ` + domainCols + `
FROM app.org_domains d JOIN app.orgs o ON o.id = d.org_id AND o.deleted_at IS NULL
WHERE d.org_id = $1::uuid AND d.id = $2::uuid`
	if lock {
		sql += ` FOR UPDATE OF d`
	}
	dom, err := scanDomain(q.QueryRow(ctx, sql, orgID.String(), domainID.String()))
	if err != nil {
		return store.OrgDomain{}, err
	}
	list := []store.OrgDomain{dom}
	if err := withAssignments(ctx, q, list, nil); err != nil {
		return store.OrgDomain{}, err
	}
	return list[0], nil
}

func (d *DB) GetOrgDomain(ctx context.Context, orgID, domainID uuid.UUID) (store.OrgDomain, error) {
	return getDomain(ctx, d.pool, orgID, domainID, false)
}

func (d *DB) CreateOrgDomain(ctx context.Context, orgID uuid.UUID, name, token string, actor uuid.UUID, at time.Time) (store.OrgDomain, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return store.OrgDomain{}, mapErr(err)
	}
	defer tx.Rollback(ctx)
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.orgs WHERE id = $1::uuid AND deleted_at IS NULL)`, orgID.String()).Scan(&live); err != nil {
		return store.OrgDomain{}, mapErr(err)
	}
	if !live {
		return store.OrgDomain{}, store.ErrNotFound
	}
	var held bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (SELECT 1 FROM app.org_domains WHERE name = $1 AND status IN ('verified', 'lapsed'))
`, name).Scan(&held); err != nil {
		return store.OrgDomain{}, mapErr(err)
	}
	if held {
		var mine bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.org_domains WHERE name = $1 AND org_id = $2::uuid)`, name, orgID.String()).Scan(&mine); err != nil {
			return store.OrgDomain{}, mapErr(err)
		}
		if mine {
			return store.OrgDomain{}, store.ErrConflict
		}
		return store.OrgDomain{}, store.ErrDomainTaken
	}
	dom, err := scanDomain(tx.QueryRow(ctx, `
INSERT INTO app.org_domains AS d (id, org_id, name, token, status, created_by, created_at)
VALUES ($1::uuid, $2::uuid, $3, $4, 'pending', $5::uuid, $6)
RETURNING `+domainCols, uuid.NewString(), orgID.String(), name, token, actor.String(), at))
	if err != nil {
		return store.OrgDomain{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.OrgDomain{}, mapErr(err)
	}
	return dom, nil
}

func (d *DB) MarkDomainVerified(ctx context.Context, orgID, domainID uuid.UUID, at time.Time) (store.OrgDomain, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return store.OrgDomain{}, mapErr(err)
	}
	defer tx.Rollback(ctx)
	dom, err := getDomain(ctx, tx, orgID, domainID, true)
	if err != nil {
		return store.OrgDomain{}, err
	}
	err = exec(ctx, tx, `
UPDATE app.org_domains
SET status = 'verified', failures = 0, last_check_at = $2,
    verified_at = CASE WHEN status = 'verified' THEN verified_at ELSE $2 END
WHERE id = $1::uuid
`, dom.ID.String(), at)
	if isUnique(err) {
		return store.OrgDomain{}, store.ErrDomainTaken
	}
	if err != nil {
		return store.OrgDomain{}, mapErr(err)
	}
	dom, err = getDomain(ctx, tx, orgID, domainID, false)
	if err != nil {
		return store.OrgDomain{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.OrgDomain{}, mapErr(err)
	}
	return dom, nil
}

func (d *DB) RecordDomainCheck(ctx context.Context, domainID uuid.UUID, ok bool, at time.Time) (store.OrgDomain, bool, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return store.OrgDomain{}, false, mapErr(err)
	}
	defer tx.Rollback(ctx)
	dom, err := scanDomain(tx.QueryRow(ctx, `SELECT `+domainCols+` FROM app.org_domains d WHERE d.id = $1::uuid FOR UPDATE`, domainID.String()))
	if err != nil {
		return store.OrgDomain{}, false, err
	}
	if dom.Status == store.DomainPending {
		return store.OrgDomain{}, false, store.ErrState
	}
	lapsed := false
	if ok {
		err = exec(ctx, tx, `
UPDATE app.org_domains
SET status = 'verified', failures = 0, last_check_at = $2,
    verified_at = CASE WHEN status = 'verified' THEN verified_at ELSE $2 END
WHERE id = $1::uuid
`, domainID.String(), at)
	} else {
		lapsed = dom.Status == store.DomainVerified && dom.Failures+1 >= store.DomainLapseAfter
		err = exec(ctx, tx, `
UPDATE app.org_domains
SET failures = failures + 1, last_check_at = $2,
    status = CASE WHEN $3 THEN 'lapsed' ELSE status END
WHERE id = $1::uuid
`, domainID.String(), at, lapsed)
	}
	if err != nil {
		return store.OrgDomain{}, false, mapErr(err)
	}
	dom, err = getDomain(ctx, tx, dom.OrgID, dom.ID, false)
	if err != nil {
		return store.OrgDomain{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.OrgDomain{}, false, mapErr(err)
	}
	return dom, lapsed, nil
}

func (d *DB) ListDomainsDue(ctx context.Context, before time.Time, limit int) ([]store.OrgDomain, error) {
	if limit <= 0 {
		limit = 1000
	}
	out, err := collectDomains(ctx, d.pool, `
SELECT `+domainCols+`
FROM app.org_domains d JOIN app.orgs o ON o.id = d.org_id AND o.deleted_at IS NULL
WHERE d.status IN ('verified', 'lapsed') AND (d.last_check_at IS NULL OR d.last_check_at < $1)
ORDER BY d.last_check_at ASC NULLS FIRST, d.id::text
LIMIT $2
`, before, limit)
	if err != nil {
		return nil, err
	}
	return out, withAssignments(ctx, d.pool, out, nil)
}

func (d *DB) PutDomainAssignments(ctx context.Context, orgID, domainID uuid.UUID, in []store.DomainAssignment) (store.OrgDomain, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return store.OrgDomain{}, mapErr(err)
	}
	defer tx.Rollback(ctx)
	if _, err := getDomain(ctx, tx, orgID, domainID, true); err != nil {
		return store.OrgDomain{}, err
	}
	seen := map[string]bool{}
	for _, a := range in {
		key := a.ProjectID.String() + a.Pattern
		if seen[key] || (a.Pattern != "@" && a.Pattern != "*") {
			return store.OrgDomain{}, store.ErrInvalid
		}
		seen[key] = true
		var ok bool
		if err := tx.QueryRow(ctx, `
SELECT EXISTS (SELECT 1 FROM app.projects WHERE id = $1::uuid AND org_id = $2::uuid AND deleted_at IS NULL)
`, a.ProjectID.String(), orgID.String()).Scan(&ok); err != nil {
			return store.OrgDomain{}, mapErr(err)
		}
		if !ok {
			return store.OrgDomain{}, store.ErrInvalid
		}
	}
	if err := exec(ctx, tx, `DELETE FROM app.domain_assignments WHERE domain_id = $1::uuid`, domainID.String()); err != nil {
		return store.OrgDomain{}, mapErr(err)
	}
	for _, a := range in {
		if err := exec(ctx, tx, `
INSERT INTO app.domain_assignments (domain_id, project_id, pattern) VALUES ($1::uuid, $2::uuid, $3)
`, domainID.String(), a.ProjectID.String(), a.Pattern); err != nil {
			return store.OrgDomain{}, mapErr(err)
		}
	}
	dom, err := getDomain(ctx, tx, orgID, domainID, false)
	if err != nil {
		return store.OrgDomain{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.OrgDomain{}, mapErr(err)
	}
	return dom, nil
}

func (d *DB) ProjectDomains(ctx context.Context, projectID uuid.UUID) ([]store.OrgDomain, error) {
	out, err := collectDomains(ctx, d.pool, `
SELECT `+domainCols+`
FROM app.org_domains d
JOIN app.orgs o ON o.id = d.org_id AND o.deleted_at IS NULL
JOIN app.projects p ON p.org_id = d.org_id AND p.id = $1::uuid AND p.deleted_at IS NULL
WHERE EXISTS (SELECT 1 FROM app.domain_assignments a WHERE a.domain_id = d.id AND a.project_id = $1::uuid)
ORDER BY d.name
`, projectID.String())
	if err != nil {
		return nil, err
	}
	return out, withAssignments(ctx, d.pool, out, &projectID)
}

// lockPlan reads and locks one plan and checks it for a confirm by actor.
func lockPlan(ctx context.Context, tx pgx.Tx, confirm []byte, kind string, actor uuid.UUID, at time.Time) (store.EdgeConfirmation, error) {
	c, err := scanEdgeConfirmation(tx.QueryRow(ctx, edgeConfirmationSelect+` FOR UPDATE`, confirm))
	if err != nil {
		return store.EdgeConfirmation{}, err
	}
	if c.Kind != kind || c.UserID != actor || c.UsedAt != nil || !c.ExpiresAt.After(at) {
		return store.EdgeConfirmation{}, store.ErrNotFound
	}
	return c, nil
}

func (d *DB) DeleteOrgDomain(ctx context.Context, orgID, domainID uuid.UUID, want []string, confirm []byte, actor uuid.UUID, at time.Time) ([]store.Route, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	defer tx.Rollback(ctx)
	c, err := lockPlan(ctx, tx, confirm, store.EdgePlanDomainDelete, actor, at)
	if err != nil {
		return nil, err
	}
	if c.OrgID == nil || *c.OrgID != orgID || c.DomainID == nil || *c.DomainID != domainID {
		return nil, store.ErrNotFound
	}
	if _, err := getDomain(ctx, tx, orgID, domainID, true); err != nil {
		return nil, err
	}
	// No route may be added or removed between this read and the delete,
	// so the set compared with the plan is the set removed. SELECT … FOR
	// UPDATE would need an UPDATE grant on app.routes; this lock mode needs
	// only the DELETE that deploybot_api already holds, and it lets reads
	// through. Domain deletes are rare, so a short table lock is cheap.
	if err := exec(ctx, tx, `LOCK TABLE app.routes IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return nil, mapErr(err)
	}
	removed, err := routesUnder(ctx, tx, orgID, domainID)
	if err != nil {
		return nil, err
	}
	got := store.RouteKeys(removed)
	if len(got) != len(want) {
		return nil, store.ErrRoutesChanged
	}
	for i := range got {
		if got[i] != want[i] {
			return nil, store.ErrRoutesChanged
		}
	}
	ids := make([]string, len(removed))
	for i, r := range removed {
		ids[i] = r.ID.String()
	}
	if err := exec(ctx, tx, `DELETE FROM app.routes WHERE id = ANY($1::uuid[])`, ids); err != nil {
		return nil, mapErr(err)
	}
	if err := exec(ctx, tx, `UPDATE app.edge_confirmations SET used_at = $2 WHERE token_hash = $1`, confirm, at); err != nil {
		return nil, mapErr(err)
	}
	if err := exec(ctx, tx, `DELETE FROM app.org_domains WHERE id = $1::uuid`, domainID.String()); err != nil {
		return nil, mapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, mapErr(err)
	}
	return removed, nil
}

const routeCols = `r.id::text, r.project_id::text, r.entry_id::text, e.name, r.hostname, r.path_prefix, r.port,
       r.client_ip_from, r.no_store_paths, r.created_by::text, r.created_at`

const routeFrom = `
FROM app.routes r
JOIN app.catalog_entries e ON e.id = r.entry_id
JOIN app.projects p ON p.id = r.project_id AND p.deleted_at IS NULL`

func collectRoutes(ctx context.Context, q queryer, sql string, args ...any) ([]store.Route, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.Route{}
	for rows.Next() {
		var r store.Route
		var id, project, entry, by string
		if err := rows.Scan(&id, &project, &entry, &r.Entry, &r.Hostname, &r.PathPrefix, &r.Port,
			&r.ClientIPFrom, &r.NoStorePaths, &by, &r.CreatedAt); err != nil {
			return nil, mapErr(err)
		}
		if r.ID, err = parseID(id); err != nil {
			return nil, err
		}
		if r.ProjectID, err = parseID(project); err != nil {
			return nil, err
		}
		if r.EntryID, err = parseID(entry); err != nil {
			return nil, err
		}
		if r.CreatedBy, err = parseID(by); err != nil {
			return nil, err
		}
		if r.NoStorePaths == nil {
			r.NoStorePaths = []string{}
		}
		out = append(out, r)
	}
	return out, mapErr(rows.Err())
}

// routesUnder is RoutesUnderDomain: routes of the org's live projects
// under the domain, except a hostname another verified or lapsed domain
// assigned to the same project still covers.
func routesUnder(ctx context.Context, q queryer, orgID, domainID uuid.UUID) ([]store.Route, error) {
	sql := `SELECT ` + routeCols + routeFrom + `
JOIN app.org_domains d ON d.id = $2::uuid AND d.org_id = $1::uuid
WHERE p.org_id = $1::uuid
  AND (r.hostname = d.name OR substr(r.hostname, strpos(r.hostname, '.') + 1) = d.name)
  AND NOT EXISTS (
    SELECT 1 FROM app.org_domains d2
    JOIN app.domain_assignments a2 ON a2.domain_id = d2.id AND a2.project_id = r.project_id
    WHERE d2.id <> d.id AND d2.org_id = $1::uuid AND d2.status IN ('verified', 'lapsed')
      AND ((a2.pattern = '@' AND d2.name = r.hostname)
        OR (a2.pattern = '*' AND strpos(r.hostname, '.') > 1 AND substr(r.hostname, strpos(r.hostname, '.') + 1) = d2.name))
  )
ORDER BY r.hostname, r.path_prefix`
	return collectRoutes(ctx, q, sql, orgID.String(), domainID.String())
}

func (d *DB) RoutesUnderDomain(ctx context.Context, orgID, domainID uuid.UUID) ([]store.Route, error) {
	if _, err := getDomain(ctx, d.pool, orgID, domainID, false); err != nil {
		return nil, err
	}
	return routesUnder(ctx, d.pool, orgID, domainID)
}

func (d *DB) ListProjectRoutes(ctx context.Context, projectID uuid.UUID) ([]store.Route, error) {
	return collectRoutes(ctx, d.pool, `SELECT `+routeCols+routeFrom+`
WHERE r.project_id = $1::uuid
ORDER BY e.name, r.hostname, r.path_prefix
`, projectID.String())
}

func (d *DB) ListRoutesByHostname(ctx context.Context, hostnames []string) ([]store.Route, error) {
	if len(hostnames) == 0 {
		return []store.Route{}, nil
	}
	return collectRoutes(ctx, d.pool, `SELECT `+routeCols+routeFrom+`
WHERE r.hostname = ANY($1::text[])
ORDER BY r.hostname, r.path_prefix
`, hostnames)
}

func (d *DB) ReplaceEntryRoutes(ctx context.Context, projectID, entryID uuid.UUID, routes []store.Route, confirm []byte, actor uuid.UUID, at time.Time) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback(ctx)
	c, err := lockPlan(ctx, tx, confirm, store.EdgePlanRoutes, actor, at)
	if err != nil {
		return err
	}
	if c.ProjectID == nil || *c.ProjectID != projectID || c.EntryID == nil || *c.EntryID != entryID {
		return store.ErrNotFound
	}
	var live bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM app.catalog_entries e JOIN app.projects p ON p.id = e.project_id AND p.deleted_at IS NULL
    WHERE e.id = $1::uuid AND e.project_id = $2::uuid
)`, entryID.String(), projectID.String()).Scan(&live); err != nil {
		return mapErr(err)
	}
	if !live {
		return store.ErrNotFound
	}
	if err := exec(ctx, tx, `DELETE FROM app.routes WHERE entry_id = $1::uuid`, entryID.String()); err != nil {
		return mapErr(err)
	}
	for _, r := range routes {
		paths := r.NoStorePaths
		if paths == nil {
			paths = []string{}
		}
		if err := exec(ctx, tx, `
INSERT INTO app.routes (id, project_id, entry_id, hostname, path_prefix, port, client_ip_from, no_store_paths, created_by, created_at)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8::text[], $9::uuid, $10)
`, uuid.NewString(), projectID.String(), entryID.String(), r.Hostname, r.PathPrefix, r.Port,
			r.ClientIPFrom, paths, actor.String(), at); err != nil {
			return mapErr(err)
		}
	}
	if err := exec(ctx, tx, `UPDATE app.edge_confirmations SET used_at = $2 WHERE token_hash = $1`, confirm, at); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit(ctx))
}

const hostEdgeCols = `host_id::text, enabled, status, detail, config_hash, applied_at, updated_at`

func scanHostEdge(row pgx.Row) (store.HostEdge, error) {
	var e store.HostEdge
	var id string
	if err := row.Scan(&id, &e.Enabled, &e.Status, &e.Detail, &e.ConfigHash, &e.AppliedAt, &e.UpdatedAt); err != nil {
		return store.HostEdge{}, mapErr(err)
	}
	var err error
	e.HostID, err = parseID(id)
	return e, err
}

func (d *DB) GetHostEdge(ctx context.Context, hostID uuid.UUID) (store.HostEdge, error) {
	e, err := scanHostEdge(d.pool.QueryRow(ctx, `SELECT `+hostEdgeCols+` FROM app.host_edges WHERE host_id = $1::uuid`, hostID.String()))
	if errors.Is(err, store.ErrNotFound) {
		var exists bool
		if err := d.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.hosts WHERE id = $1::uuid)`, hostID.String()).Scan(&exists); err != nil {
			return store.HostEdge{}, mapErr(err)
		}
		if !exists {
			return store.HostEdge{}, store.ErrNotFound
		}
		return store.HostEdge{HostID: hostID, Status: store.EdgeOff}, nil
	}
	return e, err
}

func (d *DB) SetHostEdgeEnabled(ctx context.Context, hostID uuid.UUID, enabled bool, at time.Time) (store.HostEdge, error) {
	return scanHostEdge(d.pool.QueryRow(ctx, `
INSERT INTO app.host_edges AS e (host_id, enabled, status, updated_at)
VALUES ($1::uuid, $2, CASE WHEN $2 THEN 'pending' ELSE 'off' END, $3)
ON CONFLICT (host_id) DO UPDATE SET
    enabled = EXCLUDED.enabled,
    status = CASE WHEN EXCLUDED.enabled OR e.status <> 'off' THEN 'pending' ELSE e.status END,
    detail = CASE WHEN EXCLUDED.enabled OR e.status <> 'off' THEN '' ELSE e.detail END,
    updated_at = EXCLUDED.updated_at
RETURNING `+hostEdgeCols, hostID.String(), enabled, at))
}

func (d *DB) SetHostEdgeState(ctx context.Context, e store.HostEdge) error {
	return mapErr(exec(ctx, d.pool, `
INSERT INTO app.host_edges (host_id, enabled, status, detail, config_hash, applied_at, updated_at)
VALUES ($1::uuid, false, $2, $3, $4, $5, $6)
ON CONFLICT (host_id) DO UPDATE SET
    status = EXCLUDED.status, detail = EXCLUDED.detail, config_hash = EXCLUDED.config_hash,
    applied_at = EXCLUDED.applied_at, updated_at = EXCLUDED.updated_at
`, e.HostID.String(), e.Status, e.Detail, e.ConfigHash, e.AppliedAt, e.UpdatedAt))
}

func (d *DB) ListOrgEdges(ctx context.Context, orgID uuid.UUID) ([]store.HostEdge, error) {
	rows, err := d.pool.Query(ctx, `
SELECT `+hostEdgeCols+`
FROM app.host_edges e JOIN app.hosts h ON h.id = e.host_id
WHERE h.org_id = $1::uuid AND h.revoked_at IS NULL AND (e.enabled OR e.status <> 'off')
ORDER BY e.host_id::text
`, orgID.String())
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.HostEdge{}
	for rows.Next() {
		e, err := scanHostEdge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, mapErr(rows.Err())
}

const liveOwnedFrom = `
FROM app.services s
JOIN app.hosts h ON h.id = s.host_id AND h.revoked_at IS NULL
JOIN app.projects p ON p.id = s.project_id AND p.deleted_at IS NULL
WHERE s.gone_at IS NULL AND s.catalog_entry IS NOT NULL`

func (d *DB) EntryHosts(ctx context.Context, projectID uuid.UUID) (map[string][]uuid.UUID, error) {
	rows, err := d.pool.Query(ctx, `
SELECT DISTINCT s.catalog_entry, s.host_id::text`+liveOwnedFrom+` AND s.project_id = $1::uuid
ORDER BY 1, 2
`, projectID.String())
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string][]uuid.UUID{}
	for rows.Next() {
		var entry, host string
		if err := rows.Scan(&entry, &host); err != nil {
			return nil, mapErr(err)
		}
		id, err := parseID(host)
		if err != nil {
			return nil, err
		}
		out[entry] = append(out[entry], id)
	}
	return out, mapErr(rows.Err())
}

func (d *DB) HostEntries(ctx context.Context, hostID uuid.UUID) ([]store.HostEntry, error) {
	rows, err := d.pool.Query(ctx, `
SELECT s.project_id::text, s.catalog_entry, s.ports`+liveOwnedFrom+` AND s.host_id = $1::uuid
ORDER BY 1, 2
`, hostID.String())
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.HostEntry{}
	for rows.Next() {
		var project, entry string
		var ports []int32
		if err := rows.Scan(&project, &entry, &ports); err != nil {
			return nil, mapErr(err)
		}
		pid, err := parseID(project)
		if err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].ProjectID != pid || out[n-1].Entry != entry {
			out = append(out, store.HostEntry{ProjectID: pid, Entry: entry, Ports: []int{}})
		}
		last := &out[len(out)-1]
		for _, p := range ports {
			last.Ports = append(last.Ports, int(p))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}
	for i := range out {
		sort.Ints(out[i].Ports)
		out[i].Ports = slices.Compact(out[i].Ports)
	}
	return out, nil
}

const edgeConfirmationSelect = `
SELECT user_id::text, kind, project_id::text, entry_id::text, org_id::text, domain_id::text,
       body, request_hash, idempotency_key, created_at, expires_at, used_at
FROM app.edge_confirmations
WHERE token_hash = $1`

func scanEdgeConfirmation(row pgx.Row) (store.EdgeConfirmation, error) {
	var c store.EdgeConfirmation
	var user string
	var project, entry, org, domain *string
	if err := row.Scan(&user, &c.Kind, &project, &entry, &org, &domain, &c.Body, &c.RequestHash,
		&c.IdempotencyKey, &c.CreatedAt, &c.ExpiresAt, &c.UsedAt); err != nil {
		return store.EdgeConfirmation{}, mapErr(err)
	}
	var err error
	if c.UserID, err = parseID(user); err != nil {
		return store.EdgeConfirmation{}, err
	}
	for _, f := range []struct {
		raw *string
		dst **uuid.UUID
	}{{project, &c.ProjectID}, {entry, &c.EntryID}, {org, &c.OrgID}, {domain, &c.DomainID}} {
		if *f.dst, err = parseOptID(f.raw); err != nil {
			return store.EdgeConfirmation{}, err
		}
	}
	return c, nil
}

func (d *DB) PutEdgeConfirmation(ctx context.Context, hash []byte, c store.EdgeConfirmation) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback(ctx)
	// Rows expired more than store.RequestKeep ago go (spec-release-v0239 F4).
	if err := exec(ctx, tx, `DELETE FROM app.edge_confirmations WHERE expires_at < $1`, c.CreatedAt.Add(-store.RequestKeep)); err != nil {
		return mapErr(err)
	}
	if err := exec(ctx, tx, `
INSERT INTO app.edge_confirmations (
    token_hash, user_id, kind, project_id, entry_id, org_id, domain_id, body,
    request_hash, idempotency_key, created_at, expires_at
) VALUES ($1, $2::uuid, $3, $4::uuid, $5::uuid, $6::uuid, $7::uuid, $8::jsonb, $9, $10, $11, $12)
`, hash, c.UserID.String(), c.Kind, optUUID(c.ProjectID), optUUID(c.EntryID), optUUID(c.OrgID), optUUID(c.DomainID),
		string(c.Body), c.RequestHash, c.IdempotencyKey, c.CreatedAt, c.ExpiresAt); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit(ctx))
}

func (d *DB) GetEdgeConfirmation(ctx context.Context, hash []byte) (store.EdgeConfirmation, error) {
	return scanEdgeConfirmation(d.pool.QueryRow(ctx, edgeConfirmationSelect, hash))
}

func (d *DB) OrgACMEEmail(ctx context.Context, orgID uuid.UUID) (string, error) {
	var email *string
	if err := d.pool.QueryRow(ctx, `SELECT acme_email FROM app.orgs WHERE id = $1::uuid AND deleted_at IS NULL`, orgID.String()).Scan(&email); err != nil {
		return "", mapErr(err)
	}
	if email == nil {
		return "", nil
	}
	return *email, nil
}

func (d *DB) SetOrgACMEEmail(ctx context.Context, orgID uuid.UUID, email string) error {
	var value any
	if email != "" {
		value = email
	}
	tag, err := d.pool.Exec(ctx, `UPDATE app.orgs SET acme_email = $2 WHERE id = $1::uuid AND deleted_at IS NULL`, orgID.String(), value)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}
