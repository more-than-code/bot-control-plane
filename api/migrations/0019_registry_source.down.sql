-- Hand-run rollback for 0019_registry_source.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0019 fully applied.
-- Registry rows stay. A token stored by Connect GitHub then acts as a manual
-- ghcr.io login and keeps working until it is revoked at GitHub. Roll the
-- API back to v1.28 first (api-v1 v1.29 item 9).
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0019 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0019_registry_source.sql';

BEGIN;

ALTER TABLE app.registries DROP COLUMN IF EXISTS connected_by;
ALTER TABLE app.registries DROP COLUMN IF EXISTS source;

COMMIT;
