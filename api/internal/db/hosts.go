package db

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

const joinCols = `id::text, org_id::text, grant_project_id::text, grant_level, host_name_hint,
       created_by::text, created_at, expires_at, used_at, used_by_host_id::text, revoked_at`

func (d *DB) CreateJoinToken(ctx context.Context, orgID uuid.UUID, hash []byte, hint *string, grant *store.TokenGrant, createdBy uuid.UUID, expiresAt, at time.Time) (store.JoinToken, error) {
	var grantProject, grantLevel any
	if grant != nil {
		if !store.ValidAccessLevel(grant.Level) {
			return store.JoinToken{}, store.ErrInvalid
		}
		grantProject, grantLevel = grant.ProjectID.String(), grant.Level
	}
	// project_id is a legacy column that still copies the grant project
	// (null without a grant). The API reads grant_project_id; nothing reads
	// project_id since 0016 made the 0015 down script unusable.
	return scanJoin(d.pool.QueryRow(ctx, `
INSERT INTO app.join_tokens (
    id, org_id, project_id, grant_project_id, grant_level, token_hash, host_name_hint,
    created_by, created_at, expires_at
)
SELECT $1::uuid, o.id, CAST($2 AS uuid), CAST($2 AS uuid), $3, $4, $5, $6::uuid, $7, $8
FROM app.orgs o
WHERE o.id = $9::uuid AND o.deleted_at IS NULL
  AND (
    CAST($2 AS uuid) IS NULL
    OR EXISTS (
        SELECT 1 FROM app.projects p
        WHERE p.id = CAST($2 AS uuid) AND p.org_id = o.id AND p.deleted_at IS NULL
    )
  )
RETURNING `+joinCols,
		uuid.New().String(), grantProject, grantLevel, hash, optString(hint), createdBy.String(), at, expiresAt, orgID.String()))
}

func (d *DB) ListJoinTokens(ctx context.Context, orgID uuid.UUID, grantProject *uuid.UUID, limit int, cursor *store.Cursor) ([]store.JoinToken, *store.Cursor, error) {
	lim := pageLimit(limit)
	var atArg, idArg, projectArg any
	if cursor != nil {
		atArg = cursor.At
		idArg = cursor.ID
	}
	if grantProject != nil {
		projectArg = grantProject.String()
	}
	rows, err := d.pool.Query(ctx, `
SELECT `+joinCols+`
FROM app.join_tokens
WHERE org_id = $1::uuid
  AND (CAST($5 AS uuid) IS NULL OR grant_project_id = CAST($5 AS uuid))
  AND (
    CAST($2 AS timestamptz) IS NULL
    OR (created_at, id) > (CAST($2 AS timestamptz), CAST($3 AS uuid))
  )
ORDER BY created_at ASC, id ASC
LIMIT $4
`, orgID.String(), atArg, idArg, lim+1, projectArg)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	defer rows.Close()
	items := []store.JoinToken{}
	for rows.Next() {
		tok, err := scanJoin(rows)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, tok)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, mapErr(err)
	}
	return cutPage(items, lim, func(t store.JoinToken) time.Time { return t.CreatedAt }, func(t store.JoinToken) string { return t.ID.String() })
}

