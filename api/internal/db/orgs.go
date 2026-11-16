package db

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// createOrgTx inserts an org with ownerID as its owner. The slug is
// baseSlug, made unique with -2 style suffixes.
func createOrgTx(ctx context.Context, tx pgx.Tx, ownerID uuid.UUID, name, baseSlug string, at time.Time) (store.Org, error) {
	var takenErr error
	slug, err := store.Unique(baseSlug, store.MaxOrgSlug, func(candidate string) bool {
		if takenErr != nil {
			return true
		}
		var exists bool
		if qerr := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.orgs WHERE slug = $1)`, candidate).Scan(&exists); qerr != nil {
			takenErr = qerr
			return true
		}
		return exists
	})
	if takenErr != nil {
		return store.Org{}, takenErr
	}
	if err != nil {
		return store.Org{}, err
	}
	o := store.Org{ID: uuid.New(), Name: name, Slug: slug, Role: store.OrgRoleOwner, CreatedBy: ownerID, CreatedAt: at}
	if err := exec(ctx, tx, `
INSERT INTO app.orgs (id, name, slug, created_by, created_at)
VALUES ($1::uuid, $2, $3, $4::uuid, $5)
`, o.ID.String(), name, slug, ownerID.String(), at); err != nil {
		return store.Org{}, err
	}
	if err := exec(ctx, tx, `
INSERT INTO app.org_members (org_id, user_id, role, added_by, added_at)
VALUES ($1::uuid, $2::uuid, 'owner', $2::uuid, $3)
`, o.ID.String(), ownerID.String(), at); err != nil {
		return store.Org{}, err
	}
	return o, nil
}

func (d *DB) CreateOrg(ctx context.Context, ownerID uuid.UUID, name, baseSlug string, at time.Time) (store.Org, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return store.Org{}, mapErr(err)
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.users WHERE id = $1::uuid)`, ownerID.String()).Scan(&exists); err != nil {
		return store.Org{}, mapErr(err)
	}
	if !exists {
		return store.Org{}, store.ErrNotFound
	}
	o, err := createOrgTx(ctx, tx, ownerID, name, baseSlug, at)
	if err != nil {
		return store.Org{}, mapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Org{}, mapErr(err)
	}
	return o, nil
}

func scanOrg(row pgx.Row) (store.Org, error) {
	var o store.Org
	var id, createdBy string
	if err := row.Scan(&id, &o.Name, &o.Slug, &o.Role, &createdBy, &o.CreatedAt); err != nil {
		return store.Org{}, mapErr(err)
	}
	var err error
	if o.ID, err = parseID(id); err != nil {
		return store.Org{}, err
	}
	if o.CreatedBy, err = parseID(createdBy); err != nil {
		return store.Org{}, err
	}
	return o, nil
}

func (d *DB) GetOrg(ctx context.Context, id uuid.UUID) (store.Org, error) {
	return scanOrg(d.pool.QueryRow(ctx, `
SELECT id::text, name, slug, ''::text, created_by::text, created_at
FROM app.orgs WHERE id = $1::uuid AND deleted_at IS NULL
`, id.String()))
}

func (d *DB) OrgRole(ctx context.Context, orgID, userID uuid.UUID) (string, error) {
	var role string
	err := d.pool.QueryRow(ctx, `
SELECT m.role FROM app.org_members m
JOIN app.orgs o ON o.id = m.org_id AND o.deleted_at IS NULL
WHERE m.org_id = $1::uuid AND m.user_id = $2::uuid
`, orgID.String(), userID.String()).Scan(&role)
	if err != nil {
		return "", mapErr(err)
	}
	return role, nil
}

