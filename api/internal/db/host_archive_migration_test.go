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

// hostArchiveMigration is api-v1 v1.40 item 4 (spec-host-remove 4).
const hostArchiveMigration = "0026_host_archive.sql"

// TestHostArchiveMigrationText: 0026 adds the column and the check, and its
// down script drops both, never refusing.
func TestHostArchiveMigrationText(t *testing.T) {
	if !numberedSQL(hostArchiveMigration) || numberedSQL("0026_host_archive.down.sql") {
		t.Fatal("0026 numbering")
	}
	body, err := migrations.FS.ReadFile(hostArchiveMigration)
	if err != nil {
		t.Fatal(err)
	}
	stmts := sqlBodies(string(body))
	if len(stmts) != 2 ||
		stmts[0] != "ALTER TABLE app.hosts ADD COLUMN archived_at timestamptz" ||
		stmts[1] != "ALTER TABLE app.hosts ADD CONSTRAINT hosts_archived_revoked_chk CHECK (archived_at IS NULL OR revoked_at IS NOT NULL)" {
		t.Fatalf("0026 statements %q", stmts)
	}
	down, err := migrations.FS.ReadFile("0026_host_archive.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{string(body), string(down)} {
		for _, line := range strings.Split(f, "\n") {
			if trim := strings.TrimSpace(line); strings.HasPrefix(trim, "--") && !strings.Contains(trim, "DELETE FROM app.schema_migrations") && strings.ContainsAny(trim, ";'") {
				t.Fatalf("comment contains a semicolon or an apostrophe: %s", trim)
			}
		}
	}
	ds := sqlBodies(string(down))
	if len(ds) != 4 || ds[0] != "BEGIN" ||
		ds[1] != "ALTER TABLE app.hosts DROP CONSTRAINT IF EXISTS hosts_archived_revoked_chk" ||
		ds[2] != "ALTER TABLE app.hosts DROP COLUMN IF EXISTS archived_at" || ds[3] != "COMMIT" ||
		strings.Contains(string(down), "RAISE") ||
		!strings.Contains(string(down), "DELETE FROM app.schema_migrations WHERE name = '"+hostArchiveMigration+"'") {
		t.Fatalf("down script %q", ds)
	}
}

// TestHostArchiveMigration is spec-host-remove 6.4: the check refuses
// archived_at on a live host, then down (with an archived host present),
// and up again. It runs in one transaction that is rolled back, so the test
// database keeps 0026.
func TestHostArchiveMigration(t *testing.T) {
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
	// A cleanup, not a defer: freeSharedNames below registers its own
	// cleanup, which runs first and still needs the pool.
	t.Cleanup(d.Close)
	if !onTestDatabase(ctx, d) {
		t.Fatal("refusing a database that is not deploybot_test or deploybot_test_*")
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	p := parity{t: t, st: d, ctx: ctx, at: time.Now().UTC().Truncate(time.Microsecond), tag: strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")}
	owner := p.user("mig26")
	org := p.org(owner.ID)
	p.freeSharedNames(org)
	live := p.enroll(org, nil, owner.ID, "m26l")
	gone := p.enroll(org, nil, owner.ID, "m26g")
	if err := d.RevokeHost(ctx, gone.ID, p.at); err != nil {
		t.Fatal(err)
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
	state := func() (column, check int) {
		t.Helper()
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = 'app' AND table_name = 'hosts' AND column_name = 'archived_at'`).Scan(&column); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conname = 'hosts_archived_revoked_chk'`).Scan(&check); err != nil {
			t.Fatal(err)
		}
		return column, check
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
	checkRefusesLive := func(when string) {
		t.Helper()
		if _, err := tx.Exec(ctx, `SAVEPOINT live`); err != nil {
			t.Fatal(err)
		}
		_, err := tx.Exec(ctx, `UPDATE app.hosts SET archived_at = now() WHERE id = $1::uuid`, live.ID.String())
		if err == nil || !strings.Contains(err.Error(), "hosts_archived_revoked_chk") {
			t.Fatalf("%s: archived_at on a live host: %v", when, err)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT live`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE app.hosts SET archived_at = now() WHERE id = $1::uuid`, gone.ID.String()); err != nil {
			t.Fatalf("%s: archived_at on a revoked host: %v", when, err)
		}
	}

	if c, k := state(); c != 1 || k != 1 {
		t.Fatalf("after up: column %d check %d", c, k)
	}
	checkRefusesLive("after up")

	// The down never refuses, even with an archived host.
	if err := run(script("0026_host_archive.down.sql")); err != nil {
		t.Fatalf("down: %v", err)
	}
	if c, k := state(); c != 0 || k != 0 {
		t.Fatalf("after down: column %d check %d", c, k)
	}
	if err := run(script(hostArchiveMigration)); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if c, k := state(); c != 1 || k != 1 {
		t.Fatalf("after up again: column %d check %d", c, k)
	}
	checkRefusesLive("after up again")
}
