-- Hand-run rollback for 0022_env_private_sets.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0022 fully applied.
-- It refuses while any private set exists: the first statement fails with a
-- check violation, and nothing changes. Delete those sets (or their entries)
-- first, after rolling the API back to v1.32 (api-v1 v1.33 item 6). Take a
-- dump first.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0022 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0022_env_private_sets.sql';

BEGIN;

ALTER TABLE app.env_sets ADD CONSTRAINT env_sets_no_private_chk CHECK (entry_id IS NULL);

ALTER TABLE app.env_sets DROP CONSTRAINT env_sets_name_chk;

ALTER TABLE app.env_sets DROP COLUMN IF EXISTS entry_id;

ALTER TABLE app.env_sets
    ADD CONSTRAINT env_sets_name_chk CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,47}$');

COMMIT;
