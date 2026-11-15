-- Hand-run rollback for 0017_env_generate.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0017 fully applied.
-- Pending env-set create plans are dropped (nothing was applied yet). The
-- reveal_pending, needs_value, and generated flags are dropped: a generated value that
-- was not revealed can no longer be revealed, its preview goes back to the
-- v1.13 rule, and a placeholder keeps its
-- empty secret value. Take a dump first. Roll the API back to v1.26 first
-- and replace any ${...} references with literal values (api-v1 v1.27 item 10).
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0017 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0017_env_generate.sql';

BEGIN;

DROP TABLE IF EXISTS app.env_set_confirmations;

ALTER TABLE app.env_values DROP CONSTRAINT IF EXISTS env_values_generated_chk;
ALTER TABLE app.env_values DROP CONSTRAINT IF EXISTS env_values_reveal_chk;
ALTER TABLE app.env_values DROP COLUMN IF EXISTS generated;
ALTER TABLE app.env_values DROP COLUMN IF EXISTS needs_value;
ALTER TABLE app.env_values DROP COLUMN IF EXISTS reveal_pending;

COMMIT;
