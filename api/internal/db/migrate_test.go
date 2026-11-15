package db

import (
	"os"
	"strings"
	"testing"

	"github.com/more-than-code/bot-control-plane/api/migrations"
)

func TestMigrationSkipsDownScript(t *testing.T) {
	if numberedSQL("0003_shared_hosts.down.sql") || numberedSQL("0004_service_names.down.sql") || numberedSQL("0005_mcp_oauth.down.sql") || numberedSQL("0006_invites.down.sql") || numberedSQL("0007_catalog.down.sql") || numberedSQL("0008_service_gone.down.sql") || numberedSQL("0009_catalog_images.down.sql") || numberedSQL("0010_project_files.down.sql") || numberedSQL("0011_service_confirmations.down.sql") || numberedSQL("0012_revoked_host_rows.down.sql") || numberedSQL("0013_catalog_write_confirmations.down.sql") || numberedSQL("0014_auto_actions.down.sql") || numberedSQL("0015_orgs.down.sql") || numberedSQL("0017_env_generate.down.sql") || numberedSQL("0018_env_value_requests.down.sql") || numberedSQL("0019_registry_source.down.sql") {
		t.Fatal("down script is numbered SQL")
	}
	if !numberedSQL("0003_shared_hosts.sql") || !numberedSQL("0004_service_names.sql") || !numberedSQL("0005_mcp_oauth.sql") || !numberedSQL("0006_invites.sql") || !numberedSQL("0007_catalog.sql") || !numberedSQL("0008_service_gone.sql") || !numberedSQL("0009_catalog_images.sql") || !numberedSQL("0010_project_files.sql") || !numberedSQL("0011_service_confirmations.sql") || !numberedSQL("0012_revoked_host_rows.sql") || !numberedSQL("0013_catalog_write_confirmations.sql") || !numberedSQL("0014_auto_actions.sql") || !numberedSQL("0015_orgs.sql") || !numberedSQL("0016_orgs_phase2.sql") || !numberedSQL("0017_env_generate.sql") || !numberedSQL("0018_env_value_requests.sql") || !numberedSQL("0019_registry_source.sql") {
		t.Fatal("numbered migration was skipped")
	}
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(names, ",")
	for _, want := range []string{"0001_app.sql", "0002_metrics.sql", "0003_shared_hosts.sql", "0004_service_names.sql", "0005_mcp_oauth.sql", "0006_invites.sql", "0007_catalog.sql", "0008_service_gone.sql", "0009_catalog_images.sql", "0010_project_files.sql", "0011_service_confirmations.sql", "0012_revoked_host_rows.sql", "0013_catalog_write_confirmations.sql", "0014_auto_actions.sql", "0015_orgs.sql", "0016_orgs_phase2.sql", "0017_env_generate.sql", "0018_env_value_requests.sql", "0019_registry_source.sql"} {
		if !strings.Contains(got, want) {
			t.Fatalf("migrations = %s", got)
		}
	}
	if strings.Contains(got, ".down.sql") {
		t.Fatalf("down script would be applied: %s", got)
	}
}

func TestCatalogImagesMigrationSplits(t *testing.T) {
	body, err := migrations.FS.ReadFile("0009_catalog_images.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "--") && strings.Contains(trim, ";") {
			t.Fatalf("comment contains a semicolon: %s", trim)
		}
	}
	for _, stmt := range splitSQL(text) {
		if !meaningfulSQL(stmt) {
			t.Fatalf("comment-only statement: %q", stmt)
		}
	}
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		raw, err := migrations.FS.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "chr(0)") {
			t.Fatalf("%s uses chr(0)", entry.Name())
		}
	}
}

func TestSplitSQLKeepsFunctionBody(t *testing.T) {
	body, err := migrations.FS.ReadFile("0003_shared_hosts.sql")
	if err != nil {
		t.Fatal(err)
	}
	stmts := splitSQL(string(body))
	var fn string
	for _, stmt := range stmts {
		trimmed := strings.TrimSpace(stmt)
		if trimmed == "RETURN NEW" || strings.HasPrefix(trimmed, "END") {
			t.Fatalf("function body was split: %q", trimmed)
		}
		if strings.Contains(stmt, "CREATE FUNCTION") {
			fn = stmt
		}
	}
	if !strings.Contains(fn, "RETURN NEW") || !strings.Contains(fn, "CONSTRAINT = 'host_shares_not_owner'") {
		t.Fatalf("function statement: %s", fn)
	}
	parts := splitSQL("SELECT $1;\nSELECT 2;")
	if len(parts) != 2 || !strings.Contains(parts[0], "$1") {
		t.Fatalf("dollar param split: %#v", parts)
	}
}

