-- Hand-run rollback for 0024_notification_reads.sql. --migrate does not apply this file.
-- It runs in one transaction, so a failure leaves 0024 fully applied.
-- It drops every read mark. Marks are disposable (spec-header-org-project-bell
-- section 7): after a later up, every notification shows as unread once.
-- Roll the API back to v1.36 first.
--
-- Run as deploybot_owner. After this script, delete the applied row before
-- --migrate will run 0024 again:
--
--   DELETE FROM app.schema_migrations WHERE name = '0024_notification_reads.sql';

BEGIN;

DROP TABLE IF EXISTS app.notification_reads;

COMMIT;
