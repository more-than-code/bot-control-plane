-- Connect requests (api-v1 v1.31): an agent asks the person to Connect
-- GitHub for a project, and the requesting user approves it on the console
-- page /connect/<id>. Applied only by `bot-api-server --migrate` (SET ROLE
-- deploybot_owner). The matching rollback is
-- 0021_registry_connect_requests.down.sql. --migrate does not apply
-- *.down.sql.
--
-- There is no token column: the GitHub token goes straight into
-- app.registries when the request is connected, in the same transaction.
-- id_hash is the SHA-256 of the 43-character request id, as for value
-- requests. The id itself is never stored. reason is the note from the
-- agent (at most 200 characters, checked by the API). It is shown only on
-- the connect page. The registry is always ghcr.io, so it is not stored.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)
--
-- state is pending, connected or declined. "expired" is computed from
-- expires_at for a pending row and never stored. done_at is set when the
-- row leaves pending.
--
-- user_id and project_id cascade (api-v1 v1.31 item 8).

CREATE TABLE app.registry_connect_requests (
    id_hash bytea PRIMARY KEY,
    user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    reason text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'pending',
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    done_at timestamptz,
    CONSTRAINT registry_connect_requests_hash_len CHECK (octet_length(id_hash) = 32),
    CONSTRAINT registry_connect_requests_reason_len CHECK (char_length(reason) <= 200),
    CONSTRAINT registry_connect_requests_state_chk CHECK (state IN ('pending', 'connected', 'declined')),
    CONSTRAINT registry_connect_requests_done_chk CHECK ((state = 'pending') = (done_at IS NULL)),
    CONSTRAINT registry_connect_requests_user_fk FOREIGN KEY (user_id) REFERENCES app.users (id) ON DELETE CASCADE,
    CONSTRAINT registry_connect_requests_project_fk FOREIGN KEY (project_id) REFERENCES app.projects (id) ON DELETE CASCADE
);

-- The per-user open count (at most 20 pending, unexpired requests).
CREATE INDEX registry_connect_requests_open_idx
    ON app.registry_connect_requests (user_id, expires_at)
    WHERE state = 'pending';

-- The 7-day cleanup.
CREATE INDEX registry_connect_requests_expires_idx
    ON app.registry_connect_requests (expires_at);

-- Default privileges grant app CRUD. The server updates state, done_at and
-- expires_at (a new request for the same project ends the open one now)
-- and nothing else.
REVOKE ALL ON TABLE app.registry_connect_requests FROM deploybot_api;
GRANT SELECT, INSERT, DELETE ON TABLE app.registry_connect_requests TO deploybot_api;
GRANT UPDATE (state, done_at, expires_at) ON TABLE app.registry_connect_requests TO deploybot_api;
