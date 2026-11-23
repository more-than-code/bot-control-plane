-- Hand-run rollback for 0034_deploy_task.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0034 fully applied.
-- It never refuses. Roll the API back to v1.49 first. Stored task ids are
-- dropped: a result that arrives later for such a row is then dropped as
-- unknown, as before v1.50.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0034 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0034_deploy_task.sql';

BEGIN;

DROP INDEX IF EXISTS app.image_deploys_task_uidx;
ALTER TABLE app.image_deploys DROP CONSTRAINT IF EXISTS image_deploys_task_chk;
ALTER TABLE app.image_deploys DROP COLUMN IF EXISTS task_action;
ALTER TABLE app.image_deploys DROP COLUMN IF EXISTS task_id;

COMMIT;
