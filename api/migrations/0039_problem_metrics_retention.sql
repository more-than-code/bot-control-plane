-- The metrics_retention problem kind: the API hourly sweep opens it when a
-- TimescaleDB retention job of the metrics hypertables fails or stops running
-- (api-v1 v1.58, spec-retention-self-check rev 2). Applied only by
-- `bot-api-server --migrate` (SET ROLE deploybot_owner). The matching
-- rollback is 0039_problem_metrics_retention.down.sql. --migrate does not
-- apply *.down.sql. (No semicolon or apostrophe in these comments: splitSQL
-- does not skip comments.) No grant: deploybot_api already reads the
-- timescaledb_information views.

ALTER TABLE app.problems DROP CONSTRAINT problems_kind_chk;
ALTER TABLE app.problems
    ADD CONSTRAINT problems_kind_chk CHECK (kind IN ('host_offline', 'registry_login', 'deploy', 'host_file', 'agent_upgrade', 'host_resource', 'metrics_retention'));
