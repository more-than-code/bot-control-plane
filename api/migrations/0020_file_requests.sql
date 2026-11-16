-- File requests (api-v1 v1.30): an agent asks for a managed file and the
-- requesting user adds it on the console page /fill-file/<id>. Applied only
-- by `bot-api-server --migrate` (SET ROLE deploybot_owner). The matching
-- rollback is 0020_file_requests.down.sql. --migrate does not apply
-- *.down.sql.
--
-- There is no content column: a request names a file, and the content goes
-- straight into app.project_files when the request is filled. id_hash is the
-- SHA-256 of the 43-character request id, as for value requests. The id
-- itself is never stored. secret is the flag the agent asked for (the person
-- may still make a plain request secret when filling it). reason is the note
-- from the agent (at most 200 characters, checked by the API). It is shown
-- only on the fill page.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)
--
-- state is pending, filled or declined. "expired" is computed from
-- expires_at for a pending row and never stored. done_at is set when the
-- row leaves pending.
--
-- user_id and project_id cascade: deleting a user or a project removes its
-- requests (api-v1 v1.30 item 7).

CREATE TABLE app.file_requests (
    id_hash bytea PRIMARY KEY,
    user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    name text NOT NULL,
    secret boolean NOT NULL DEFAULT true,
    reason text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'pending',
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    done_at timestamptz,
    CONSTRAINT file_requests_hash_len CHECK (octet_length(id_hash) = 32),
    CONSTRAINT file_requests_name_chk CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
    CONSTRAINT file_requests_reason_len CHECK (char_length(reason) <= 200),
    CONSTRAINT file_requests_state_chk CHECK (state IN ('pending', 'filled', 'declined')),
    CONSTRAINT file_requests_done_chk CHECK ((state = 'pending') = (done_at IS NULL)),
    CONSTRAINT file_requests_user_fk FOREIGN KEY (user_id) REFERENCES app.users (id) ON DELETE CASCADE,
    CONSTRAINT file_requests_project_fk FOREIGN KEY (project_id) REFERENCES app.projects (id) ON DELETE CASCADE
);

-- The per-user open count (at most 20 pending, unexpired file requests).
CREATE INDEX file_requests_open_idx
    ON app.file_requests (user_id, expires_at)
    WHERE state = 'pending';

-- The 7-day cleanup.
CREATE INDEX file_requests_expires_idx
    ON app.file_requests (expires_at);

-- The cascade from a deleted project, and the replace of an open request
-- for the same project and name when a user asks again.
CREATE INDEX file_requests_project_idx
    ON app.file_requests (project_id, name);

-- Default privileges grant app CRUD. The server updates state, done_at and
-- expires_at (a new request for the same name ends the open one now) and
-- nothing else. Granted in full from the start, as learned from 0018.
REVOKE ALL ON TABLE app.file_requests FROM deploybot_api;
GRANT SELECT, INSERT, DELETE ON TABLE app.file_requests TO deploybot_api;
GRANT UPDATE (state, done_at, expires_at) ON TABLE app.file_requests TO deploybot_api;