func TestSharedHostsMigrationText(t *testing.T) {
	assertSharedHostsMigration(t)
}

func TestServiceNamesMigrationText(t *testing.T) {
	body, err := migrations.FS.ReadFile("0004_service_names.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"ADD COLUMN display_name text NULL",
		"ADD COLUMN owner_user text NULL",
		"CREATE UNIQUE INDEX services_host_display_name_uidx",
		"ON app.services (host_id, lower(display_name))",
		"WHERE display_name IS NOT NULL",
		"CREATE INDEX IF NOT EXISTS services_project_idx ON app.services (project_id)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("0004 missing %s", want)
		}
	}
	if strings.Contains(text, "GRANT") {
		t.Fatal("0004 changes grants")
	}
	down, err := migrations.FS.ReadFile("0004_service_names.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downText := string(down)
	for _, want := range []string{
		"--migrate does not apply",
		"Names typed by people are lost",
		"DELETE FROM app.schema_migrations WHERE name = '0004_service_names.sql'",
		"DROP INDEX IF EXISTS app.services_host_display_name_uidx",
		"DROP COLUMN IF EXISTS display_name",
		"DROP COLUMN IF EXISTS owner_user",
	} {
		if !strings.Contains(downText, want) {
			t.Fatalf("down script missing %s", want)
		}
	}
	if strings.Contains(downText, "services_project_idx") {
		t.Fatal("down script mentions the 0003 project index")
	}
}

func TestServiceGoneMigrationText(t *testing.T) {
	body, err := migrations.FS.ReadFile("0008_service_gone.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"ADD COLUMN gone_at timestamptz NULL",
		"SET gone_at = s.last_seen_at",
		"peer.host_id = s.host_id",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("0008 missing %s", want)
		}
	}
	if strings.Contains(text, "GRANT") {
		t.Fatal("0008 changes grants")
	}
	alterAt := strings.Index(text, "ADD COLUMN gone_at")
	updateAt := strings.Index(text, "UPDATE app.services AS s")
	if alterAt < 0 || updateAt < 0 || alterAt > updateAt {
		t.Fatal("backfill UPDATE must run after gone_at is added")
	}
	down, err := migrations.FS.ReadFile("0008_service_gone.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downText := string(down)
	for _, want := range []string{
		"--migrate does not apply",
		"DELETE FROM app.schema_migrations WHERE name = '0008_service_gone.sql'",
		"DROP COLUMN IF EXISTS gone_at",
	} {
		if !strings.Contains(downText, want) {
			t.Fatalf("down script missing %s", want)
		}
	}
}

