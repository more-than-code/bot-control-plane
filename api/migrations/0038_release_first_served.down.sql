-- Hand-run rollback for 0038_release_first_served.sql. --migrate does not apply this file.
-- It runs in one transaction and never refuses. The recorded times are
-- dropped: the bell then dates every release by its CHANGELOG date until the
-- API records its version again. Roll the API back to a build without 0038
-- first, or it records its version again at the next start after an up.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0038 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0038_release_first_served.sql';

BEGIN;

DROP TABLE IF EXISTS app.release_first_served;

COMMIT;
