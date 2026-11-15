-- Connect GitHub (api-v1 v1.29 item 9). Applied only by `bot-api-server
-- --migrate` (SET ROLE deploybot_owner). The matching rollback is
-- 0019_registry_source.down.sql. --migrate does not apply *.down.sql.
--
-- source says how a registry login was stored. manual is a username and
-- token typed into the console. github is a GitHub OAuth App token stored by
-- Connect GitHub for ghcr.io, with the GitHub login as the username.
-- Existing rows become manual. connected_by is the user who connected
-- GitHub, and is null for a manual row. The token stays sealed as before
-- and is never returned. The OAuth state lives in memory only, so there is
-- no table for it.
--
-- deploybot_api keeps SELECT, INSERT and DELETE on the table and gains
-- UPDATE on the two new columns, next to its other column grants.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)

ALTER TABLE app.registries
    ADD COLUMN source text NOT NULL DEFAULT 'manual'
    CONSTRAINT registries_source_chk CHECK (source IN ('manual', 'github'));

ALTER TABLE app.registries
    ADD COLUMN connected_by uuid NULL
    CONSTRAINT registries_connected_by_fk REFERENCES app.users (id) ON DELETE SET NULL;

GRANT UPDATE (source, connected_by) ON TABLE app.registries TO deploybot_api;
