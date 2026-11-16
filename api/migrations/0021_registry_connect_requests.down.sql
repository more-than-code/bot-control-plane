-- Hand-run rollback for 0021_registry_connect_requests.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0021 fully applied.
-- Open connect requests are dropped: their links stop working. Logins that
-- were connected stay in app.registries. Roll the API back to v1.30 first
-- (api-v1 v1.31 item 9). Take a dump first.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0021 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0021_registry_connect_requests.sql';

BEGIN;

DROP TABLE IF EXISTS app.registry_connect_requests;

COMMIT;
