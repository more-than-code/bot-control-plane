-- Hand-run rollback for 0027_problems.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0027 fully applied.
-- It never refuses. Only problem history is lost. Read marks for problem
-- keys become orphans and are pruned within 90 days.
-- Roll the API back to v1.40 first.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0027 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0027_problems.sql';

BEGIN;

DROP TABLE IF EXISTS app.problems;

COMMIT;
