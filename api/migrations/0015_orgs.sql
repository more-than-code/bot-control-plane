-- Organizations, phase 1 (api-v1 v1.24 item 9, spec-organizations §4.1).
-- Applied only by `deploybot-server api --migrate` (SET ROLE deploybot_owner).
-- The hand-run rollback is 0015_orgs.down.sql. --migrate does not apply *.down.sql.
--
-- An org owns hosts and contains projects. host_access replaces host_shares.
-- Phase 1 is additive: hosts.project_id, host_shares and the approval tables
-- stay (read-only for the new API) so v0.2.29 can run after the down script.
--
-- The org mapping is data. db.Migrate sets the session settings
-- deploybot.org_mapping (a JSON array of project_slug, org_slug, org_name,
-- owner_email) and deploybot.org_mapping_require_all on its one connection,
-- from BOT_API_ORG_MAPPING_FILE when that is set. This file copies them into
-- the scratch table app.org_migration_mapping and drops it at the end.
-- Projects the mapping names go to its orgs. Every other project uses the default
-- rule: one personal org per project owner, slug u-<12 hex of the user id>.
-- Any mapping error raises, and the whole file rolls back.

CREATE TABLE app.org_migration_mapping AS
SELECT x.project_slug, x.org_slug, x.org_name, x.owner_email
FROM jsonb_to_recordset(COALESCE(NULLIF(current_setting('deploybot.org_mapping', true), ''), '[]')::jsonb)
    AS x(project_slug text, org_slug text, org_name text, owner_email text);

DO $check$
DECLARE
    bad text;
BEGIN
    SELECT string_agg(org_slug, ', ') INTO bad
    FROM (SELECT DISTINCT org_slug FROM app.org_migration_mapping
          WHERE org_slug !~ '^[a-z0-9][a-z0-9-]{0,63}$' OR org_slug LIKE 'u-%') AS s;
    IF bad IS NOT NULL THEN
        RAISE EXCEPTION 'org mapping: invalid org slug: %', bad;
    END IF;

    SELECT string_agg(org_slug, ', ') INTO bad
    FROM (SELECT org_slug FROM app.org_migration_mapping
          GROUP BY org_slug
          HAVING count(DISTINCT org_name) > 1 OR count(DISTINCT lower(owner_email)) > 1) AS s;
    IF bad IS NOT NULL THEN
        RAISE EXCEPTION 'org mapping: one org has several names or owners: %', bad;
    END IF;

    SELECT string_agg(org_name, ', ') INTO bad
    FROM (SELECT DISTINCT org_name FROM app.org_migration_mapping
          WHERE length(btrim(org_name)) = 0 OR char_length(org_name) > 80) AS s;
    IF bad IS NOT NULL THEN
        RAISE EXCEPTION 'org mapping: invalid org name: %', bad;
    END IF;

    SELECT string_agg(m.project_slug, ', ') INTO bad
    FROM app.org_migration_mapping AS m
    WHERE NOT EXISTS (SELECT 1 FROM app.projects AS p WHERE p.slug = m.project_slug);
    IF bad IS NOT NULL THEN
        RAISE EXCEPTION 'org mapping: unknown project slug: %', bad;
    END IF;

    SELECT string_agg(DISTINCT m.owner_email, ', ') INTO bad
    FROM app.org_migration_mapping AS m
    WHERE NOT EXISTS (
        SELECT 1 FROM app.users AS u
        WHERE lower(u.email) = lower(m.owner_email) AND u.disabled_at IS NULL
    );
    IF bad IS NOT NULL THEN
        RAISE EXCEPTION 'org mapping: unknown or disabled owner: %', bad;
    END IF;

    IF current_setting('deploybot.org_mapping_require_all', true) = 'true' THEN
        SELECT string_agg(p.slug, ', ' ORDER BY p.slug) INTO bad
        FROM app.projects AS p
        WHERE p.deleted_at IS NULL
          AND NOT EXISTS (SELECT 1 FROM app.org_migration_mapping AS m WHERE m.project_slug = p.slug);
        IF bad IS NOT NULL THEN
            RAISE EXCEPTION 'org mapping: require_all_projects, but these live projects are not mapped: %', bad;
        END IF;
    END IF;
END
$check$;

CREATE TABLE app.orgs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL,
    slug text NOT NULL,
    created_by uuid NOT NULL REFERENCES app.users (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    CONSTRAINT orgs_slug_unique UNIQUE (slug),
    CONSTRAINT orgs_slug_chk CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,63}$')
);

