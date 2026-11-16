-- Domains, a per-host edge and routes, stage 1 (api-v1 v1.35, spec
-- docs/spec-domains-edge-routes.md rev 2). Applied only by
-- `bot-api-server --migrate` (SET ROLE deploybot_owner). The matching
-- rollback is 0023_domains_edge_routes.down.sql. --migrate does not apply
-- *.down.sql.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)
--
-- org_domains: a domain an org adds and proves with a DNS TXT record.
-- status is pending, verified or lapsed. failures counts consecutive failed
-- daily re-checks of a verified or lapsed domain. A name is unique within
-- an org, and verified (or lapsed) by one org only: the partial unique
-- index below. token is the public verification token shown as the TXT
-- value. It is not a secret.
--
-- domain_assignments: which projects of the org may route under a domain.
-- pattern is @ (the name itself) or * (one label below it).
--
-- routes: hostname plus path prefix to an entry port, unique across the
-- instance. Hostnames and domain names have no all-digit last label, and
-- a path prefix is lowercase with no dot segment, as the API checks. entry_id cascades, so deleting an entry drops its routes. A
-- route never touches the entry spec_hash.
--
-- host_edges: one row per host whose edge was ever switched. status is off,
-- pending, applied or error. config_hash is the sha256 of the last config
-- the host applied.
--
-- edge_confirmations: single-use plans for a route replace (kind routes)
-- and a domain delete (kind domain_delete), as the other confirmation
-- tables. body never holds a secret. domain_id has no foreign key: the
-- confirmed delete removes the domain and keeps the used plan, so a second
-- confirm still reads it as used.
--
-- orgs.acme_email is the optional ACME contact of the org (api-v1 v1.35
-- item 5).

ALTER TABLE app.orgs ADD COLUMN acme_email text NULL;

ALTER TABLE app.orgs
    ADD CONSTRAINT orgs_acme_email_chk CHECK (acme_email IS NULL OR char_length(acme_email) BETWEEN 3 AND 254);

