-- Host names unique among live hosts only (api-v1 v1.38 item 1, spec
-- docs/spec-host-names.md rev 1). Applied only by
-- `bot-api-server --migrate` (SET ROLE deploybot_owner). The matching
-- rollback is 0025_host_names_live.down.sql. --migrate does not apply
-- *.down.sql.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)
--
-- The plain constraint hosts_org_name_unique (0015) counted revoked hosts,
-- so a revoked host kept its name forever and a re-enrolled host got a -2
-- suffix. The partial unique index below counts only hosts that are not
-- revoked. Nothing un-revokes a host, so a freed name never clashes again.
-- No data changes.

ALTER TABLE app.hosts DROP CONSTRAINT hosts_org_name_unique;

CREATE UNIQUE INDEX hosts_org_name_live_uidx ON app.hosts (org_id, name) WHERE revoked_at IS NULL;
