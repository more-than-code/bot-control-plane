-- Narrower domain assignments (api-v1 v1.52, spec
-- docs/spec-domain-assign-names.md rev 1). Applied only by `bot-api-server
-- --migrate` (SET ROLE deploybot_owner). The matching rollback is
-- 0035_domain_assignment_names.down.sql. --migrate does not apply
-- *.down.sql. (No semicolon or apostrophe in these comments: splitSQL does
-- not skip comments.)
--
-- domain_assignments.pattern was @ or * only. It may now also be a name
-- relative to the domain (marketingos, api.dev) or *. and a relative name
-- (*.dev). The API checks the full length. No row changes.

ALTER TABLE app.domain_assignments DROP CONSTRAINT domain_assignments_pattern_chk;
ALTER TABLE app.domain_assignments ADD CONSTRAINT domain_assignments_pattern_chk CHECK (
    pattern IN ('@', '*')
    OR (length(pattern) <= 253
        AND pattern ~ '^(\*\.)?[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$')
);
