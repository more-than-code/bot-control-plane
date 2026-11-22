-- Delete stale image versions (api-v1 v1.45, spec
-- docs/spec-image-version-delete.md). Applied only by `bot-api-server
-- --migrate` (SET ROLE deploybot_owner). The matching rollback is
-- 0030_registry_delete_packages.down.sql. --migrate does not apply
-- *.down.sql.
--
-- A github-source registry row records whether GitHub granted the
-- delete:packages scope on its last connect. It is false for a manual row,
-- for every existing row, and after a plain Connect GitHub. A token refresh
-- keeps the scopes, so it never touches this column. The API returns it as
-- can_delete on the registry object and never deletes a package version
-- without it.
--
-- deploybot_api gains UPDATE on the new column, next to its other column
-- grants. It already holds INSERT on the table.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)

ALTER TABLE app.registries
    ADD COLUMN delete_packages boolean NOT NULL DEFAULT false;

GRANT UPDATE (delete_packages) ON TABLE app.registries TO deploybot_api;
