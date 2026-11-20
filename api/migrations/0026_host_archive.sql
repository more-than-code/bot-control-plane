-- Remove a revoked host from the lists (api-v1 v1.40 item 4, spec
-- docs/spec-host-remove.md rev 1). Applied only by
-- `bot-api-server --migrate` (SET ROLE deploybot_owner). The matching
-- rollback is 0026_host_archive.down.sql. --migrate does not apply
-- *.down.sql.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)
--
-- archived_at is the time an org owner or admin removed the host from the
-- host lists and the project service lists. Only a revoked host can be
-- archived, which the check enforces. The row and its history stay.
-- deploybot_api already updates app.hosts table-wide, so no grant.
-- No data changes.

ALTER TABLE app.hosts ADD COLUMN archived_at timestamptz;

ALTER TABLE app.hosts ADD CONSTRAINT hosts_archived_revoked_chk CHECK (archived_at IS NULL OR revoked_at IS NOT NULL);
