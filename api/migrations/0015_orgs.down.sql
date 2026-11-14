-- Hand-run rollback for 0015_orgs.sql. --migrate does not apply this file.
-- It makes the database readable by v0.2.29 again (spec-organizations §7).
-- Run order: stop the API, take a dump, run this file as deploybot_owner,
-- switch `current` to v0.2.29, start the API.
--
-- It runs in one transaction, so a failure leaves 0015 fully applied.
-- Lost: orgs, org members, access grants and org join tokens made after the
-- release. host_shares is not touched. Hosts keep hosts.project_id (the
-- former owner). A host enrolled after the release with no grant takes a
-- project that has deploy access to it, else its org's oldest project.
--
-- After this script, delete the applied row before --migrate runs 0015 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0015_orgs.sql';

BEGIN;

-- Hosts with no project: a deploy project, then any access project, then
-- the org's oldest project (deleted ones too).
UPDATE app.hosts AS h
SET project_id = pick.project_id
FROM (
    SELECT DISTINCT ON (h2.id) h2.id AS host_id, a.project_id
    FROM app.hosts AS h2
    JOIN app.host_access AS a ON a.host_id = h2.id
    WHERE h2.project_id IS NULL
    ORDER BY h2.id, (a.level = 'deploy') DESC, a.granted_at, a.project_id
) AS pick
WHERE h.id = pick.host_id;

UPDATE app.hosts AS h
SET project_id = pick.project_id
FROM (
    SELECT DISTINCT ON (h2.id) h2.id AS host_id, p.id AS project_id
    FROM app.hosts AS h2
    JOIN app.projects AS p ON p.org_id = h2.org_id
    WHERE h2.project_id IS NULL
    ORDER BY h2.id, p.created_at, p.id
) AS pick
WHERE h.id = pick.host_id;

DO $check$
DECLARE
    n integer;
BEGIN
    SELECT count(*) INTO n FROM app.hosts WHERE project_id IS NULL;
    IF n > 0 THEN
        RAISE EXCEPTION 'org rollback: % host(s) belong to an org with no project; revoke and delete them first', n;
    END IF;
END
$check$;

-- hosts_project_name_unique (project_id, name) is back in force for those
-- hosts. A later duplicate is renamed as 0015 does and recorded as a system
-- audit row (host.migration_rename).
INSERT INTO app.audit_log (via, project_id, host_id, action, decision, detail)
SELECT 'system', h.project_id, h.id, 'host.migration_rename', 'allowed',
       jsonb_build_object('from', h.name, 'to', left(h.name, 54) || '-' || left(h.id::text, 8), 'migration', '0015_orgs.down.sql')
FROM app.hosts AS h
JOIN (
    SELECT id, row_number() OVER (PARTITION BY project_id, name ORDER BY enrolled_at, id) AS rn
    FROM app.hosts
) AS d ON d.id = h.id
WHERE d.rn > 1;

UPDATE app.hosts AS h
SET name = left(h.name, 54) || '-' || left(h.id::text, 8)
FROM (
    SELECT id, row_number() OVER (PARTITION BY project_id, name ORDER BY enrolled_at, id) AS rn
    FROM app.hosts
) AS d
WHERE d.id = h.id AND d.rn > 1;

-- Org-level services go back to the host's project.
UPDATE app.services AS s
SET project_id = h.project_id
FROM app.hosts AS h
WHERE h.id = s.host_id AND s.project_id IS NULL;

-- Org tokens with no project: an unused one is deleted, a used one takes
-- its host's project.
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

COMMIT;
