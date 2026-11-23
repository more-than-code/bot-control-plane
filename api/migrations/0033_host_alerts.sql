-- Host resource alerts (api-v1 v1.49, spec docs/spec-host-resource-alerts.md
-- rev 1). Applied only by `bot-api-server --migrate` (SET ROLE
-- deploybot_owner). The matching rollback is 0033_host_alerts.down.sql.
-- --migrate does not apply *.down.sql.
-- (No semicolon or apostrophe in these comments: splitSQL does not skip
-- comments.)
--
-- host_alert_settings: at most one row per host. A null threshold uses the
-- default. A host without a row has alerts on with every default. The row
-- goes with its host.
--
-- problems gains the kind host_resource (memory, disk per mount, cpu and
-- load of a watched host), next to the 0032 kinds.
--
-- deploybot_api may read, add and change rows. It never deletes one: the
-- host cascade does.

CREATE TABLE app.host_alert_settings (
    host_id uuid PRIMARY KEY,
    alerts_enabled boolean NOT NULL DEFAULT true,
    mem_min_available_percent integer,
    disk_max_used_percent integer,
    cpu_max_busy_percent integer,
    updated_at timestamptz NOT NULL,
    updated_by uuid NOT NULL,
    CONSTRAINT host_alert_settings_host_fk FOREIGN KEY (host_id) REFERENCES app.hosts (id) ON DELETE CASCADE,
    CONSTRAINT host_alert_settings_user_fk FOREIGN KEY (updated_by) REFERENCES app.users (id),
    CONSTRAINT host_alert_settings_mem_chk CHECK (mem_min_available_percent IS NULL OR mem_min_available_percent BETWEEN 5 AND 50),
    CONSTRAINT host_alert_settings_disk_chk CHECK (disk_max_used_percent IS NULL OR disk_max_used_percent BETWEEN 50 AND 99),
    CONSTRAINT host_alert_settings_cpu_chk CHECK (cpu_max_busy_percent IS NULL OR cpu_max_busy_percent BETWEEN 50 AND 100)
);

ALTER TABLE app.problems DROP CONSTRAINT problems_kind_chk;
ALTER TABLE app.problems
    ADD CONSTRAINT problems_kind_chk CHECK (kind IN ('host_offline', 'registry_login', 'deploy', 'host_file', 'agent_upgrade', 'host_resource'));

REVOKE ALL ON TABLE app.host_alert_settings FROM deploybot_api;
GRANT SELECT, INSERT, UPDATE ON TABLE app.host_alert_settings TO deploybot_api;
