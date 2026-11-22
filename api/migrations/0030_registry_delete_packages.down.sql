-- Hand-run rollback for 0030_registry_delete_packages.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0030 fully applied.
-- It never refuses. Registry rows stay. GitHub logins keep their token (and
-- its delete:packages scope at GitHub), but the API no longer offers image
-- version deletes. Roll the API back to v1.44 first.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0030 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0030_registry_delete_packages.sql';

BEGIN;

ALTER TABLE app.registries DROP COLUMN IF EXISTS delete_packages;

COMMIT;