func (d *DB) RevokeJoinToken(ctx context.Context, orgID uuid.UUID, grantProject *uuid.UUID, tokenID uuid.UUID, at time.Time) error {
	var projectArg any
	if grantProject != nil {
		projectArg = grantProject.String()
	}
	tag, err := d.pool.Exec(ctx, `
UPDATE app.join_tokens
SET revoked_at = COALESCE(revoked_at, $3)
WHERE id = $2::uuid AND org_id = $1::uuid
  AND (CAST($4 AS uuid) IS NULL OR grant_project_id = CAST($4 AS uuid))
`, orgID.String(), tokenID.String(), at, projectArg)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *DB) ConsumeJoinToken(ctx context.Context, tokenHash []byte, in store.EnrollInput, credentialID uuid.UUID, credentialHash []byte, at time.Time) (store.Host, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return store.Host{}, mapErr(err)
	}
	defer tx.Rollback(ctx)

	var tok store.JoinToken
	var idText, orgText, creatorText string
	var grantText, grantLevel *string
	var stored []byte
	err = tx.QueryRow(ctx, `
SELECT id::text, org_id::text, grant_project_id::text, grant_level, token_hash, used_at, revoked_at, expires_at, created_by::text
FROM app.join_tokens
WHERE token_hash = $1
FOR UPDATE
`, tokenHash).Scan(&idText, &orgText, &grantText, &grantLevel, &stored, &tok.UsedAt, &tok.RevokedAt, &tok.ExpiresAt, &creatorText)
	if errors.Is(err, pgx.ErrNoRows) {
		secret.Equal(tokenHash, missHash)
		return store.Host{}, store.ErrUnauthorized
	}
	if err != nil {
		return store.Host{}, mapErr(err)
	}
	if !secret.Equal(tokenHash, stored) || tok.UsedAt != nil || tok.RevokedAt != nil || !at.Before(tok.ExpiresAt) {
		return store.Host{}, store.ErrUnauthorized
	}
	if tok.ID, err = parseID(idText); err != nil {
		return store.Host{}, err
	}
	if tok.OrgID, err = parseID(orgText); err != nil {
		return store.Host{}, err
	}
	var deleted *time.Time
	err = tx.QueryRow(ctx, `SELECT deleted_at FROM app.orgs WHERE id = $1::uuid`, tok.OrgID.String()).Scan(&deleted)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && deleted != nil) {
		return store.Host{}, store.ErrUnauthorized
	}
	if err != nil {
		return store.Host{}, mapErr(err)
	}
	// The creator must still own or administer the org (review A7);
	// otherwise the token is as good as unknown.
	var creatorAdmin bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM app.org_members
    WHERE org_id = $1::uuid AND user_id = $2::uuid AND role IN ('owner', 'admin')
)
`, tok.OrgID.String(), creatorText).Scan(&creatorAdmin); err != nil {
		return store.Host{}, mapErr(err)
	}
	if !creatorAdmin {
		return store.Host{}, store.ErrUnauthorized
	}
	// A grant whose project has since been deleted is dropped; the host
	// still joins the org.
	var grant *store.TokenGrant
	if grantText != nil && grantLevel != nil {
		var live bool
		if err := tx.QueryRow(ctx, `
SELECT EXISTS (SELECT 1 FROM app.projects WHERE id = $1::uuid AND org_id = $2::uuid AND deleted_at IS NULL)
`, *grantText, tok.OrgID.String()).Scan(&live); err != nil {
			return store.Host{}, mapErr(err)
		}
		if live {
			pid, err := parseID(*grantText)
			if err != nil {
				return store.Host{}, err
			}
			grant = &store.TokenGrant{ProjectID: pid, Level: *grantLevel}
		}
	}
	var takenErr error
	name, err := store.Unique(in.Name, 63, func(candidate string) bool {
		if takenErr != nil {
			return true
		}
		var exists bool
		qerr := tx.QueryRow(ctx, `
SELECT EXISTS (SELECT 1 FROM app.hosts WHERE org_id = $1::uuid AND name = $2)
`, tok.OrgID.String(), candidate).Scan(&exists)
		if qerr != nil {
			takenErr = qerr
			return true
		}
		return exists
	})
	if takenErr != nil {
		return store.Host{}, mapErr(takenErr)
	}
	if err != nil {
		return store.Host{}, err
	}

	hostID := uuid.New()
	if _, err := tx.Exec(ctx, `SAVEPOINT host_name`); err != nil {
		return store.Host{}, mapErr(err)
	}
	n := 2
	for {
		_, err = tx.Exec(ctx, `
INSERT INTO app.hosts (
    id, org_id, name, node_type, os, arch, always_on, agent_version, last_seen_at, enrolled_at
) VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $9)
`, hostID.String(), tok.OrgID.String(), name, in.NodeType, in.OS, in.Arch, in.AlwaysOn, in.AgentVersion, at)
		if err == nil {
			break
		}
		if !isUnique(err) {
			return store.Host{}, mapErr(err)
		}
		if _, rerr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT host_name`); rerr != nil {
			return store.Host{}, mapErr(rerr)
		}
		if n >= 1000 {
			return store.Host{}, store.ErrConflict
		}
		name = store.WithSuffix(in.Name, n, 63)
		n++
	}
	if grant != nil {
		if err := exec(ctx, tx, `
INSERT INTO app.host_access (host_id, project_id, level, granted_by, granted_at)
VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5)
`, hostID.String(), grant.ProjectID.String(), grant.Level, creatorText, at); err != nil {
			return store.Host{}, mapErr(err)
		}
	}
	if err := exec(ctx, tx, `
INSERT INTO app.agent_credentials (id, host_id, credential_hash, created_at)
VALUES ($1::uuid, $2::uuid, $3, $4)
`, credentialID.String(), hostID.String(), credentialHash, at); err != nil {
		return store.Host{}, mapErr(err)
	}
	if err := exec(ctx, tx, `
UPDATE app.join_tokens
SET used_at = $2, used_by_host_id = $3::uuid
WHERE id = $1::uuid
`, tok.ID.String(), at, hostID.String()); err != nil {
		return store.Host{}, mapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Host{}, mapErr(err)
	}
	seen := at
	host := store.Host{
		ID:           hostID,
		OrgID:        tok.OrgID,
		Name:         name,
		NodeType:     in.NodeType,
		OS:           in.OS,
		Arch:         in.Arch,
		AlwaysOn:     in.AlwaysOn,
		AgentVersion: in.AgentVersion,
		LastSeenAt:   &seen,
		EnrolledAt:   at,
	}
	if grant != nil {
		pid := grant.ProjectID
		host.GrantProjectID = &pid
	}
	return host, nil
}

