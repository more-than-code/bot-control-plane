package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/more-than-code/bot-control-plane/api/migrations"
)

// TestRetentionProblemMigrationText pins 0039: numbering, two statements
// that widen the kind check by metrics_retention only, and comments that
// splitSQL cannot break (no semicolon or apostrophe).
func TestRetentionProblemMigrationText(t *testing.T) {
	if !numberedSQL(retentionProblemMigration) || numberedSQL("0039_problem_metrics_retention.down.sql") {
		t.Fatal("0039 numbering")
	}
	body, err := migrations.FS.ReadFile(retentionProblemMigration)
	if err != nil {
		t.Fatal(err)
	}
	down, err := migrations.FS.ReadFile("0039_problem_metrics_retention.down.sql")
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
	stmts := sqlBodies(string(body))
	if len(stmts) != 2 || stmts[0] != "ALTER TABLE app.problems DROP CONSTRAINT problems_kind_chk" ||
		!strings.Contains(stmts[1], "('host_offline', 'registry_login', 'deploy', 'host_file', 'agent_upgrade', 'host_resource', 'metrics_retention')") {
		t.Fatalf("0039 statements %q", stmts)
	}
}

// TestRetentionProblemMigration: after 0039 a metrics_retention row is
// accepted; the down script deletes such rows and refuses the kind again;
// up again accepts it. Everything runs in a rolled-back transaction.
func TestRetentionProblemMigration(t *testing.T) {
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
	t.Cleanup(d.Close)
	if !onTestDatabase(ctx, d) {
		t.Fatal("refusing a database that is not deploybot_test or deploybot_test_*")
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	p := parity{t: t, st: d, ctx: ctx, at: time.Now().UTC().Truncate(time.Microsecond), tag: strings.ReplaceAll(uuid.NewString(), "-", "")[:8]}
	owner := p.user("mig39")
	org := p.org(owner.ID)

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
	script := func(file string) {
		t.Helper()
		body, err := migrations.FS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, stmt := range sqlBodies(string(body)) {
			if stmt == "BEGIN" || stmt == "COMMIT" {
				continue
			}
			if _, err := tx.Exec(ctx, stmt); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
		}
	}
	insert := func() error {
		_, err := tx.Exec(ctx, `
INSERT INTO app.problems (id, kind, subject, org_id, level, title, detail, opened_at, updated_at)
VALUES ($1::uuid, 'metrics_retention', $2, $3::uuid, 'heads_up', 'x', '', now(), now())`,
			uuid.NewString(), "metrics_retention:"+org.String()+":"+p.tag, org.String())
		return err
	}
	count := func() int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.problems WHERE kind = 'metrics_retention' AND subject LIKE '%' || $1`, p.tag).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := insert(); err != nil {
		t.Fatalf("refused after up: %v", err)
	}
	script("0039_problem_metrics_retention.down.sql")
	if count() != 0 {
		t.Fatal("after down: row left")
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT k`); err != nil {
		t.Fatal(err)
	}
	if err := insert(); err == nil || !strings.Contains(err.Error(), "problems_kind_chk") {
		t.Fatalf("accepted after down: %v", err)
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT k`); err != nil {
		t.Fatal(err)
	}
	script(retentionProblemMigration)
	if err := insert(); err != nil {
		t.Fatalf("refused after up again: %v", err)
	}
}
