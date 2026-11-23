-- Hand-run rollback for 0032_agent_upgrades.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0032 fully applied.
-- It never refuses. Upgrade history and agent_upgrade problems are
-- dropped. Agents already upgraded stay as they are. Roll the API back to
-- v1.47 first.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0032 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0032_agent_upgrades.sql';

BEGIN;

DELETE FROM app.problems WHERE kind = 'agent_upgrade';
ALTER TABLE app.problems DROP CONSTRAINT IF EXISTS problems_kind_chk;
ALTER TABLE app.problems ADD CONSTRAINT problems_kind_chk CHECK (kind IN ('host_offline', 'registry_login', 'deploy', 'host_file'));
DROP INDEX IF EXISTS app.host_agent_upgrades_open_uidx;
DROP TABLE IF EXISTS app.host_agent_upgrades;

COMMIT;
