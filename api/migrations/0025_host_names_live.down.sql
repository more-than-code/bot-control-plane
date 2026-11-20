-- Hand-run rollback for 0025_host_names_live.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0025 fully applied.
-- It refuses, changing nothing, while any (org_id, name) pair is held by
-- more than one host, revoked or not: the plain constraint could not be
-- added back. The message names those hosts. Rename one of each pair first
-- (spec-host-names section 8), after rolling the API back to v1.37.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0025 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0025_host_names_live.sql';

BEGIN;

DO $check$
DECLARE
    shared text;
BEGIN
    SELECT string_agg(name || ' (org ' || org_id::text || ', ' || n::text || ' hosts)', ', ' ORDER BY org_id, name) INTO shared
    FROM (
        SELECT org_id, name, count(*) AS n
        FROM app.hosts
        GROUP BY org_id, name
        HAVING count(*) > 1
    ) AS dup;
    IF shared IS NOT NULL THEN
        RAISE EXCEPTION 'host names rollback: names held by more than one host: %. Rename one of each first', shared;
    END IF;
END
$check$;

DROP INDEX IF EXISTS app.hosts_org_name_live_uidx;

ALTER TABLE app.hosts ADD CONSTRAINT hosts_org_name_unique UNIQUE (org_id, name);

COMMIT;
