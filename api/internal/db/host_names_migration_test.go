package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/migrations"
)

// hostNamesMigration is api-v1 v1.38 item 1 (spec-host-names 6).
const hostNamesMigration = "0025_host_names_live.sql"

// sqlBodies is splitSQL with the leading comment lines of each statement
// removed.
func sqlBodies(sql string) []string {
	var out []string
	for _, stmt := range splitSQL(sql) {
		var keep []string
		for _, line := range strings.Split(stmt, "\n") {
			if len(keep) == 0 && (strings.HasPrefix(strings.TrimSpace(line), "--") || strings.TrimSpace(line) == "") {
				continue
			}
			keep = append(keep, line)
		}
		out = append(out, strings.TrimSpace(strings.Join(keep, "\n")))
	}
	return out
}

// TestHostNamesMigrationText: only 0025 may drop hosts_org_name_unique, it
// replaces it with the live-host partial index, and its down script refuses
// shared names before it puts the constraint back.
func TestHostNamesMigrationText(t *testing.T) {
	if !numberedSQL(hostNamesMigration) || numberedSQL("0025_host_names_live.down.sql") {
		t.Fatal("0025 numbering")
	}
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") || name == hostNamesMigration {
			continue
		}
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		// 0015_orgs.down.sql removes the whole phase 1 schema, org_id
		// included, so it may drop the rule with IF EXISTS.
		if strings.Contains(string(body), "DROP CONSTRAINT hosts_org_name_unique") {
			t.Fatalf("%s drops the per-org name rule", name)
		}
	}
	body, err := migrations.FS.ReadFile(hostNamesMigration)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	stmts := sqlBodies(text)
	if len(stmts) != 2 ||
		stmts[0] != "ALTER TABLE app.hosts DROP CONSTRAINT hosts_org_name_unique" ||
		stmts[1] != "CREATE UNIQUE INDEX hosts_org_name_live_uidx ON app.hosts (org_id, name) WHERE revoked_at IS NULL" {
		t.Fatalf("0025 statements %q", stmts)
	}
	down, err := migrations.FS.ReadFile("0025_host_names_live.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{text, string(down)} {
		for _, line := range strings.Split(f, "\n") {
			if trim := strings.TrimSpace(line); strings.HasPrefix(trim, "--") && !strings.Contains(trim, "DELETE FROM app.schema_migrations") && strings.ContainsAny(trim, ";'") {
				t.Fatalf("comment contains a semicolon or an apostrophe: %s", trim)
			}
		}
	}
	ds := sqlBodies(string(down))
	if len(ds) != 5 || ds[0] != "BEGIN" || !strings.HasPrefix(ds[1], "DO $check$") || !strings.Contains(ds[1], "RAISE EXCEPTION") ||
		ds[2] != "DROP INDEX IF EXISTS app.hosts_org_name_live_uidx" ||
		ds[3] != "ALTER TABLE app.hosts ADD CONSTRAINT hosts_org_name_unique UNIQUE (org_id, name)" || ds[4] != "COMMIT" ||
		!strings.Contains(string(down), "DELETE FROM app.schema_migrations WHERE name = '"+hostNamesMigration+"'") {
		t.Fatalf("down script %q", ds)
	}
}

// TestHostNamesMigration is spec-host-names 7.5: up, a refused down while a
// name is shared (nothing changes), down, and up again. It runs in one
// transaction that is rolled back, so the test database keeps 0025.
func TestHostNamesMigration(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	lockMigrations(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d, err := Open(ctx, url)
	if err != nil {
		t.Fatal("connect failed")
	}
	defer d.Close()
	if !onTestDatabase(ctx, d) {
		t.Fatal("refusing a database that is not deploybot_test or deploybot_test_*")
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// A shared name: a revoked pi2 and the live pi2 enrolled after it.
	p := parity{t: t, st: d, ctx: ctx, at: time.Now().UTC().Truncate(time.Microsecond), tag: strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")}
	owner := p.user("mig25")
	org := p.org(owner.ID)
	old := p.enroll(org, nil, owner.ID, "pi2")
	if err := d.RevokeHost(ctx, old.ID, p.at); err != nil {
		t.Fatal(err)
	}
	if again := p.enroll(org, nil, owner.ID, "pi2"); again.Name != old.Name {
		t.Fatalf("re-enrolled %q, want %q", again.Name, old.Name)
	}

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal("connect failed")
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `SET ROLE deploybot_owner`); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	state := func() (index, constraint int) {
		t.Helper()
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname = 'app' AND indexname = 'hosts_org_name_live_uidx'`).Scan(&index); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conname = 'hosts_org_name_unique'`).Scan(&constraint); err != nil {
			t.Fatal(err)
		}
		return index, constraint
	}
	script := func(file string) []string {
		t.Helper()
		body, err := migrations.FS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, stmt := range sqlBodies(string(body)) {
			if stmt != "BEGIN" && stmt != "COMMIT" {
				out = append(out, stmt)
			}
		}
		return out
	}
	run := func(stmts []string) error {
		for _, stmt := range stmts {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	}
	if i, c := state(); i != 1 || c != 0 {
		t.Fatalf("after up: index %d constraint %d", i, c)
	}

	if _, err := tx.Exec(ctx, `SAVEPOINT refused`); err != nil {
		t.Fatal(err)
	}
	err = run(script("0025_host_names_live.down.sql"))
	if err == nil || !strings.Contains(err.Error(), "host names rollback") || !strings.Contains(err.Error(), old.Name) {
		t.Fatalf("down with a shared name: %v", err)
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT refused`); err != nil {
		t.Fatal(err)
	}
	if i, c := state(); i != 1 || c != 0 {
		t.Fatalf("refused down changed the schema: index %d constraint %d", i, c)
	}

	// Rename the revoked member of every shared pair (this test's and any
	// other test left), as spec-host-names 8 says, then the down runs.
	if _, err := tx.Exec(ctx, `
UPDATE app.hosts AS h
SET name = left(h.name, 40) || '-r' || left(h.id::text, 8)
WHERE h.revoked_at IS NOT NULL
  AND EXISTS (SELECT 1 FROM app.hosts AS o WHERE o.org_id = h.org_id AND o.name = h.name AND o.id <> h.id)
`); err != nil {
		t.Fatal(err)
	}
	if err := run(script("0025_host_names_live.down.sql")); err != nil {
		t.Fatalf("down: %v", err)
	}
	if i, c := state(); i != 0 || c != 1 {
		t.Fatalf("after down: index %d constraint %d", i, c)
	}
	if err := run(script(hostNamesMigration)); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if i, c := state(); i != 1 || c != 0 {
		t.Fatalf("after up again: index %d constraint %d", i, c)
	}
}
