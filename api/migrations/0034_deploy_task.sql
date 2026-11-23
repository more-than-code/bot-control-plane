-- Late deploy results (api-v1 v1.50 item 3, spec
-- docs/spec-deploybot-on-containers.md section 3.3). Applied only by
-- `bot-api-server --migrate` (SET ROLE deploybot_owner). The matching
-- rollback is 0034_deploy_task.down.sql. --migrate does not apply
-- *.down.sql.
--
-- A console or MCP deploy or rollback row keeps the id of the agent task
-- the API sent and which of the two it was, so a result that arrives after
-- the waiting API process ended (an API restart, another API process) can
-- settle the row from Postgres. Rows written before this migration, remove
-- rows and automatic rows keep null in both columns.
--
-- deploybot_api gains UPDATE on the two new columns only.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)

ALTER TABLE app.image_deploys ADD COLUMN task_id uuid NULL;
ALTER TABLE app.image_deploys ADD COLUMN task_action text NULL;

ALTER TABLE app.image_deploys
    ADD CONSTRAINT image_deploys_task_chk CHECK (
        (task_id IS NULL AND task_action IS NULL)
        OR (task_id IS NOT NULL AND task_action IN ('deploy', 'rollback'))
    );

CREATE UNIQUE INDEX image_deploys_task_uidx
    ON app.image_deploys (task_id)
    WHERE task_id IS NOT NULL;

GRANT UPDATE (task_id, task_action) ON TABLE app.image_deploys TO deploybot_api;
