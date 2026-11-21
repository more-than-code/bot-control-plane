-- Host config files (api-v1 v1.44, spec docs/spec-host-config-files.md
-- rev 1). Applied only by `bot-api-server --migrate` (SET ROLE
-- deploybot_owner). The matching rollback is 0029_host_files.down.sql.
-- --migrate does not apply *.down.sql.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)
--
-- host_file_versions: every saved version of a root-owned file a host
-- declares in its runner.toml, per host and file name. A plain file keeps
-- its text and sha256. A secret file keeps only ciphertext (AAD
-- host-file|host|name|version) and no hash. The API keeps the newest 50
-- versions per file and never deletes the applied one.
--
-- host_file_applies: one row per apply result the agent returned. The
-- applied version of a file is the last row that succeeded or was
-- unchanged.
--
-- hosts.host_files_status: the latest host_files_status frame with its
-- time. It never holds content and, for a secret file, no size or hash.
--
-- problems gains the kind host_file.
--
-- deploybot_api may read, add and prune versions (never change one), add
-- and read apply rows (an append-only history), and update the status
-- column of hosts.

CREATE TABLE app.host_file_versions (
    host_id uuid NOT NULL,
    name text NOT NULL,
    version integer NOT NULL,
    secret boolean NOT NULL,
    size integer NOT NULL,
    sha256 text,
    content text,
    nonce bytea,
    content_cipher bytea,
    key_version integer,
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (host_id, name, version),
    CONSTRAINT host_file_versions_host_fk FOREIGN KEY (host_id) REFERENCES app.hosts (id) ON DELETE CASCADE,
    CONSTRAINT host_file_versions_user_fk FOREIGN KEY (created_by) REFERENCES app.users (id),
    CONSTRAINT host_file_versions_name_chk CHECK (name ~ '^[a-z][a-z0-9-]{0,31}$'),
    CONSTRAINT host_file_versions_version_chk CHECK (version >= 1),
    CONSTRAINT host_file_versions_size_chk CHECK (size BETWEEN 1 AND 65536),
    CONSTRAINT host_file_versions_form_chk CHECK (
        (
            NOT secret
            AND content IS NOT NULL
            AND octet_length(content) = size
            AND sha256 ~ '^[0-9a-f]{64}$'
            AND nonce IS NULL
            AND content_cipher IS NULL
            AND key_version IS NULL
        )
        OR (
            secret
            AND content IS NULL
            AND sha256 IS NULL
            AND octet_length(nonce) = 12
            AND octet_length(content_cipher) = size + 16
            AND key_version >= 1
        )
    )
);

CREATE TABLE app.host_file_applies (
    id uuid PRIMARY KEY,
    host_id uuid NOT NULL,
    name text NOT NULL,
    version integer NOT NULL,
    outcome text NOT NULL,
    stage text,
    reason text,
    took_ms integer,
    applied_by uuid NOT NULL,
    at timestamptz NOT NULL,
    CONSTRAINT host_file_applies_host_fk FOREIGN KEY (host_id) REFERENCES app.hosts (id) ON DELETE CASCADE,
    CONSTRAINT host_file_applies_user_fk FOREIGN KEY (applied_by) REFERENCES app.users (id),
    CONSTRAINT host_file_applies_name_chk CHECK (name ~ '^[a-z][a-z0-9-]{0,31}$'),
    CONSTRAINT host_file_applies_version_chk CHECK (version >= 1),
    CONSTRAINT host_file_applies_outcome_chk CHECK (outcome IN ('succeeded', 'unchanged', 'worker_busy', 'check_failed', 'rolled_back', 'rollback_failed', 'forbidden_spec')),
    CONSTRAINT host_file_applies_stage_chk CHECK (stage IS NULL OR char_length(stage) BETWEEN 1 AND 32),
    CONSTRAINT host_file_applies_reason_chk CHECK (reason IS NULL OR char_length(reason) BETWEEN 1 AND 70),
    CONSTRAINT host_file_applies_took_chk CHECK (took_ms IS NULL OR took_ms >= 0)
);

CREATE INDEX host_file_applies_file_idx ON app.host_file_applies (host_id, name, at);

ALTER TABLE app.hosts ADD COLUMN host_files_status jsonb NULL;

ALTER TABLE app.problems DROP CONSTRAINT problems_kind_chk;

ALTER TABLE app.problems ADD CONSTRAINT problems_kind_chk CHECK (kind IN ('host_offline', 'registry_login', 'deploy', 'host_file'));

REVOKE ALL ON TABLE app.host_file_versions FROM deploybot_api;
GRANT SELECT, INSERT, DELETE ON TABLE app.host_file_versions TO deploybot_api;
REVOKE ALL ON TABLE app.host_file_applies FROM deploybot_api;
GRANT SELECT, INSERT ON TABLE app.host_file_applies TO deploybot_api;
GRANT UPDATE (host_files_status) ON TABLE app.hosts TO deploybot_api;
