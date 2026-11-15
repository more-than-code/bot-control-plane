-- Value requests (api-v1 v1.28): an agent asks for an env value and the
-- requesting user fills it on the console page /fill/<id>. Applied only by
-- `bot-api-server --migrate` (SET ROLE deploybot_owner). The matching
-- rollback is 0018_env_value_requests.down.sql. --migrate does not apply
-- *.down.sql.
--
-- There is no value column: a request names a key, and the value goes
-- straight into app.env_values when the request is filled. id_hash is the
-- SHA-256 of the 43-character request id, as for confirmation ids. The id
-- itself is never stored. reason is the note from the agent (at most 200
-- characters, checked by the API). It is shown only on the fill page.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)
--
-- state is pending, filled or declined. "expired" is computed from
-- expires_at for a pending row and never stored. done_at is set when the
-- row leaves pending.
--
-- user_id and project_id cascade, and so does the (project_id, set_name)
-- foreign key to app.env_sets: deleting a set or a project removes its
-- requests (api-v1 v1.28 item 7).

CREATE TABLE app.env_value_requests (
    id_hash bytea PRIMARY KEY,
    user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    set_name text NOT NULL,
    key text NOT NULL,
    reason text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'pending',
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    done_at timestamptz,
    CONSTRAINT env_value_requests_hash_len CHECK (octet_length(id_hash) = 32),
    CONSTRAINT env_value_requests_key_chk CHECK (key ~ '^[A-Z_][A-Z0-9_]{0,63}$'),
    CONSTRAINT env_value_requests_reason_len CHECK (char_length(reason) <= 200),
    CONSTRAINT env_value_requests_state_chk CHECK (state IN ('pending', 'filled', 'declined')),
    CONSTRAINT env_value_requests_done_chk CHECK ((state = 'pending') = (done_at IS NULL)),
    CONSTRAINT env_value_requests_user_fk FOREIGN KEY (user_id) REFERENCES app.users (id) ON DELETE CASCADE,
    CONSTRAINT env_value_requests_project_fk FOREIGN KEY (project_id) REFERENCES app.projects (id) ON DELETE CASCADE,
    CONSTRAINT env_value_requests_set_fk FOREIGN KEY (project_id, set_name)
        REFERENCES app.env_sets (project_id, name) ON DELETE CASCADE
);

-- The per-user open count (at most 20 pending, unexpired requests).
CREATE INDEX env_value_requests_open_idx
    ON app.env_value_requests (user_id, expires_at)
    WHERE state = 'pending';

-- The 7-day cleanup, and the cascade from a deleted env set.
CREATE INDEX env_value_requests_expires_idx
    ON app.env_value_requests (expires_at);
CREATE INDEX env_value_requests_set_idx
    ON app.env_value_requests (project_id, set_name);

-- Default privileges grant app CRUD. The server updates state, done_at and
-- expires_at (a new request for the same key ends the open one now) and
-- nothing else.
REVOKE ALL ON TABLE app.env_value_requests FROM deploybot_api;
GRANT SELECT, INSERT, DELETE ON TABLE app.env_value_requests TO deploybot_api;
GRANT UPDATE (state, done_at, expires_at) ON TABLE app.env_value_requests TO deploybot_api;
