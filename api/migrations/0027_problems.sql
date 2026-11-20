-- Problems in the bell (api-v1 v1.41, spec docs/spec-bell-problems.md
-- rev 1). Applied only by `bot-api-server --migrate` (SET ROLE
-- deploybot_owner). The matching rollback is 0027_problems.down.sql.
-- --migrate does not apply *.down.sql.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)
--
-- problems: one row per problem a detector found (host_offline,
-- registry_login, deploy). subject is the identity of the problem within
-- its kind (the host id, project_id:registry host, or entry_id:host_id).
-- At most one open row (resolved_at null) per kind and subject. A problem
-- that returns within 30 minutes of resolving reopens the same row. The API
-- deletes resolved rows older than 30 days.
--
-- Every reference cascades: when the parent row is deleted the problem is
-- about nothing, so it goes with it and never dangles. Only catalog entries
-- are deleted in practice (their deploy history in app.image_deploys
-- cascades the same way). Orgs, projects and hosts are soft-deleted, so
-- their cascade never fires today.
--
-- title and detail are short human text written by the API. They never
-- hold a token, secret, env value, log line or email address.

CREATE TABLE app.problems (
    id uuid PRIMARY KEY,
    kind text NOT NULL,
    subject text NOT NULL,
    org_id uuid NOT NULL,
    project_id uuid,
    host_id uuid,
    entry_id uuid,
    level text NOT NULL,
    title text NOT NULL,
    detail text NOT NULL,
    opened_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    resolved_at timestamptz,
    CONSTRAINT problems_org_fk FOREIGN KEY (org_id) REFERENCES app.orgs (id) ON DELETE CASCADE,
    CONSTRAINT problems_project_fk FOREIGN KEY (project_id) REFERENCES app.projects (id) ON DELETE CASCADE,
    CONSTRAINT problems_host_fk FOREIGN KEY (host_id) REFERENCES app.hosts (id) ON DELETE CASCADE,
    CONSTRAINT problems_entry_fk FOREIGN KEY (entry_id) REFERENCES app.catalog_entries (id) ON DELETE CASCADE,
    CONSTRAINT problems_kind_chk CHECK (kind IN ('host_offline', 'registry_login', 'deploy')),
    CONSTRAINT problems_subject_chk CHECK (char_length(subject) BETWEEN 1 AND 300),
    CONSTRAINT problems_level_chk CHECK (level IN ('needs_action', 'heads_up')),
    CONSTRAINT problems_title_chk CHECK (char_length(title) BETWEEN 1 AND 300),
    CONSTRAINT problems_detail_chk CHECK (char_length(detail) <= 300),
    CONSTRAINT problems_resolved_chk CHECK (resolved_at IS NULL OR resolved_at >= opened_at)
);

CREATE UNIQUE INDEX problems_open_uidx ON app.problems (kind, subject) WHERE resolved_at IS NULL;

CREATE INDEX problems_org_idx ON app.problems (org_id, resolved_at);

CREATE INDEX problems_project_idx ON app.problems (project_id, resolved_at) WHERE project_id IS NOT NULL;

-- Default privileges grant app CRUD. Detectors insert, update the text,
-- level and times, and the prune deletes.
REVOKE ALL ON TABLE app.problems FROM deploybot_api;
GRANT SELECT, INSERT, DELETE ON TABLE app.problems TO deploybot_api;
GRANT UPDATE (level, title, detail, updated_at, resolved_at) ON TABLE app.problems TO deploybot_api;
