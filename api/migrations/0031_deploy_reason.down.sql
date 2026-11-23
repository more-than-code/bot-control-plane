-- Hand-run rollback for 0031_deploy_reason.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0031 fully applied.
-- Roll the API back to v1.46 first. refused rows are deleted (an older API
-- deleted them too), then the stored reasons, final states and log tails
-- are dropped.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0031 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0031_deploy_reason.sql';

BEGIN;

DELETE FROM app.image_deploys WHERE outcome = 'refused';

ALTER TABLE app.image_deploys DROP CONSTRAINT IF EXISTS image_deploys_refused_chk;
ALTER TABLE app.image_deploys DROP CONSTRAINT image_deploys_outcome_chk;
ALTER TABLE app.image_deploys
    ADD CONSTRAINT image_deploys_outcome_chk CHECK (outcome IN (
        'in_flight', 'succeeded', 'unchanged', 'failed', 'timeout',
        'rolled_back', 'rollback_failed', 'image_unavailable', 'forbidden_spec', 'conflict',
        'auto_restarted', 'auto_rolled_back', 'auto_gave_up', 'auto_failed'
    ));

ALTER TABLE app.image_deploys DROP COLUMN IF EXISTS log_tail;
ALTER TABLE app.image_deploys DROP COLUMN IF EXISTS final_status;
ALTER TABLE app.image_deploys DROP COLUMN IF EXISTS reason;

COMMIT;
