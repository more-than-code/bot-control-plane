-- Hand-run rollback for 0020_file_requests.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0020 fully applied.
-- Open file requests are dropped: their links stop working. Filled files
-- stay in their projects. Roll the API back to v1.29 first (api-v1 v1.30
-- item 8). Take a dump first.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0020 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0020_file_requests.sql';

BEGIN;

DROP TABLE IF EXISTS app.file_requests;

COMMIT;
