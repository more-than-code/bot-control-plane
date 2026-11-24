-- When this server first served each Deploybot version, so the bell dates a
-- release notice by that time and not by its CHANGELOG date at midnight
-- (api-v1 v1.57, spec-release-first-served). Applied only by
-- `bot-api-server --migrate` (SET ROLE deploybot_owner). The matching
-- rollback is 0038_release_first_served.down.sql. --migrate does not apply
-- *.down.sql. (No semicolon or apostrophe in these comments: splitSQL does
-- not skip comments.)
--
-- release_first_served: one row per version, written by the API at startup
-- when its own version has no row. first_served_at never changes.

CREATE TABLE app.release_first_served (
    version text PRIMARY KEY,
    first_served_at timestamptz NOT NULL,
    CONSTRAINT release_first_served_version_chk CHECK (version ~ '^[0-9]{1,4}[.][0-9]{1,4}[.][0-9]{1,4}$')
);

REVOKE ALL ON TABLE app.release_first_served FROM deploybot_api;
GRANT SELECT, INSERT ON TABLE app.release_first_served TO deploybot_api;