CREATE TABLE app.org_members (
    org_id uuid NOT NULL REFERENCES app.orgs (id),
    user_id uuid NOT NULL REFERENCES app.users (id),
    role text NOT NULL,
    added_by uuid REFERENCES app.users (id),
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, user_id),
    CONSTRAINT org_members_role_chk CHECK (role IN ('owner', 'admin', 'member'))
);

CREATE UNIQUE INDEX org_members_one_owner ON app.org_members (org_id) WHERE role = 'owner';
CREATE INDEX org_members_user_idx ON app.org_members (user_id);

CREATE TABLE app.host_access (
    host_id uuid NOT NULL REFERENCES app.hosts (id) ON DELETE CASCADE,
    project_id uuid NOT NULL REFERENCES app.projects (id) ON DELETE CASCADE,
    level text NOT NULL,
    granted_by uuid REFERENCES app.users (id),
    granted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (host_id, project_id),
    CONSTRAINT host_access_level_chk CHECK (level IN ('metrics', 'deploy'))
);

CREATE INDEX host_access_project_idx ON app.host_access (project_id);

ALTER TABLE app.projects ADD COLUMN org_id uuid REFERENCES app.orgs (id);
ALTER TABLE app.hosts ADD COLUMN org_id uuid REFERENCES app.orgs (id);
ALTER TABLE app.join_tokens ADD COLUMN org_id uuid REFERENCES app.orgs (id);
ALTER TABLE app.join_tokens ADD COLUMN grant_project_id uuid REFERENCES app.projects (id);
ALTER TABLE app.join_tokens ADD COLUMN grant_level text;
ALTER TABLE app.join_tokens
    ADD CONSTRAINT join_tokens_grant_chk CHECK (
        (grant_project_id IS NULL AND grant_level IS NULL)
        OR (grant_project_id IS NOT NULL AND grant_level IN ('metrics', 'deploy'))
    );
ALTER TABLE app.audit_log ADD COLUMN org_id uuid REFERENCES app.orgs (id);

-- Mapped orgs (production, D-7). The owner is the mapped owner.
INSERT INTO app.orgs (name, slug, created_by)
SELECT DISTINCT m.org_name, m.org_slug, u.id
FROM app.org_migration_mapping AS m
JOIN app.users AS u ON lower(u.email) = lower(m.owner_email);

UPDATE app.projects AS p
SET org_id = o.id
FROM app.org_migration_mapping AS m
JOIN app.orgs AS o ON o.slug = m.org_slug
WHERE p.slug = m.project_slug;

-- Default rule: one personal org per owner of an unmapped project. A
-- project with no owner row (only deleted projects) uses its creator.
CREATE TABLE app.org_migration_default_owner AS
SELECT p.id AS project_id, COALESCE(pm.user_id, p.created_by) AS owner_id
FROM app.projects AS p
LEFT JOIN app.project_members AS pm ON pm.project_id = p.id AND pm.role = 'owner'
WHERE p.org_id IS NULL;

