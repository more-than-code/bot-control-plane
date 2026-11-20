-- Hand-run rollback for 0026_host_archive.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0026 fully applied.
-- It never refuses. Archived hosts then list again, as revoked hosts.
-- Nothing else is lost. Roll the API back to v1.39 first.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0026 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0026_host_archive.sql';

BEGIN;

ALTER TABLE app.hosts DROP CONSTRAINT IF EXISTS hosts_archived_revoked_chk;

ALTER TABLE app.hosts DROP COLUMN IF EXISTS archived_at;

COMMIT;
