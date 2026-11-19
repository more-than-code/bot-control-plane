-- Notification read marks (api-v1 v1.37 item 3, spec
-- docs/spec-header-org-project-bell.md rev 1 option B). Applied only by
-- `bot-api-server --migrate` (SET ROLE deploybot_owner). The matching
-- rollback is 0024_notification_reads.down.sql. --migrate does not apply
-- *.down.sql.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)
--
-- notification_reads: one row per user and notification key the user
-- marked read. Notifications themselves are computed on each read and never
-- stored. A row is written only when something is marked read. The API
-- prunes the user marks older than 90 days whenever it writes marks.

CREATE TABLE app.notification_reads (
    user_id uuid NOT NULL,
    key text NOT NULL,
    read_at timestamptz NOT NULL,
    PRIMARY KEY (user_id, key),
    CONSTRAINT notification_reads_user_fk FOREIGN KEY (user_id) REFERENCES app.users (id) ON DELETE CASCADE,
    CONSTRAINT notification_reads_key_chk CHECK (char_length(key) BETWEEN 1 AND 200)
);

CREATE INDEX notification_reads_user_read_at_idx ON app.notification_reads (user_id, read_at);

-- Default privileges grant app CRUD. Marks are an upsert of read_at and a
-- prune by delete.
REVOKE ALL ON TABLE app.notification_reads FROM deploybot_api;
GRANT SELECT, INSERT, DELETE ON TABLE app.notification_reads TO deploybot_api;
GRANT UPDATE (read_at) ON TABLE app.notification_reads TO deploybot_api;
