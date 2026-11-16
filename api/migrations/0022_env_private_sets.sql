-- Entry-private env sets (api-v1 v1.33 item 6). Applied only by
-- `bot-api-server --migrate` (SET ROLE deploybot_owner). The matching
-- rollback is 0022_env_private_sets.down.sql. --migrate does not apply
-- *.down.sql.
--
-- A private set belongs to one catalog entry. entry_id names that entry and
-- is null for a shared set. A private set is named with an at sign and the
-- entry name, for example @serve, so it can never collide with a shared set
-- name. The API creates it on first use and the foreign key deletes it with
-- its entry. Its values and value requests go with it through their own
-- cascades from app.env_sets. At most one private set per entry.
--
-- deploybot_api keeps SELECT, INSERT and DELETE on the table, which covers
-- the new column, and UPDATE on updated_at and updated_by only, so entry_id
-- is never changed after the insert.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)

ALTER TABLE app.env_sets ADD COLUMN entry_id uuid NULL;

ALTER TABLE app.env_sets
    ADD CONSTRAINT env_sets_entry_fk FOREIGN KEY (entry_id)
        REFERENCES app.catalog_entries (id) ON DELETE CASCADE;

CREATE UNIQUE INDEX env_sets_entry_idx
    ON app.env_sets (entry_id)
    WHERE entry_id IS NOT NULL;

ALTER TABLE app.env_sets DROP CONSTRAINT env_sets_name_chk;

ALTER TABLE app.env_sets
    ADD CONSTRAINT env_sets_name_chk CHECK (
        (entry_id IS NULL AND name ~ '^[a-z0-9][a-z0-9-]{0,47}$')
        OR (entry_id IS NOT NULL AND name ~ '^@[a-z0-9][a-z0-9-]{0,47}$')
    );