INSERT INTO app.orgs (name, slug, created_by)
SELECT DISTINCT
    left(COALESCE(NULLIF(btrim(u.name), ''), split_part(u.email, '@', 1), 'My'), 74) || '''s org',
    'u-' || left(replace(u.id::text, '-', ''), 12),
    u.id
FROM app.org_migration_default_owner AS d
JOIN app.users AS u ON u.id = d.owner_id;

UPDATE app.projects AS p
SET org_id = o.id
FROM app.org_migration_default_owner AS d
JOIN app.orgs AS o ON o.slug = 'u-' || left(replace(d.owner_id::text, '-', ''), 12)
WHERE p.id = d.project_id;

-- Org owners only. Other project members are not added (D-2).
INSERT INTO app.org_members (org_id, user_id, role, added_by)
SELECT o.id, o.created_by, 'owner', o.created_by
FROM app.orgs AS o
ON CONFLICT DO NOTHING;

-- Hosts follow their owning project, revoked records included.
UPDATE app.hosts AS h
SET org_id = p.org_id
FROM app.projects AS p
WHERE p.id = h.project_id;

-- Host names are unique per org. A later duplicate (another project of the
-- same org had the name, ordered by enrolled_at, then id) becomes the first
-- 54 characters of its name, '-', and the first 8 characters of its id.
-- Each rename is recorded as a system audit row (host.migration_rename) for
-- the after report.
INSERT INTO app.audit_log (via, org_id, project_id, host_id, action, decision, detail)
SELECT 'system', h.org_id, h.project_id, h.id, 'host.migration_rename', 'allowed',
       jsonb_build_object('from', h.name, 'to', left(h.name, 54) || '-' || left(h.id::text, 8), 'migration', '0015_orgs.sql')
FROM app.hosts AS h
JOIN (
    SELECT id, row_number() OVER (PARTITION BY org_id, name ORDER BY enrolled_at, id) AS rn
    FROM app.hosts
) AS d ON d.id = h.id
WHERE d.rn > 1;

UPDATE app.hosts AS h
SET name = left(h.name, 54) || '-' || left(h.id::text, 8)
FROM (
    SELECT id, row_number() OVER (PARTITION BY org_id, name ORDER BY enrolled_at, id) AS rn
    FROM app.hosts
) AS d
WHERE d.id = h.id AND d.rn > 1;

-- Every project keeps what it has: the former owner gets deploy access,
-- and a share in the same org becomes an access row.
INSERT INTO app.host_access (host_id, project_id, level, granted_by, granted_at)
SELECT h.id, h.project_id, 'deploy', NULL, h.enrolled_at
FROM app.hosts AS h;

INSERT INTO app.host_access (host_id, project_id, level, granted_by, granted_at)
SELECT s.host_id, s.project_id, s.level, s.created_by, s.created_at
FROM app.host_shares AS s
JOIN app.hosts AS h ON h.id = s.host_id
JOIN app.projects AS p ON p.id = s.project_id
WHERE p.org_id = h.org_id
ON CONFLICT DO NOTHING;

-- A cross-org share has no place in the new model (D-3). It is reported
-- and left in host_shares, which the new API does not read.
DO $report$
DECLARE
    n integer;
BEGIN
    SELECT count(*) INTO n
    FROM app.host_shares AS s
    JOIN app.hosts AS h ON h.id = s.host_id
    JOIN app.projects AS p ON p.id = s.project_id
    WHERE p.org_id <> h.org_id;
    IF n > 0 THEN
        RAISE NOTICE 'org migration: % cross-org host share(s) dropped from access', n;
    END IF;
END
$report$;

UPDATE app.join_tokens AS t
SET org_id = p.org_id, grant_project_id = t.project_id, grant_level = 'deploy'
FROM app.projects AS p
WHERE p.id = t.project_id;

-- Audit rows: host events take the host's org, other project rows the project's.
UPDATE app.audit_log AS a
SET org_id = h.org_id
FROM app.hosts AS h
WHERE a.host_id = h.id AND a.org_id IS NULL;

UPDATE app.audit_log AS a
SET org_id = p.org_id
FROM app.projects AS p
WHERE a.project_id = p.id AND a.org_id IS NULL;

-- New hosts may have no project, and org-level services have none.
-- 0015_orgs.down.sql fills these back before a v0.2.29 rollback.
ALTER TABLE app.hosts ALTER COLUMN project_id DROP NOT NULL;
ALTER TABLE app.services ALTER COLUMN project_id DROP NOT NULL;
ALTER TABLE app.join_tokens ALTER COLUMN project_id DROP NOT NULL;
ALTER TABLE app.service_confirmations ALTER COLUMN project_id DROP NOT NULL;

-- Discovered services with no Deploybot owner become org-level (D-5).
UPDATE app.services AS s
SET project_id = NULL
FROM app.hosts AS h
WHERE h.id = s.host_id
  AND s.source = 'discovered'
  AND s.project_id = h.project_id;

ALTER TABLE app.projects ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE app.hosts ALTER COLUMN org_id SET NOT NULL;
ALTER TABLE app.join_tokens ALTER COLUMN org_id SET NOT NULL;

ALTER TABLE app.hosts ADD CONSTRAINT hosts_org_name_unique UNIQUE (org_id, name);

CREATE INDEX projects_org_idx ON app.projects (org_id);
CREATE INDEX join_tokens_org_idx ON app.join_tokens (org_id, created_at, id);
CREATE INDEX audit_log_org_at_idx ON app.audit_log (org_id, at DESC, id DESC);

DROP TABLE app.org_migration_default_owner;
DROP TABLE app.org_migration_mapping;

-- Default privileges already grant app CRUD to deploybot_api. Say it here
-- as well so the new tables do not depend on grant timing.
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE app.orgs TO deploybot_api;
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE app.org_members TO deploybot_api;
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE app.host_access TO deploybot_api;
