-- Hand-run rollback for 0033_host_alerts.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0033 fully applied.
-- It never refuses. host_resource problems and every host alert setting are
-- dropped. Roll the API back to v1.48 first. The kind check goes back to the 0032 list.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0033 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0033_host_alerts.sql';

BEGIN;

DELETE FROM app.problems WHERE kind = 'host_resource';
ALTER TABLE app.problems DROP CONSTRAINT IF EXISTS problems_kind_chk;
ALTER TABLE app.problems ADD CONSTRAINT problems_kind_chk CHECK (kind IN ('host_offline', 'registry_login', 'deploy', 'host_file', 'agent_upgrade'));
DROP TABLE IF EXISTS app.host_alert_settings;

COMMIT;