const hostCols = `h.id::text, h.org_id::text, h.name, h.node_type, h.os, h.arch,
       h.always_on, h.agent_version, h.last_seen_at, h.enrolled_at, h.revoked_at, h.programs`

func (d *DB) GetHost(ctx context.Context, id uuid.UUID) (store.Host, error) {
	return scanHost(d.pool.QueryRow(ctx, `
SELECT `+hostCols+`
FROM app.hosts h
WHERE h.id = $1::uuid
`, id.String()))
}

// ListHosts is the hosts projectID has access to, by name (byte order,
// matching memstore), then id. Resume compares (name, id) with the cursor,
// written out with COLLATE "C" so it cannot fall back to the database
// locale. An old rank-1 cursor (shared hosts before v1.24) is past the end.
func (d *DB) ListHosts(ctx context.Context, projectID uuid.UUID, limit int, cursor *store.Cursor) ([]store.Host, *store.Cursor, error) {
	return d.listHosts(ctx, `JOIN app.host_access a ON a.host_id = h.id AND a.project_id = $1::uuid`, `a.level`, projectID, limit, cursor)
}

func (d *DB) ListOrgHosts(ctx context.Context, orgID uuid.UUID, limit int, cursor *store.Cursor) ([]store.Host, *store.Cursor, error) {
	var deleted *time.Time
	err := d.pool.QueryRow(ctx, `SELECT deleted_at FROM app.orgs WHERE id = $1::uuid`, orgID.String()).Scan(&deleted)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && deleted != nil) {
		return nil, nil, store.ErrNotFound
	}
	if err != nil {
		return nil, nil, mapErr(err)
	}
	items, next, err := d.listHosts(ctx, `JOIN app.orgs o ON o.id = h.org_id AND o.id = $1::uuid`, `NULL::text`, orgID, limit, cursor)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].ID.String()
	}
	byHost, err := d.accessForHosts(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	for i := range items {
		access := byHost[items[i].ID]
		if access == nil {
			access = []store.HostAccess{}
		}
		items[i].Access = &access
	}
	return items, next, nil
}

func (d *DB) listHosts(ctx context.Context, join, level string, id uuid.UUID, limit int, cursor *store.Cursor) ([]store.Host, *store.Cursor, error) {
	lim := pageLimit(limit)
	var rankArg, nameArg, idArg any
	if cursor != nil && cursor.Rank != nil {
		rankArg = *cursor.Rank
		nameArg = cursor.Name
		idArg = cursor.ID
	}
	rows, err := d.pool.Query(ctx, `
SELECT `+hostCols+`, `+level+`
FROM app.hosts h
`+join+`
-- Host cursors have rank 0 since v1.24. An old rank-1 cursor (shared hosts
-- before v1.24) matches no branch, so it is past the last page.
WHERE (
    CAST($2 AS int) IS NULL
    OR (CAST($2 AS int) = 0 AND (h.name COLLATE "C") > (CAST($3 AS text) COLLATE "C"))
    OR (
        CAST($2 AS int) = 0
        AND (h.name COLLATE "C") = (CAST($3 AS text) COLLATE "C")
        AND h.id > CAST($4 AS uuid)
    )
)
ORDER BY h.name COLLATE "C" ASC, h.id ASC
LIMIT $5
`, id.String(), rankArg, nameArg, idArg, lim+1)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	defer rows.Close()
	items := []store.Host{}
	for rows.Next() {
		h, err := scanListedHost(rows)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, h)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, mapErr(err)
	}
	if len(items) > lim {
		items = items[:lim]
		last := items[len(items)-1]
		rank := 0
		return items, &store.Cursor{Rank: &rank, Name: last.Name, ID: last.ID.String()}, nil
	}
	return items, nil, nil
}

func scanListedHost(row pgx.Row) (store.Host, error) {
	var level *string
	h, err := scanHostRow(row, &level)
	if err != nil {
		return store.Host{}, err
	}
	h.AccessLevel = level
	return h, nil
}

