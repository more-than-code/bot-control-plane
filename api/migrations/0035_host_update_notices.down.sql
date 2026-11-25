-- Hand-run rollback for 0035_host_update_notices.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0035 fully applied.
-- It never refuses. The noticed times are dropped: after a later up, each
-- pending host update is dated when the API next lists it. Roll the API back
-- to a build without 0035 first.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0035 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0035_host_update_notices.sql';

BEGIN;

DROP TABLE IF EXISTS app.host_update_notices;

COMMIT;
