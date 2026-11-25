-- When the API first saw a host behind the latest bot-agent release, so the
-- bell dates an update notice by that time and not by the host last
-- check-in. Applied only by `bot-api-server --migrate` (SET ROLE
-- deploybot_owner). The matching rollback is
-- 0035_host_update_notices.down.sql. --migrate does not apply *.down.sql.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)
--
-- host_update_notices: one row per host and latest version, written the
-- first time the notifications list finds the host behind that version.
-- noticed_at never changes. Rows for an older latest version of the host
-- are deleted when a newer one is noticed.

CREATE TABLE app.host_update_notices (
    host_id uuid NOT NULL,
    version text NOT NULL,
    noticed_at timestamptz NOT NULL,
    PRIMARY KEY (host_id, version),
    CONSTRAINT host_update_notices_host_fk FOREIGN KEY (host_id) REFERENCES app.hosts (id) ON DELETE CASCADE,
    CONSTRAINT host_update_notices_version_chk CHECK (version ~ '^[0-9]{1,4}[.][0-9]{1,4}[.][0-9]{1,4}$')
);

REVOKE ALL ON TABLE app.host_update_notices FROM deploybot_api;
GRANT SELECT, INSERT, DELETE ON TABLE app.host_update_notices TO deploybot_api;