func (d *DB) ListUserOrgs(ctx context.Context, userID uuid.UUID) ([]store.Org, error) {
	rows, err := d.pool.Query(ctx, `
SELECT o.id::text, o.name, o.slug, m.role, o.created_by::text, o.created_at
FROM app.org_members m
JOIN app.orgs o ON o.id = m.org_id AND o.deleted_at IS NULL
WHERE m.user_id = $1::uuid
ORDER BY o.created_at, o.id
`, userID.String())
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []store.Org{}
	for rows.Next() {
		o, err := scanOrg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, mapErr(rows.Err())
}

func (d *DB) RenameOrg(ctx context.Context, id uuid.UUID, name string) (store.Org, error) {
	return scanOrg(d.pool.QueryRow(ctx, `
UPDATE app.orgs SET name = $2
WHERE id = $1::uuid AND deleted_at IS NULL
RETURNING id::text, name, slug, ''::text, created_by::text, created_at
`, id.String(), name))
}

// DeleteOrg soft-deletes the org and its projects and revokes every host
// credential of the org, in one transaction.
func (d *DB) DeleteOrg(ctx context.Context, id uuid.UUID, at time.Time) ([]uuid.UUID, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE app.orgs SET deleted_at = $2 WHERE id = $1::uuid AND deleted_at IS NULL`, id.String(), at)
	if err != nil {
		return nil, mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return nil, store.ErrNotFound
	}
	// api-v1 v1.35: a deleted org gives up its domains (and the verified
	// names they hold). Assignments cascade.
	if err := exec(ctx, tx, `DELETE FROM app.org_domains WHERE org_id = $1::uuid`, id.String()); err != nil {
		return nil, mapErr(err)
	}
	projects, err := collectIDs(ctx, tx, `SELECT id::text FROM app.projects WHERE org_id = $1::uuid AND deleted_at IS NULL ORDER BY id::text`, id.String())
	if err != nil {
		return nil, err
	}
	for _, pid := range projects {
		if err := deleteProjectTx(ctx, tx, pid, at); err != nil {
			return nil, err
		}
	}
	// Every statement takes ($1 org, $2 time); the services one does not use
	// $2 (gone_at is the host's revoke time), so it runs with only $1.
	for _, q := range []string{
		`UPDATE app.hosts SET revoked_at = $2 WHERE org_id = $1::uuid AND revoked_at IS NULL`,
		`UPDATE app.agent_credentials c SET revoked_at = $2 FROM app.hosts h
		 WHERE c.host_id = h.id AND h.org_id = $1::uuid AND c.revoked_at IS NULL`,
		`UPDATE app.services s SET gone_at = h.revoked_at FROM app.hosts h
		 WHERE s.host_id = h.id AND h.org_id = $1::uuid AND h.revoked_at IS NOT NULL AND s.gone_at IS NULL`,
		`UPDATE app.join_tokens SET revoked_at = $2 WHERE org_id = $1::uuid AND revoked_at IS NULL`,
	} {
		args := []any{id.String(), at}
		if !strings.Contains(q, "$2") {
			args = args[:1]
		}
		if err := exec(ctx, tx, q, args...); err != nil {
			return nil, mapErr(err)
		}
	}
	hosts, err := collectIDs(ctx, tx, `SELECT id::text FROM app.hosts WHERE org_id = $1::uuid ORDER BY id::text`, id.String())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, mapErr(err)
	}
	return hosts, nil
}

func collectIDs(ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	ids := []uuid.UUID{}
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, mapErr(err)
		}
		id, err := parseID(text)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, mapErr(rows.Err())
}

func (d *DB) liveOrg(ctx context.Context, q queryer, id uuid.UUID) error {
	var alive bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.orgs WHERE id = $1::uuid AND deleted_at IS NULL)`, id.String()).Scan(&alive); err != nil {
		return mapErr(err)
	}
	if !alive {
		return store.ErrNotFound
	}
	return nil
}

const orgMemberCols = `m.user_id::text, u.email, u.name, m.role, m.added_at`

func scanOrgMember(row pgx.Row) (store.OrgMember, error) {
	var m store.OrgMember
	var id string
	if err := row.Scan(&id, &m.Email, &m.Name, &m.Role, &m.AddedAt); err != nil {
		return store.OrgMember{}, mapErr(err)
	}
	var err error
	m.UserID, err = parseID(id)
	return m, err
}

func (d *DB) ListOrgMembers(ctx context.Context, orgID uuid.UUID, limit int, cursor *store.Cursor) ([]store.OrgMember, *store.Cursor, error) {
	if err := d.liveOrg(ctx, d.pool, orgID); err != nil {
		return nil, nil, err
	}
	lim := pageLimit(limit)
	var atArg, idArg any
	if cursor != nil {
		atArg, idArg = cursor.At, cursor.ID
	}
	rows, err := d.pool.Query(ctx, `
SELECT `+orgMemberCols+`
FROM app.org_members m
JOIN app.users u ON u.id = m.user_id
WHERE m.org_id = $1::uuid
  AND (
    CAST($2 AS timestamptz) IS NULL
    OR (m.added_at, m.user_id) > (CAST($2 AS timestamptz), CAST($3 AS uuid))
  )
ORDER BY m.added_at ASC, m.user_id ASC
LIMIT $4
`, orgID.String(), atArg, idArg, lim+1)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	defer rows.Close()
	items := []store.OrgMember{}
	for rows.Next() {
		m, err := scanOrgMember(rows)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, mapErr(err)
	}
	return cutPage(items, lim, func(m store.OrgMember) time.Time { return m.AddedAt }, func(m store.OrgMember) string { return m.UserID.String() })
}