CREATE TABLE app.org_domains (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL,
    name text NOT NULL,
    token text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    verified_at timestamptz,
    last_check_at timestamptz,
    failures integer NOT NULL DEFAULT 0,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT org_domains_org_fk FOREIGN KEY (org_id) REFERENCES app.orgs (id) ON DELETE CASCADE,
    CONSTRAINT org_domains_created_by_fk FOREIGN KEY (created_by) REFERENCES app.users (id),
    CONSTRAINT org_domains_org_name_unique UNIQUE (org_id, name),
    CONSTRAINT org_domains_name_chk CHECK (char_length(name) <= 253 AND name ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$' AND name !~ '\.[0-9]+$'),
    CONSTRAINT org_domains_token_chk CHECK (token ~ '^[A-Za-z0-9_-]{16,64}$'),
    CONSTRAINT org_domains_status_chk CHECK (status IN ('pending', 'verified', 'lapsed')),
    CONSTRAINT org_domains_failures_chk CHECK (failures >= 0)
);

CREATE UNIQUE INDEX org_domains_verified_name_idx
    ON app.org_domains (name)
    WHERE status IN ('verified', 'lapsed');

CREATE TABLE app.domain_assignments (
    domain_id uuid NOT NULL,
    project_id uuid NOT NULL,
    pattern text NOT NULL,
    PRIMARY KEY (domain_id, project_id, pattern),
    CONSTRAINT domain_assignments_domain_fk FOREIGN KEY (domain_id) REFERENCES app.org_domains (id) ON DELETE CASCADE,
    CONSTRAINT domain_assignments_project_fk FOREIGN KEY (project_id) REFERENCES app.projects (id) ON DELETE CASCADE,
    CONSTRAINT domain_assignments_pattern_chk CHECK (pattern IN ('@', '*'))
);

CREATE INDEX domain_assignments_project_idx ON app.domain_assignments (project_id);

CREATE TABLE app.routes (
    id uuid PRIMARY KEY,
    project_id uuid NOT NULL,
    entry_id uuid NOT NULL,
    hostname text NOT NULL,
    path_prefix text NOT NULL DEFAULT '/',
    port integer NOT NULL,
    client_ip_from text,
    no_store_paths text[] NOT NULL DEFAULT '{}',
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL,
    CONSTRAINT routes_project_fk FOREIGN KEY (project_id) REFERENCES app.projects (id) ON DELETE CASCADE,
    CONSTRAINT routes_entry_fk FOREIGN KEY (entry_id) REFERENCES app.catalog_entries (id) ON DELETE CASCADE,
    CONSTRAINT routes_created_by_fk FOREIGN KEY (created_by) REFERENCES app.users (id),
    CONSTRAINT routes_hostname_path_unique UNIQUE (hostname, path_prefix),
    CONSTRAINT routes_hostname_chk CHECK (char_length(hostname) <= 253 AND hostname ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$' AND hostname !~ '\.[0-9]+$'),
    CONSTRAINT routes_path_chk CHECK (path_prefix = '/' OR (path_prefix ~ '^(/[a-z0-9._~-]+){1,16}$' AND path_prefix !~ '/\.\.?(/|$)')),
    CONSTRAINT routes_port_chk CHECK (port BETWEEN 1 AND 65535),
    CONSTRAINT routes_client_ip_chk CHECK (client_ip_from IS NULL OR client_ip_from = 'cloudflare'),
    CONSTRAINT routes_no_store_chk CHECK (cardinality(no_store_paths) <= 8)
);

CREATE INDEX routes_entry_idx ON app.routes (entry_id);
CREATE INDEX routes_project_idx ON app.routes (project_id);

CREATE TABLE app.host_edges (
    host_id uuid PRIMARY KEY,
    enabled boolean NOT NULL DEFAULT false,
    status text NOT NULL DEFAULT 'off',
    detail text NOT NULL DEFAULT '',
    config_hash text NOT NULL DEFAULT '',
    applied_at timestamptz,
    updated_at timestamptz NOT NULL,
    CONSTRAINT host_edges_host_fk FOREIGN KEY (host_id) REFERENCES app.hosts (id) ON DELETE CASCADE,
    CONSTRAINT host_edges_status_chk CHECK (status IN ('off', 'pending', 'applied', 'error')),
    CONSTRAINT host_edges_hash_chk CHECK (config_hash = '' OR config_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT host_edges_detail_len CHECK (char_length(detail) <= 300)
);

CREATE TABLE app.edge_confirmations (
    token_hash bytea PRIMARY KEY,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    project_id uuid,
    entry_id uuid,
    org_id uuid,
    domain_id uuid,
    body jsonb NOT NULL,
    request_hash bytea NOT NULL,
    idempotency_key text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    CONSTRAINT edge_confirmations_token_len CHECK (octet_length(token_hash) = 32),
    CONSTRAINT edge_confirmations_request_len CHECK (octet_length(request_hash) = 32),
    CONSTRAINT edge_confirmations_key_chk CHECK (idempotency_key ~ '^[A-Za-z0-9._:-]{1,128}$'),
    CONSTRAINT edge_confirmations_body_chk CHECK (jsonb_typeof(body) = 'object'),
    CONSTRAINT edge_confirmations_shape_chk CHECK (
        (kind = 'routes' AND project_id IS NOT NULL AND entry_id IS NOT NULL AND org_id IS NULL AND domain_id IS NULL)
        OR (kind = 'domain_delete' AND org_id IS NOT NULL AND domain_id IS NOT NULL AND project_id IS NULL AND entry_id IS NULL)
    ),
    CONSTRAINT edge_confirmations_user_fk FOREIGN KEY (user_id) REFERENCES app.users (id) ON DELETE CASCADE,
    CONSTRAINT edge_confirmations_project_fk FOREIGN KEY (project_id) REFERENCES app.projects (id) ON DELETE CASCADE,
    CONSTRAINT edge_confirmations_entry_fk FOREIGN KEY (entry_id) REFERENCES app.catalog_entries (id) ON DELETE CASCADE,
    CONSTRAINT edge_confirmations_org_fk FOREIGN KEY (org_id) REFERENCES app.orgs (id) ON DELETE CASCADE
);

-- Default privileges grant app CRUD. Narrow each new table to what the
-- server writes. Domains: status, checks and failures change, never the
-- name, org or token. Assignments and routes are replaced by delete and
-- insert. host_edges is an upsert of every state column. Plans update
-- used_at only, as the other confirmation tables.
REVOKE ALL ON TABLE app.org_domains FROM deploybot_api;
GRANT SELECT, INSERT, DELETE ON TABLE app.org_domains TO deploybot_api;
GRANT UPDATE (status, verified_at, last_check_at, failures) ON TABLE app.org_domains TO deploybot_api;

REVOKE ALL ON TABLE app.domain_assignments FROM deploybot_api;
GRANT SELECT, INSERT, DELETE ON TABLE app.domain_assignments TO deploybot_api;

REVOKE ALL ON TABLE app.routes FROM deploybot_api;
GRANT SELECT, INSERT, DELETE ON TABLE app.routes TO deploybot_api;

REVOKE ALL ON TABLE app.host_edges FROM deploybot_api;
GRANT SELECT, INSERT ON TABLE app.host_edges TO deploybot_api;
GRANT UPDATE (enabled, status, detail, config_hash, applied_at, updated_at) ON TABLE app.host_edges TO deploybot_api;

REVOKE ALL ON TABLE app.edge_confirmations FROM deploybot_api;
GRANT SELECT, INSERT, DELETE ON TABLE app.edge_confirmations TO deploybot_api;
GRANT UPDATE (used_at) ON TABLE app.edge_confirmations TO deploybot_api;

GRANT UPDATE (acme_email) ON TABLE app.orgs TO deploybot_api;
