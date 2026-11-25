-- Fix for api-v1 v1.55 item 2: the API role needs UPDATE (expires_at) on
-- app.catalog_confirmations. ReopenConfirmation clears used_at and extends
-- expires_at when the host refused a confirmed deploy or rollback, and 0007
-- granted deploybot_api only UPDATE (used_at), so every reopen failed with
-- permission denied and the plan stayed used. The grant stays limited to
-- that column. Applied only by `bot-api-server --migrate` (SET ROLE
-- deploybot_owner). The matching rollback is
-- 0040_catalog_confirmations_expires_grant.down.sql. --migrate does not
-- apply *.down.sql. (No semicolon or apostrophe in these comments: splitSQL
-- does not skip comments.)

GRANT UPDATE (expires_at) ON TABLE app.catalog_confirmations TO deploybot_api;
