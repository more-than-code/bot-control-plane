-- Organizations, phase 2 (api-v1 v1.26 item 3, spec-organizations §4.1 and §5).
-- Applied only by `deploybot-server api --migrate` (SET ROLE deploybot_owner).
-- Forward only: there is no down script. The rollback after this migration
-- is restoring the dump taken before it (spec §7). 0015_orgs.down.sql no
-- longer applies once this file has run.
--
-- Drops what phase 1 kept for a v0.2.29 rollback and the v1.26 API no longer
-- reads: host_shares (with its trigger and function), catalog_host_approvals,
-- hosts.project_id and the per-project host-name rule. hosts_org_name_unique
-- (the per-org name rule from 0015) stays.
--
-- join_tokens.project_id and service_confirmations.project_id are left as
-- they are on purpose (out of scope for v1.26, on the backlog).

-- Tell the operator what is dropped. Same-org shares already became
-- host_access rows in 0015. What is left here is the cross-org shares 0015
-- reported, plus the old rows of converted shares.
DO $report$
DECLARE
    shares bigint;
    cross_org bigint;
    approvals bigint;
    cross_rows text;
BEGIN
    SELECT count(*) INTO shares FROM app.host_shares;
    SELECT count(*) INTO cross_org
    FROM app.host_shares AS s
    JOIN app.hosts AS h ON h.id = s.host_id
    JOIN app.projects AS p ON p.id = s.project_id
    WHERE p.org_id <> h.org_id;
    SELECT count(*) INTO approvals FROM app.catalog_host_approvals;
    RAISE NOTICE 'org migration phase 2: dropping % host_shares rows (% cross-org, no access) and % catalog_host_approvals rows',
        shares, cross_org, approvals;
    IF cross_org > 0 THEN
        SELECT string_agg(s.host_id::text || ' -> ' || p.slug || ' (' || s.level || ')', ', ' ORDER BY s.host_id, p.slug) INTO cross_rows
        FROM app.host_shares AS s
        JOIN app.hosts AS h ON h.id = s.host_id
        JOIN app.projects AS p ON p.id = s.project_id
        WHERE p.org_id <> h.org_id;
        RAISE NOTICE 'org migration phase 2: dropping cross-org host shares: %', cross_rows;
    END IF;
END
$report$;

DROP TRIGGER host_shares_not_owner ON app.host_shares;
DROP FUNCTION app.host_shares_not_owner();
DROP TABLE app.host_shares;

DROP TABLE app.catalog_host_approvals;

ALTER TABLE app.hosts DROP CONSTRAINT hosts_project_name_unique;
ALTER TABLE app.hosts DROP COLUMN project_id;
