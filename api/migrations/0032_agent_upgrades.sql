-- Agent upgrades from the console or MCP (api-v1 v1.48, spec
-- docs/spec-agent-self-upgrade.md rev 1). Applied only by `bot-api-server
-- --migrate` (SET ROLE deploybot_owner). The matching rollback is
-- 0032_agent_upgrades.down.sql. --migrate does not apply *.down.sql.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)
--
-- host_agent_upgrades: one row per upgrade an org owner or admin started.
-- started while the API sends the task, running after the host accepted
-- it, then succeeded, rolled_back, failed or unknown with finished_at.
-- reason is the agent or API token, never free text. At most one row per
-- host is started or running (a partial unique index).
--
-- problems gains the kind agent_upgrade.
--
-- deploybot_api may read and add rows and update only the state, the
-- reason and finished_at.

CREATE TABLE app.host_agent_upgrades (
    id uuid PRIMARY KEY,
    host_id uuid NOT NULL,
    from_version text NOT NULL,
    to_version text NOT NULL,
    state text NOT NULL,
    reason text,
    started_by uuid NOT NULL,
    started_at timestamptz NOT NULL,
    finished_at timestamptz,
    CONSTRAINT host_agent_upgrades_host_fk FOREIGN KEY (host_id) REFERENCES app.hosts (id) ON DELETE CASCADE,
    CONSTRAINT host_agent_upgrades_user_fk FOREIGN KEY (started_by) REFERENCES app.users (id),
    CONSTRAINT host_agent_upgrades_from_chk CHECK (char_length(from_version) <= 64),
    CONSTRAINT host_agent_upgrades_to_chk CHECK (to_version ~ '^[0-9]{1,4}[.][0-9]{1,4}[.][0-9]{1,4}$'),
    CONSTRAINT host_agent_upgrades_state_chk CHECK (state IN ('started', 'running', 'succeeded', 'rolled_back', 'failed', 'unknown')),
    CONSTRAINT host_agent_upgrades_reason_chk CHECK (reason IS NULL OR reason ~ '^[a-z0-9_]{1,64}$'),
    CONSTRAINT host_agent_upgrades_finished_chk CHECK ((state IN ('started', 'running')) = (finished_at IS NULL))
);

CREATE INDEX host_agent_upgrades_host_idx ON app.host_agent_upgrades (host_id, started_at DESC);

CREATE UNIQUE INDEX host_agent_upgrades_open_uidx ON app.host_agent_upgrades (host_id) WHERE state IN ('started', 'running');

ALTER TABLE app.problems DROP CONSTRAINT problems_kind_chk;

ALTER TABLE app.problems ADD CONSTRAINT problems_kind_chk CHECK (kind IN ('host_offline', 'registry_login', 'deploy', 'host_file', 'agent_upgrade'));

REVOKE ALL ON TABLE app.host_agent_upgrades FROM deploybot_api;
GRANT SELECT, INSERT ON TABLE app.host_agent_upgrades TO deploybot_api;
GRANT UPDATE (state, reason, finished_at) ON TABLE app.host_agent_upgrades TO deploybot_api;
