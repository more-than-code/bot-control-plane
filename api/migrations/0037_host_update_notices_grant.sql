-- Fix for 0035 (v0.2.46): the API role needs UPDATE on
-- app.host_update_notices. NoticeHostUpdates locks the rows it deletes with
-- SELECT FOR UPDATE, and Postgres requires the UPDATE privilege for that, so
-- every notifications list failed with permission denied. The API never
-- updates these rows. Applied only by `bot-api-server --migrate` (SET ROLE
-- deploybot_owner). The matching rollback is
-- 0037_host_update_notices_grant.down.sql. --migrate does not apply
-- *.down.sql. (No semicolon or apostrophe in these comments: splitSQL does
-- not skip comments.) On us1 the same grant was applied by hand on
-- 2026-11-24, so this is a no-op there.

GRANT UPDATE ON TABLE app.host_update_notices TO deploybot_api;
