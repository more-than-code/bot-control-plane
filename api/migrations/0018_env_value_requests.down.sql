-- Hand-run rollback for 0018_env_value_requests.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0018 fully applied.
-- Open value requests are dropped: their links stop working. Filled values
-- stay in their env sets. Roll the API back to v1.27 first (api-v1 v1.28
-- item 8). Take a dump first.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0018 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0018_env_value_requests.sql';

BEGIN;

DROP TABLE IF EXISTS app.env_value_requests;

COMMIT;