func (d *DB) RevokeHost(ctx context.Context, hostID uuid.UUID, at time.Time) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return mapErr(err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
UPDATE app.hosts
SET revoked_at = COALESCE(revoked_at, $2)
WHERE id = $1::uuid
`, hostID.String(), at)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	if err := exec(ctx, tx, `
UPDATE app.agent_credentials
SET revoked_at = $2
WHERE host_id = $1::uuid AND revoked_at IS NULL
`, hostID.String(), at); err != nil {
		return mapErr(err)
	}
	// Still-present rows go gone at the host's revoke time. A row that is
	// already gone keeps its time. A second revoke does not move it.
	if err := exec(ctx, tx, `
UPDATE app.services AS s
SET gone_at = h.revoked_at
FROM app.hosts AS h
WHERE s.host_id = h.id
  AND h.id = $1::uuid
  AND h.revoked_at IS NOT NULL
  AND s.gone_at IS NULL
`, hostID.String()); err != nil {
		return mapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return mapErr(err)
	}
	return nil
}

func (d *DB) TouchHost(ctx context.Context, hostID uuid.UUID, at time.Time, version, osName, arch string) error {
	tag, err := d.pool.Exec(ctx, `
UPDATE app.hosts
SET last_seen_at = $2,
    agent_version = CASE WHEN $3 <> '' THEN $3 ELSE agent_version END,
    os = CASE WHEN $4 <> '' THEN $4 ELSE os END,
    arch = CASE WHEN $5 <> '' THEN $5 ELSE arch END
WHERE id = $1::uuid
`, hostID.String(), at, version, osName, arch)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (d *DB) LookupCredential(ctx context.Context, hash []byte) (store.Credential, store.Host, error) {
	var cred store.Credential
	var credID string
	var stored []byte
	var revoked *time.Time
	var hostID string
	err := d.pool.QueryRow(ctx, `
SELECT c.id::text, c.host_id::text, c.credential_hash, c.revoked_at
FROM app.agent_credentials c
WHERE c.credential_hash = $1
`, hash).Scan(&credID, &hostID, &stored, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		secret.Equal(hash, missHash)
		return store.Credential{}, store.Host{}, store.ErrUnauthorized
	}
	if err != nil {
		return store.Credential{}, store.Host{}, mapErr(err)
	}
	if !secret.Equal(hash, stored) {
		return store.Credential{}, store.Host{}, store.ErrUnauthorized
	}
	if cred.ID, err = parseID(credID); err != nil {
		return store.Credential{}, store.Host{}, err
	}
	if cred.HostID, err = parseID(hostID); err != nil {
		return store.Credential{}, store.Host{}, err
	}
	cred.Hash = stored
	cred.RevokedAt = revoked
	host, err := d.GetHost(ctx, cred.HostID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Credential{}, store.Host{}, store.ErrUnauthorized
	}
	if err != nil {
		return store.Credential{}, store.Host{}, err
	}
	return cred, host, nil
}

func scanJoin(row pgx.Row) (store.JoinToken, error) {
	var t store.JoinToken
	var id, orgID, createdBy string
	var grantProject, grantLevel, usedBy *string
	if err := row.Scan(&id, &orgID, &grantProject, &grantLevel, &t.HostNameHint, &createdBy, &t.CreatedAt, &t.ExpiresAt, &t.UsedAt, &usedBy, &t.RevokedAt); err != nil {
		return store.JoinToken{}, mapErr(err)
	}
	var err error
	if t.ID, err = parseID(id); err != nil {
		return store.JoinToken{}, err
	}
	if t.OrgID, err = parseID(orgID); err != nil {
		return store.JoinToken{}, err
	}
	if grantProject != nil && grantLevel != nil {
		pid, err := parseID(*grantProject)
		if err != nil {
			return store.JoinToken{}, err
		}
		t.Grant = &store.TokenGrant{ProjectID: pid, Level: *grantLevel}
	}
	if t.CreatedBy, err = parseID(createdBy); err != nil {
		return store.JoinToken{}, err
	}
	if t.UsedByHostID, err = parseOptID(usedBy); err != nil {
		return store.JoinToken{}, err
	}
	return t, nil
}

func scanHost(row pgx.Row) (store.Host, error) {
	return scanHostRow(row)
}

// scanHostRow reads hostCols, then any extra destinations.
func scanHostRow(row pgx.Row, extra ...any) (store.Host, error) {
	var h store.Host
	var id, orgID string
	var programs []byte
	dest := []any{
		&id, &orgID, &h.Name, &h.NodeType, &h.OS, &h.Arch, &h.AlwaysOn,
		&h.AgentVersion, &h.LastSeenAt, &h.EnrolledAt, &h.RevokedAt, &programs,
	}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return store.Host{}, mapErr(err)
	}
	if err := applyPrograms(&h, programs); err != nil {
		return store.Host{}, err
	}
	var err error
	if h.ID, err = parseID(id); err != nil {
		return store.Host{}, err
	}
	if h.OrgID, err = parseID(orgID); err != nil {
		return store.Host{}, err
	}
	return h, nil
}