func (d *DB) AddOrgMember(ctx context.Context, orgID, userID uuid.UUID, role string, addedBy uuid.UUID, at time.Time) (store.OrgMember, error) {
	if role != store.OrgRoleAdmin && role != store.OrgRoleMember {
		return store.OrgMember{}, store.ErrInvalid
	}
	if err := d.liveOrg(ctx, d.pool, orgID); err != nil {
		return store.OrgMember{}, err
	}
	var exists bool
	if err := d.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app.users WHERE id = $1::uuid)`, userID.String()).Scan(&exists); err != nil {
		return store.OrgMember{}, mapErr(err)
	}
	if !exists {
		return store.OrgMember{}, store.ErrNotFound
	}
	if err := exec(ctx, d.pool, `
INSERT INTO app.org_members (org_id, user_id, role, added_by, added_at)
VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5)
`, orgID.String(), userID.String(), role, addedBy.String(), at); err != nil {
		if isUnique(err) {
			return store.OrgMember{}, store.ErrConflict
		}
		return store.OrgMember{}, mapErr(err)
	}
	return d.orgMember(ctx, orgID, userID)
}

func (d *DB) orgMember(ctx context.Context, orgID, userID uuid.UUID) (store.OrgMember, error) {
	return scanOrgMember(d.pool.QueryRow(ctx, `
SELECT `+orgMemberCols+`
FROM app.org_members m JOIN app.users u ON u.id = m.user_id
WHERE m.org_id = $1::uuid AND m.user_id = $2::uuid
`, orgID.String(), userID.String()))
}

// memberRole locks and returns the member's role. A missing org or
// member is ErrNotFound.
func memberRole(ctx context.Context, tx pgx.Tx, orgID, userID uuid.UUID) (string, error) {
	var role string
	err := tx.QueryRow(ctx, `
SELECT m.role FROM app.org_members m
JOIN app.orgs o ON o.id = m.org_id AND o.deleted_at IS NULL
WHERE m.org_id = $1::uuid AND m.user_id = $2::uuid
FOR UPDATE OF m
`, orgID.String(), userID.String()).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", store.ErrNotFound
	}
	return role, mapErr(err)
}

func (d *DB) UpdateOrgMemberRole(ctx context.Context, orgID, userID uuid.UUID, role string) (store.OrgMember, error) {
	if role != store.OrgRoleAdmin && role != store.OrgRoleMember {
		return store.OrgMember{}, store.ErrInvalid
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return store.OrgMember{}, mapErr(err)
	}
	defer tx.Rollback(ctx)
	current, err := memberRole(ctx, tx, orgID, userID)
	if err != nil {
		return store.OrgMember{}, err
	}
	if current == store.OrgRoleOwner {
		return store.OrgMember{}, store.ErrOwner
	}
	if err := exec(ctx, tx, `UPDATE app.org_members SET role = $3 WHERE org_id = $1::uuid AND user_id = $2::uuid`, orgID.String(), userID.String(), role); err != nil {
		return store.OrgMember{}, mapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return store.OrgMember{}, mapErr(err)
	}
	return d.orgMember(ctx, orgID, userID)
}

func (d *DB) RemoveOrgMember(ctx context.Context, orgID, userID uuid.UUID) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback(ctx)
	current, err := memberRole(ctx, tx, orgID, userID)
	if err != nil {
		return err
	}
	if current == store.OrgRoleOwner {
		return store.ErrOwner
	}
	if err := exec(ctx, tx, `DELETE FROM app.org_members WHERE org_id = $1::uuid AND user_id = $2::uuid`, orgID.String(), userID.String()); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit(ctx))
}

func (d *DB) TransferOrgOwnership(ctx context.Context, orgID, newOwner uuid.UUID) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback(ctx)
	current, err := memberRole(ctx, tx, orgID, newOwner)
	if err != nil {
		return err
	}
	if current == store.OrgRoleOwner {
		return nil
	}
	// The one-owner index is checked per statement: demote first.
	if err := exec(ctx, tx, `UPDATE app.org_members SET role = 'admin' WHERE org_id = $1::uuid AND role = 'owner'`, orgID.String()); err != nil {
		return mapErr(err)
	}
	if err := exec(ctx, tx, `UPDATE app.org_members SET role = 'owner' WHERE org_id = $1::uuid AND user_id = $2::uuid`, orgID.String(), newOwner.String()); err != nil {
		return mapErr(err)
	}
	return mapErr(tx.Commit(ctx))
}
