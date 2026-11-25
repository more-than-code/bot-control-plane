-- Hand-run rollback for 0039_problem_metrics_retention.sql. --migrate does not apply this file.
-- It runs in one transaction and never refuses. Every metrics_retention
-- problem is deleted, then the 0033 kind list comes back. Roll the API back
-- to a build without 0039 first, or its next sweep fails the kind check.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0039 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0039_problem_metrics_retention.sql';

BEGIN;

DELETE FROM app.problems WHERE kind = 'metrics_retention';
ALTER TABLE app.problems DROP CONSTRAINT problems_kind_chk;
ALTER TABLE app.problems
    ADD CONSTRAINT problems_kind_chk CHECK (kind IN ('host_offline', 'registry_login', 'deploy', 'host_file', 'agent_upgrade', 'host_resource'));

COMMIT;
