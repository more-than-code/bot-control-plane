-- Hand-run rollback for 0029_host_files.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0029 fully applied.
-- It never refuses. Host file versions, apply history, the stored status
-- and host_file problems are dropped. Files already applied on hosts stay
-- as they are. Roll the API back to v1.43 first.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0029 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0029_host_files.sql';

BEGIN;

DELETE FROM app.problems WHERE kind = 'host_file';
ALTER TABLE app.problems DROP CONSTRAINT IF EXISTS problems_kind_chk;
ALTER TABLE app.problems ADD CONSTRAINT problems_kind_chk CHECK (kind IN ('host_offline', 'registry_login', 'deploy'));
ALTER TABLE app.hosts DROP COLUMN IF EXISTS host_files_status;
DROP TABLE IF EXISTS app.host_file_applies;
DROP TABLE IF EXISTS app.host_file_versions;

COMMIT;
