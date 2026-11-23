-- Hand-run rollback for 0036_domain_assignment_names.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0036 fully applied.
-- It never refuses. Assignments of a single name or a narrower wildcard
-- are dropped, and no new route is accepted under them. Routes one label
-- below a domain keep serving until that domain is deleted. Routes two or
-- more labels below every domain of their org are removed here, because
-- the older API cannot find them to stop them when the domain is deleted:
-- list them with the SELECT below first if they matter. Each host drops
-- them from its edge when it reconnects to the older API. Roll the API
-- back to v1.51 first.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0036 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0036_domain_assignment_names.sql';

BEGIN;

SELECT r.hostname, r.path_prefix, r.project_id FROM app.routes r
WHERE NOT EXISTS (
    SELECT 1 FROM app.org_domains d JOIN app.projects p ON p.org_id = d.org_id AND p.id = r.project_id
    WHERE r.hostname = d.name OR substr(r.hostname, strpos(r.hostname, '.') + 1) = d.name
);
DELETE FROM app.routes r
WHERE NOT EXISTS (
    SELECT 1 FROM app.org_domains d JOIN app.projects p ON p.org_id = d.org_id AND p.id = r.project_id
    WHERE r.hostname = d.name OR substr(r.hostname, strpos(r.hostname, '.') + 1) = d.name
);
DELETE FROM app.domain_assignments WHERE pattern NOT IN ('@', '*');
ALTER TABLE app.domain_assignments DROP CONSTRAINT IF EXISTS domain_assignments_pattern_chk;
ALTER TABLE app.domain_assignments ADD CONSTRAINT domain_assignments_pattern_chk CHECK (pattern IN ('@', '*'));

COMMIT;
