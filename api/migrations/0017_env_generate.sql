-- Generated env values, show-once reveal, and env set creation with key
-- names only (api-v1 v1.27). Applied only by `bot-api-server --migrate`
-- (SET ROLE deploybot_owner). The matching rollback is
-- 0017_env_generate.down.sql. --migrate does not apply *.down.sql.
--
-- reveal_pending is true from a generated write until the one reveal, and
-- false after any other value write. Only a secret value can be pending.
-- needs_value marks a placeholder key that has no value yet. The first
-- value write clears it. generated is true while the stored value is one the
-- API generated, so its preview is always four bullets. A typed value write
-- clears it. Only a secret value can be generated. Existing rows get false
-- for all three.
--
-- env_set_confirmations is for phase B (POST /v1/projects/{id}/env-sets),
-- which is not implemented yet. Nothing reads or writes it until then. It
-- is created here so 0017 is the only v1.27 migration. It stores one
-- single-use delegated create plan. body
-- holds key names and generate kinds only, never a value. It is never
-- returned. user_id and project_id both ON DELETE CASCADE, as for
-- catalog_write_confirmations.

ALTER TABLE app.env_values ADD COLUMN reveal_pending boolean NOT NULL DEFAULT false;
ALTER TABLE app.env_values ADD COLUMN needs_value boolean NOT NULL DEFAULT false;
ALTER TABLE app.env_values ADD COLUMN generated boolean NOT NULL DEFAULT false;
ALTER TABLE app.env_values
    ADD CONSTRAINT env_values_reveal_chk CHECK (NOT reveal_pending OR is_secret);
ALTER TABLE app.env_values
    ADD CONSTRAINT env_values_generated_chk CHECK (NOT generated OR is_secret);

GRANT UPDATE (reveal_pending, needs_value, generated) ON TABLE app.env_values TO deploybot_api;

CREATE TABLE app.env_set_confirmations (
    token_hash bytea PRIMARY KEY,
    user_id uuid NOT NULL,
    project_id uuid NOT NULL,
    set_name text NOT NULL,
    body jsonb NOT NULL,
    request_hash bytea NOT NULL,
    idempotency_key text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    CONSTRAINT env_set_confirmations_token_len CHECK (octet_length(token_hash) = 32),
    CONSTRAINT env_set_confirmations_request_len CHECK (octet_length(request_hash) = 32),
    CONSTRAINT env_set_confirmations_name_chk CHECK (set_name ~ '^[a-z0-9][a-z0-9-]{0,47}$'),
    CONSTRAINT env_set_confirmations_key_chk CHECK (idempotency_key ~ '^[A-Za-z0-9._:-]{1,128}$'),
    CONSTRAINT env_set_confirmations_body_chk CHECK (jsonb_typeof(body) = 'object'),
    CONSTRAINT env_set_confirmations_user_fk FOREIGN KEY (user_id) REFERENCES app.users (id) ON DELETE CASCADE,
    CONSTRAINT env_set_confirmations_project_fk FOREIGN KEY (project_id) REFERENCES app.projects (id) ON DELETE CASCADE
);

-- Default privileges grant app CRUD. Narrow this table the same way as
-- app.catalog_write_confirmations: the server updates used_at and nothing else.
REVOKE ALL ON TABLE app.env_set_confirmations FROM deploybot_api;
GRANT SELECT, INSERT, DELETE ON TABLE app.env_set_confirmations TO deploybot_api;
GRANT UPDATE (used_at) ON TABLE app.env_set_confirmations TO deploybot_api;
