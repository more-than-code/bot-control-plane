-- Hand-run rollback for 0035_domain_assignment_names.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0035 fully applied.
-- It never refuses. Assignments of a single name or a narrower wildcard
-- are dropped. Routes under them stay until their domain is deleted, but
-- no new route is accepted under them. Roll the API back to v1.51 first.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0035 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0035_domain_assignment_names.sql';

BEGIN;

DELETE FROM app.domain_assignments WHERE pattern NOT IN ('@', '*');
ALTER TABLE app.domain_assignments DROP CONSTRAINT IF EXISTS domain_assignments_pattern_chk;
ALTER TABLE app.domain_assignments ADD CONSTRAINT domain_assignments_pattern_chk CHECK (pattern IN ('@', '*'));

COMMIT;