func TestRevokedHostRowsMigrationText(t *testing.T) {
	body, err := migrations.FS.ReadFile("0012_revoked_host_rows.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	stmts := splitSQL(text)
	if len(stmts) != 1 {
		t.Fatalf("statements %d", len(stmts))
	}
	stmt := stmts[0]
	for _, want := range []string{
		"UPDATE app.services AS s",
		"SET gone_at = h.revoked_at",
		"FROM app.hosts AS h",
		"s.host_id = h.id",
		"h.revoked_at IS NOT NULL",
		"s.gone_at IS NULL",
	} {
		if !strings.Contains(stmt, want) {
			t.Fatalf("0012 missing %s", want)
		}
	}
	if strings.Contains(text, "ALTER") || strings.Contains(text, "GRANT") || strings.Contains(text, "CREATE") {
		t.Fatal("0012 changes schema or grants")
	}
	down, err := migrations.FS.ReadFile("0012_revoked_host_rows.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downText := string(down)
	for _, want := range []string{
		"--migrate does not apply",
		"No-op",
		"DELETE FROM app.schema_migrations WHERE name = '0012_revoked_host_rows.sql'",
	} {
		if !strings.Contains(downText, want) {
			t.Fatalf("down script missing %s", want)
		}
	}
	if got := splitSQL(downText); len(got) != 0 {
		t.Fatalf("down script is not a no-op: %v", got)
	}
}

// assertSharedHostsMigration checks the embedded 0003 script. The column is
// already NOT NULL after migrate, so a live NULL backfill cannot be re-run.
func assertSharedHostsMigration(t *testing.T) {
	t.Helper()
	body, err := migrations.FS.ReadFile("0003_shared_hosts.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	updateAt := strings.Index(text, "UPDATE app.services AS s")
	notNullAt := strings.Index(text, "ALTER COLUMN project_id SET NOT NULL")
	if updateAt < 0 || notNullAt < 0 || updateAt > notNullAt {
		t.Fatal("backfill UPDATE must run before project_id is NOT NULL")
	}
	stmt := sharedHostsBackfillSQL(t)
	if !strings.Contains(stmt, "SET project_id = h.project_id") || !strings.Contains(stmt, "h.id = s.host_id") {
		t.Fatalf("backfill statement %q", stmt)
	}
	if !strings.Contains(text, "REFERENCES app.hosts (id) ON DELETE CASCADE") {
		t.Fatal("host_shares.host_id is missing ON DELETE CASCADE")
	}
	if !strings.Contains(text, "GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE app.host_shares TO deploybot_api") {
		t.Fatal("missing deploybot_api grants on app.host_shares")
	}
	down, err := migrations.FS.ReadFile("0003_shared_hosts.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downText := string(down)
	if !strings.Contains(downText, "DELETE FROM app.schema_migrations WHERE name = '0003_shared_hosts.sql'") {
		t.Fatal("down script does not document the schema_migrations delete")
	}
	if !strings.Contains(downText, "--migrate does not apply") {
		t.Fatal("down script does not say --migrate skips it")
	}
}

func TestOAuthMigrationText(t *testing.T) {
	body, err := migrations.FS.ReadFile("0005_mcp_oauth.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"CREATE TABLE app.oauth_clients",
		"CREATE TABLE app.oauth_grants",
		"CREATE TABLE app.oauth_requests",
		"CREATE TABLE app.oauth_codes",
		"'deploybot-cli'",
		"ADD COLUMN audience text NULL",
		"ADD COLUMN grant_id uuid NULL",
		"ADD COLUMN client_id text NULL",
		"ADD COLUMN code_id uuid NULL",
		"ADD COLUMN rotated_at timestamptz NULL",
		"kind IN ('console', 'mcp', 'mcp_access', 'mcp_refresh', 'api_delegated')",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE app.oauth_clients TO deploybot_api",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE app.oauth_grants TO deploybot_api",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE app.oauth_requests TO deploybot_api",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE app.oauth_codes TO deploybot_api",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("0005 missing %s", want)
		}
	}
	stmt := mcpCutoverSQL(t)
	if !strings.Contains(stmt, "SET expires_at = now()") || !strings.Contains(stmt, "kind = 'mcp'") {
		t.Fatalf("cut-over statement %q", stmt)
	}
	kindAt := strings.Index(text, "sessions_kind_chk")
	updateAt := strings.Index(text, "SET expires_at = now()")
	if kindAt < 0 || updateAt < 0 || kindAt > updateAt {
		t.Fatal("cut-over UPDATE must run after the kind check allows mcp")
	}
	down, err := migrations.FS.ReadFile("0005_mcp_oauth.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downText := string(down)
	for _, want := range []string{
		"--migrate does not apply",
		"DELETE FROM app.schema_migrations WHERE name = '0005_mcp_oauth.sql'",
		"DELETE FROM app.sessions WHERE kind NOT IN ('console', 'mcp')",
		"DROP TABLE app.oauth_codes",
		"DROP TABLE app.oauth_clients",
		"kind IN ('console', 'mcp')",
	} {
		if !strings.Contains(downText, want) {
			t.Fatalf("down script missing %s", want)
		}
	}
}

// mcpCutoverSQL is the UPDATE in 0005 that expires legacy mcp sessions.
func mcpCutoverSQL(t *testing.T) string {
	t.Helper()
	body, err := migrations.FS.ReadFile("0005_mcp_oauth.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitSQL(string(body)) {
		if strings.Contains(stmt, "SET expires_at = now()") && strings.Contains(stmt, "kind = 'mcp'") {
			return stmt
		}
	}
	t.Fatal("cut-over statement not found in 0005_mcp_oauth.sql")
	return ""
}

// sharedHostsBackfillSQL is the UPDATE in 0003 that copies hosts.project_id
// onto services. Callers run this text; they do not keep a second copy.
func sharedHostsBackfillSQL(t *testing.T) string {
	t.Helper()
	body, err := migrations.FS.ReadFile("0003_shared_hosts.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitSQL(string(body)) {
		if strings.Contains(stmt, "SET project_id = h.project_id") && strings.Contains(stmt, "h.id = s.host_id") {
			return stmt
		}
	}
	t.Fatal("backfill statement not found in 0003_shared_hosts.sql")
	return ""
}

func TestInviteMigrationText(t *testing.T) {
	body, err := migrations.FS.ReadFile("0006_invites.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"CREATE TABLE app.invites",
		"ON DELETE CASCADE",
		"CHECK (role IN ('admin', 'viewer'))",
		"CHECK (max_uses BETWEEN 1 AND 50)",
		"CHECK (uses BETWEEN 0 AND max_uses)",
		"CHECK (octet_length(token_hash) = 32)",
		"CREATE INDEX invites_project_idx ON app.invites (project_id)",
		"REVOKE ALL ON TABLE app.invites FROM deploybot_api",
		"GRANT SELECT, INSERT ON TABLE app.invites TO deploybot_api",
		"GRANT UPDATE (uses, revoked_at) ON TABLE app.invites TO deploybot_api",
		"SECURITY DEFINER",
		"SET search_path = pg_catalog, app",
		"REVOKE ALL ON FUNCTION app.invites_delete_for_project(uuid) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION app.invites_delete_for_project(uuid) TO deploybot_api",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("0006 missing %s", want)
		}
	}
	if strings.Contains(text, "GRANT UPDATE, DELETE") || strings.Contains(text, "GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE app.invites") {
		t.Fatal("0006 grants deploybot_api more than select, insert, and two columns")
	}
	fn := 0
	for _, stmt := range splitSQL(text) {
		if strings.Contains(stmt, "CREATE FUNCTION app.invites_delete_for_project") {
			fn++
			if !strings.Contains(stmt, "DELETE FROM app.invites WHERE project_id = p_project_id") {
				t.Fatal("function body was split")
			}
		}
		if strings.TrimSpace(stmt) == "DELETE FROM app.invites WHERE project_id = p_project_id" {
			t.Fatal("function body was split into its own statement")
		}
	}
	if fn != 1 {
		t.Fatalf("function statements %d", fn)
	}
	down, err := migrations.FS.ReadFile("0006_invites.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downText := string(down)
	for _, want := range []string{
		"--migrate does not apply",
		"DELETE FROM app.schema_migrations WHERE name = '0006_invites.sql'",
		"DROP FUNCTION IF EXISTS app.invites_delete_for_project(uuid)",
		"DROP TABLE IF EXISTS app.invites",
	} {
		if !strings.Contains(downText, want) {
			t.Fatalf("down script missing %s", want)
		}
	}
	if strings.Index(downText, "DROP FUNCTION") > strings.Index(downText, "DROP TABLE") {
		t.Fatal("down script drops the table before the function")
	}
}

// TestMigrateUsesOneConnection guards the org mapping: 0015 reads the
// session setting deploybot.org_mapping, which exists only on the one
// connection that MigrateWith opens. A pool would lose it between files.
func TestMigrateUsesOneConnection(t *testing.T) {
	raw, err := os.ReadFile("migrate.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if !strings.Contains(src, "pgx.ConnectConfig(ctx, cfg)") || strings.Contains(src, "pgxpool") {
		t.Fatal("Migrate must apply every file on one pgx.Connect connection, not a pool")
	}
	if strings.Count(src, "pgx.Connect") != 1 {
		t.Fatal("Migrate opens more than one connection")
	}
	if !strings.Contains(src, "pg_advisory_lock($1)") {
		t.Fatal("Migrate no longer serializes runs with an advisory lock")
	}
	body, err := migrations.FS.ReadFile("0015_orgs.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "current_setting('deploybot.org_mapping', true)") || !strings.Contains(string(body), "DROP TABLE app.org_migration_mapping") {
		t.Fatal("0015 no longer reads the mapping table")
	}
	for _, stmt := range splitSQL(string(body)) {
		if !meaningfulSQL(stmt) {
			t.Fatalf("comment-only statement: %q", stmt)
		}
	}
}

func TestParseOrgMapping(t *testing.T) {
	good := `{"require_all_projects":true,"orgs":[
	{"slug":"more-than-code","name":"More Than Code","owner_email":"o@example.com","projects":["deploybot","daikou","defect-drainer"]},
	{"slug":"saasifyease","name":"SaasifyEase","owner_email":"o@example.com","projects":["mohiguide","ai-hub","ai-hub-uat"]}]}`
	m, err := ParseOrgMapping([]byte(good))
	if err != nil || !m.RequireAllProjects || len(m.Orgs) != 2 || len(m.Orgs[0].Projects) != 3 {
		t.Fatalf("good mapping %+v %v", m, err)
	}
	bad := map[string]string{
		"unknown key":     `{"orgs":[],"extra":1}`,
		"no orgs":         `{"orgs":[]}`,
		"bad slug":        `{"orgs":[{"slug":"Bad","name":"X","owner_email":"o@example.com","projects":["a"]}]}`,
		"personal prefix": `{"orgs":[{"slug":"u-abc","name":"X","owner_email":"o@example.com","projects":["a"]}]}`,
		"dup org":         `{"orgs":[{"slug":"a","name":"X","owner_email":"o@example.com","projects":["a"]},{"slug":"a","name":"Y","owner_email":"o@example.com","projects":["b"]}]}`,
		"empty name":      `{"orgs":[{"slug":"a","name":" ","owner_email":"o@example.com","projects":["a"]}]}`,
		"bad email":       `{"orgs":[{"slug":"a","name":"X","owner_email":"nobody","projects":["a"]}]}`,
		"no projects":     `{"orgs":[{"slug":"a","name":"X","owner_email":"o@example.com","projects":[]}]}`,
		"bad project":     `{"orgs":[{"slug":"a","name":"X","owner_email":"o@example.com","projects":["Bad Slug"]}]}`,
		"project twice":   `{"orgs":[{"slug":"a","name":"X","owner_email":"o@example.com","projects":["p"]},{"slug":"b","name":"Y","owner_email":"o@example.com","projects":["p"]}]}`,
		"trailing":        good + ` {}`,
	}
	for name, raw := range bad {
		if _, err := ParseOrgMapping([]byte(raw)); err == nil {
			t.Fatalf("%s accepted", name)
		} else if strings.Contains(err.Error(), "o@example.com") {
			t.Fatalf("%s error leaks the owner email: %v", name, err)
		}
	}
	if _, err := LoadOrgMapping(t.TempDir() + "/missing.json"); err == nil {
		t.Fatal("missing file accepted")
	}
}

// TestOrgsPhase2MigrationText is api-v1 v1.26 item 3: 0016 drops the legacy
// objects, keeps the per-org name rule, and has no down script.
func TestOrgsPhase2MigrationText(t *testing.T) {
	body, err := migrations.FS.ReadFile("0016_orgs_phase2.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"DROP TRIGGER host_shares_not_owner ON app.host_shares",
		"DROP FUNCTION app.host_shares_not_owner()",
		"DROP TABLE app.host_shares",
		"DROP TABLE app.catalog_host_approvals",
		"ALTER TABLE app.hosts DROP CONSTRAINT hosts_project_name_unique",
		"ALTER TABLE app.hosts DROP COLUMN project_id",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("0016 lacks %q", want)
		}
	}
	if strings.Contains(text, "DROP CONSTRAINT hosts_org_name_unique") {
		t.Fatal("0016 drops the per-org name rule")
	}
	if _, err := migrations.FS.ReadFile("0016_orgs_phase2.down.sql"); err == nil {
		t.Fatal("0016 is forward only")
	}
	// splitSQL does not know comments: a semicolon in one splits the file,
	// and an apostrophe opens a quote that swallows the next statements.
	for _, line := range strings.Split(text, "\n") {
		if trim := strings.TrimSpace(line); strings.HasPrefix(trim, "--") && strings.ContainsAny(trim, ";'") {
			t.Fatalf("comment contains a semicolon or an apostrophe: %s", trim)
		}
	}
	// The report block, the trigger, the function, two tables and two
	// hosts changes.
	if stmts := splitSQL(text); len(stmts) != 7 {
		t.Fatalf("0016 splits into %d statements, want 7: %q", len(stmts), stmts)
	}
}
