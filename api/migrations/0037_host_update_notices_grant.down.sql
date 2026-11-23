-- Hand-run rollback for 0037_host_update_notices_grant.sql. --migrate does not apply this file.
-- After it, the notifications list fails again on a build that has 0035, so
-- roll the API back to a build without 0035 first, or do not run this.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0037 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0037_host_update_notices_grant.sql';

BEGIN;

REVOKE UPDATE ON TABLE app.host_update_notices FROM deploybot_api;

COMMIT;
