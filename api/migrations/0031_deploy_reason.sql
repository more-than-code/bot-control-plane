-- Why a deploy failed (api-v1 v1.47, spec docs/spec-deploy-reason.md).
-- Applied only by `bot-api-server --migrate` (SET ROLE deploybot_owner).
-- The matching rollback is 0031_deploy_reason.down.sql. --migrate does not
-- apply *.down.sql.
--
-- A finished deploy or rollback row keeps a short reason, the container
-- state the agent reported at the end (state, exit_code, restart_count) and
-- the redacted log tail the agent sent (at most 50 lines). Existing rows
-- keep null in all three, and the API answers a reason worked out from the
-- outcome for them.
--
-- outcome gains refused: the host refused the task (offline, policy,
-- unsupported), which used to delete the row. Such a row has no digest.
--
-- deploybot_api gains UPDATE on the new columns, next to its other column
-- grants.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)

ALTER TABLE app.image_deploys ADD COLUMN reason text NULL;
ALTER TABLE app.image_deploys ADD COLUMN final_status jsonb NULL;
ALTER TABLE app.image_deploys ADD COLUMN log_tail jsonb NULL;

ALTER TABLE app.image_deploys
    ADD CONSTRAINT image_deploys_reason_chk CHECK (reason IS NULL OR octet_length(reason) <= 1024);
ALTER TABLE app.image_deploys
    ADD CONSTRAINT image_deploys_final_status_chk CHECK (final_status IS NULL OR jsonb_typeof(final_status) = 'object');
ALTER TABLE app.image_deploys
    ADD CONSTRAINT image_deploys_log_tail_chk CHECK (
        log_tail IS NULL OR (jsonb_typeof(log_tail) = 'array' AND jsonb_array_length(log_tail) <= 50)
    );

ALTER TABLE app.image_deploys DROP CONSTRAINT image_deploys_outcome_chk;
ALTER TABLE app.image_deploys
    ADD CONSTRAINT image_deploys_outcome_chk CHECK (outcome IN (
        'in_flight', 'succeeded', 'unchanged', 'failed', 'timeout',
        'rolled_back', 'rollback_failed', 'image_unavailable', 'forbidden_spec', 'conflict',
        'auto_restarted', 'auto_rolled_back', 'auto_gave_up', 'auto_failed', 'refused'
    ));
ALTER TABLE app.image_deploys
    ADD CONSTRAINT image_deploys_refused_chk CHECK (outcome <> 'refused' OR (digest IS NULL AND via <> 'agent'));

GRANT UPDATE (reason, final_status, log_tail) ON TABLE app.image_deploys TO deploybot_api;
