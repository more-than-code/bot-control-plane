-- Hand-run rollback for 0015_orgs.sql. --migrate does not apply this file.
-- It makes the database readable by v0.2.29 again (spec-organizations §7).
-- Run order: stop the API, take a dump, run this file as deploybot_owner
-- with `psql -v ON_ERROR_STOP=1 -f 0015_orgs.down.sql`, switch `current` to
-- v0.2.29, start the API. It deletes its own schema_migrations row, so a
-- later --migrate runs 0015 again.
--
-- It runs in one transaction, so a failure leaves 0015 fully applied.
-- Lost: orgs, org members and org join tokens made after the release, and
-- the org of audit rows that have no project (they keep the row).
-- Kept: every access grant, written back as the owner (hosts.project_id)
-- and host_shares rows. Cross-org host_shares rows, which 0015 never
-- converted, are left as they are.
--
-- hosts.project_id of a live host: kept when that project is live and has
-- deploy access; otherwise the live project with deploy access that was
-- granted first; otherwise the host is revoked (credentials too, services
-- gone), as v0.2.29 does on project delete. It never falls back to a
-- project with only metrics access or to a deleted project for a live host.

BEGIN;

-- Scratch tables in app (the migration roles have no TEMP on the database);
-- dropped before COMMIT.
CREATE TABLE app.org_down_revoked (host_id uuid PRIMARY KEY);
CREATE TABLE app.org_down_shares (host_id uuid, project_id uuid, level text);

-- Live hosts: the owner must be a live project with deploy access.
UPDATE app.hosts AS h
SET project_id = pick.project_id
FROM (
    SELECT DISTINCT ON (h2.id) h2.id AS host_id, a.project_id
    FROM app.hosts AS h2
    JOIN app.host_access AS a ON a.host_id = h2.id AND a.level = 'deploy'
    JOIN app.projects AS p ON p.id = a.project_id AND p.deleted_at IS NULL
    WHERE h2.revoked_at IS NULL
    ORDER BY h2.id, (a.project_id = h2.project_id) DESC, a.granted_at, a.project_id
) AS pick
WHERE h.id = pick.host_id AND h.project_id IS DISTINCT FROM pick.project_id;

-- A live host with no live deploy project is revoked.
INSERT INTO app.org_down_revoked (host_id)
SELECT h.id
FROM app.hosts AS h
WHERE h.revoked_at IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM app.host_access AS a
      JOIN app.projects AS p ON p.id = a.project_id AND p.deleted_at IS NULL
      WHERE a.host_id = h.id AND a.level = 'deploy'
  );

UPDATE app.hosts SET revoked_at = now()
WHERE id IN (SELECT host_id FROM app.org_down_revoked);

UPDATE app.agent_credentials SET revoked_at = now()
WHERE revoked_at IS NULL AND host_id IN (SELECT host_id FROM app.org_down_revoked);

UPDATE app.services AS s SET gone_at = h.revoked_at
FROM app.hosts AS h
WHERE s.host_id = h.id AND h.id IN (SELECT host_id FROM app.org_down_revoked) AND s.gone_at IS NULL;

-- A revoked host still needs an owner row for v0.2.29: a project that had
-- access (deploy first), else the org's oldest project, deleted ones included.
UPDATE app.hosts AS h
SET project_id = pick.project_id
FROM (
    SELECT DISTINCT ON (h2.id) h2.id AS host_id, a.project_id
    FROM app.hosts AS h2
    JOIN app.host_access AS a ON a.host_id = h2.id
    WHERE h2.revoked_at IS NOT NULL AND h2.project_id IS NULL
    ORDER BY h2.id, (a.level = 'deploy') DESC, a.granted_at, a.project_id
) AS pick
WHERE h.id = pick.host_id;

UPDATE app.hosts AS h
SET project_id = pick.project_id
FROM (
    SELECT DISTINCT ON (h2.id) h2.id AS host_id, p.id AS project_id
    FROM app.hosts AS h2
    JOIN app.projects AS p ON p.org_id = h2.org_id
    WHERE h2.revoked_at IS NOT NULL AND h2.project_id IS NULL
    ORDER BY h2.id, p.created_at, p.id
) AS pick
WHERE h.id = pick.host_id;

DO $check$
DECLARE
    n integer;
BEGIN
    SELECT count(*) INTO n FROM app.hosts WHERE project_id IS NULL;
    IF n > 0 THEN
        RAISE EXCEPTION 'org rollback: % revoked host(s) belong to an org that never had a project; delete them first', n;
    END IF;
END
$check$;

-- hosts_project_name_unique (project_id, name) is back in force. A later
-- duplicate (live hosts first) is renamed as 0015 does and recorded as a
-- system audit row (host.migration_rename).
INSERT INTO app.audit_log (via, project_id, host_id, action, decision, detail)
SELECT 'system', h.project_id, h.id, 'host.migration_rename', 'allowed',
       jsonb_build_object('from', h.name, 'to', left(h.name, 54) || '-' || left(h.id::text, 8), 'migration', '0015_orgs.down.sql')
