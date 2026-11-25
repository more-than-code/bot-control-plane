-- Hand-run rollback for 0040_catalog_confirmations_expires_grant.sql. --migrate does not apply this file.
-- After it, a host-refused deploy uses up its plan again (the reopen is
-- denied and logged), which is safe: nothing else depends on the grant.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0040 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0040_catalog_confirmations_expires_grant.sql';

BEGIN;

REVOKE UPDATE (expires_at) ON TABLE app.catalog_confirmations FROM deploybot_api;

COMMIT;