FROM app.hosts AS h
JOIN (
    SELECT id, row_number() OVER (PARTITION BY project_id, name ORDER BY (revoked_at IS NOT NULL), enrolled_at, id) AS rn
    FROM app.hosts
) AS d ON d.id = h.id
WHERE d.rn > 1;

UPDATE app.hosts AS h
SET name = left(h.name, 54) || '-' || left(h.id::text, 8)
FROM (
    SELECT id, row_number() OVER (PARTITION BY project_id, name ORDER BY (revoked_at IS NOT NULL), enrolled_at, id) AS rn
    FROM app.hosts
) AS d
WHERE d.id = h.id AND d.rn > 1;

-- Access rows of other live projects become host_shares rows (level as
-- is). Same-org share rows with no access row any more are removed.
INSERT INTO app.org_down_shares (host_id, project_id, level)
SELECT a.host_id, a.project_id, a.level
FROM app.host_access AS a
JOIN app.hosts AS h ON h.id = a.host_id
JOIN app.projects AS p ON p.id = a.project_id AND p.deleted_at IS NULL
WHERE a.project_id <> h.project_id;

INSERT INTO app.host_shares (host_id, project_id, level, created_by, created_at)
SELECT a.host_id, a.project_id, a.level, a.granted_by, a.granted_at
FROM app.host_access AS a
JOIN app.org_down_shares AS d ON d.host_id = a.host_id AND d.project_id = a.project_id
ON CONFLICT (host_id, project_id) DO UPDATE SET level = EXCLUDED.level;

DELETE FROM app.host_shares AS s
USING app.hosts AS h, app.projects AS p
WHERE h.id = s.host_id AND p.id = s.project_id
  AND p.org_id = h.org_id
  AND NOT EXISTS (
      SELECT 1 FROM app.org_down_shares AS d
      WHERE d.host_id = s.host_id AND d.project_id = s.project_id
  );

-- Org-level services go back to the host's project.
UPDATE app.services AS s
SET project_id = h.project_id
FROM app.hosts AS h
WHERE h.id = s.host_id AND s.project_id IS NULL;

-- Org tokens with no project: a used one takes its grant or host project,
-- an unused one is deleted.
UPDATE app.join_tokens AS t
SET project_id = COALESCE(t.grant_project_id, h.project_id)
FROM app.hosts AS h
WHERE t.project_id IS NULL AND t.used_by_host_id = h.id;

UPDATE app.join_tokens
SET project_id = grant_project_id
WHERE project_id IS NULL AND grant_project_id IS NOT NULL;

DELETE FROM app.join_tokens WHERE project_id IS NULL;

-- Health-check plans for org-level services expire within minutes anyway.
DELETE FROM app.service_confirmations WHERE project_id IS NULL;

DO $report$
DECLARE
    revoked text;
    shares text;
BEGIN
    SELECT string_agg(host_id::text, ', ' ORDER BY host_id) INTO revoked FROM app.org_down_revoked;
    SELECT string_agg(host_id::text || '/' || project_id::text || '=' || level, ', ' ORDER BY host_id, project_id) INTO shares FROM app.org_down_shares;
    IF revoked IS NOT NULL THEN
        RAISE NOTICE 'org rollback: revoked hosts with no live deploy project: %', revoked;
    END IF;
    IF shares IS NOT NULL THEN
        RAISE NOTICE 'org rollback: access rows written back as host_shares: %', shares;
    END IF;
END
$report$;

ALTER TABLE app.hosts ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE app.services ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE app.join_tokens ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE app.service_confirmations ALTER COLUMN project_id SET NOT NULL;

ALTER TABLE app.hosts DROP CONSTRAINT IF EXISTS hosts_org_name_unique;
DROP INDEX IF EXISTS app.audit_log_org_at_idx;
DROP INDEX IF EXISTS app.join_tokens_org_idx;
DROP INDEX IF EXISTS app.projects_org_idx;

ALTER TABLE app.audit_log DROP COLUMN IF EXISTS org_id;
ALTER TABLE app.join_tokens DROP CONSTRAINT IF EXISTS join_tokens_grant_chk;
ALTER TABLE app.join_tokens DROP COLUMN IF EXISTS grant_level;
ALTER TABLE app.join_tokens DROP COLUMN IF EXISTS grant_project_id;
ALTER TABLE app.join_tokens DROP COLUMN IF EXISTS org_id;
ALTER TABLE app.hosts DROP COLUMN IF EXISTS org_id;
ALTER TABLE app.projects DROP COLUMN IF EXISTS org_id;

DROP TABLE IF EXISTS app.host_access;
DROP TABLE IF EXISTS app.org_members;
DROP TABLE IF EXISTS app.orgs;

DROP TABLE app.org_down_shares;
DROP TABLE app.org_down_revoked;

DELETE FROM app.schema_migrations WHERE name = '0015_orgs.sql';

COMMIT;
