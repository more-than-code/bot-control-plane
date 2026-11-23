package db

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
	"github.com/more-than-code/bot-control-plane/api/migrations"
)

const deployTaskMigration = "0034_deploy_task.sql"

// TestParityDeployTask: the task id on a deploy row, the lookup by host and
// task, and the late settle of in-flight and timeout rows (api-v1 v1.50
// item 3), on memstore and Postgres alike.
func TestParityDeployTask(t *testing.T) {
	runParity(t, func(p parity) {
		owner := p.user("dt")
		org := p.org(owner.ID)
		pr := p.project(org, owner.ID, "dt-"+p.tag)
		host := p.enroll(org, nil, owner.ID, "dt")
		other := p.enroll(org, nil, owner.ID, "dto")
		img, err := catalog.BuildImage("web", []byte(`{"kind":"service","image":"nginx"}`), nil, nil)
		if err != nil {
			p.t.Fatal(err)
		}
		entry, err := p.st.CreateCatalogEntry(p.ctx, pr.ID, store.CatalogWrite{Name: "web", Kind: "image", ImageRaw: json.RawMessage(catalog.SpecJSON(img)),
			SpecHash: strings.Repeat("a", 64), CommandDisplay: "nginx", Actor: owner.ID, At: p.at})
		if err != nil {
			p.t.Fatal(err)
		}
		begin := func(h uuid.UUID, at time.Time) store.ImageDeploy {
			p.t.Helper()
			row, err := p.st.BeginImageOp(p.ctx, entry.ID, h, entry.SpecHash, owner.ID, "mcp", at, at.Add(time.Hour))
			if err != nil {
				p.t.Fatal(err)
			}
			return row
		}
		row := begin(host.ID, p.at)
		task := uuid.New()
		if err := p.st.SetImageOpTask(p.ctx, row.ID, task, "bogus"); err == nil {
			p.t.Fatal("bad action accepted")
		}
		if err := p.st.SetImageOpTask(p.ctx, uuid.New(), task, "deploy"); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("unknown row %v", err)
		}
		if err := p.st.SetImageOpTask(p.ctx, row.ID, task, "deploy"); err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.ImageDeployByTask(p.ctx, other.ID, task); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("other host found the row %v", err)
		}
		if _, err := p.st.ImageDeployByTask(p.ctx, host.ID, uuid.New()); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("unknown task %v", err)
		}
		got, err := p.st.ImageDeployByTask(p.ctx, host.ID, task)
		if err != nil || got.Deploy.ID != row.ID || !got.Deploy.InFlight || got.Action != "deploy" || got.ProjectID != pr.ID || got.Entry != "web" {
			p.t.Fatalf("by task %+v %v", got, err)
		}
		// The same task id on a second row is refused (unique).
		row2 := begin(other.ID, p.at)
		if err := p.st.SetImageOpTask(p.ctx, row2.ID, task, "rollback"); !errors.Is(err, store.ErrConflict) {
			p.t.Fatalf("duplicate task id %v", err)
		}

		// In flight: settled, finished_at is the settle time.
		digest := "sha256:" + strings.Repeat("b", 64)
		exit := 0
		at := p.at.Add(time.Minute)
		settled, err := p.st.SettleLateImageDeploy(p.ctx, row.ID, store.DeployFinish{
			Outcome: "succeeded", Digest: &digest, ExitCode: &exit, FinalStatus: &store.DeployFinalStatus{State: "running"}, LogTail: []string{"ok"}, At: at,
		})
		if err != nil || settled.Outcome != "succeeded" || settled.InFlight || settled.FinishedAt == nil || !settled.FinishedAt.Equal(at) || *settled.Digest != digest {
			p.t.Fatalf("settle in flight %+v %v", settled, err)
		}
		// Now settled otherwise: left alone.
		if _, err := p.st.SettleLateImageDeploy(p.ctx, row.ID, store.DeployFinish{Outcome: "failed", At: at}); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("settled row changed %v", err)
		}

		// Timeout: settled, finished_at kept; values the result lacks are cleared.
		if err := p.st.SetImageOpTask(p.ctx, row2.ID, uuid.New(), "rollback"); err != nil {
			p.t.Fatal(err)
		}
		timedAt := p.at.Add(2 * time.Minute)
		if _, err := p.st.FinishImageDeployWith(p.ctx, row2.ID, store.DeployFinish{Outcome: "timeout", FinalStatus: &store.DeployFinalStatus{State: "unknown"}, At: timedAt}); err != nil {
			p.t.Fatal(err)
		}
		// The same outcome again is not a re-settle (api-v1 v1.50 item 9).
		if _, err := p.st.SettleLateImageDeploy(p.ctx, row2.ID, store.DeployFinish{Outcome: "timeout", At: p.at.Add(time.Hour)}); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("timeout replay %v", err)
		}
		late, err := p.st.SettleLateImageDeploy(p.ctx, row2.ID, store.DeployFinish{Outcome: "rolled_back", At: p.at.Add(time.Hour)})
		if err != nil || late.Outcome != "rolled_back" || late.FinishedAt == nil || !late.FinishedAt.Equal(timedAt) || late.FinalStatus != nil || late.Digest != nil {
			p.t.Fatalf("settle timeout %+v %v", late, err)
		}
		full, err := p.st.GetImageDeploy(p.ctx, entry.ID, other.ID, row2.ID)
		if err != nil || full.LogTail != nil || full.FinalStatus != nil {
			p.t.Fatalf("stored %+v %v", full, err)
		}
	})
}

// TestParityTimeoutStaleImageOps: in-flight rows past in_flight_until become
// timeout with finished_at at, with their task action, project and entry;
// rows still inside their deadline and finished rows are left (api-v1 v1.50).
func TestParityTimeoutStaleImageOps(t *testing.T) {
	runParity(t, func(p parity) {
		owner := p.user("ts")
		org := p.org(owner.ID)
		pr := p.project(org, owner.ID, "ts-"+p.tag)
		stale := p.enroll(org, nil, owner.ID, "tss")
		fresh := p.enroll(org, nil, owner.ID, "tsf")
		img, err := catalog.BuildImage("web", []byte(`{"kind":"service","image":"nginx"}`), nil, nil)
		if err != nil {
			p.t.Fatal(err)
		}
		entry, err := p.st.CreateCatalogEntry(p.ctx, pr.ID, store.CatalogWrite{Name: "web", Kind: "image", ImageRaw: json.RawMessage(catalog.SpecJSON(img)),
			SpecHash: strings.Repeat("a", 64), CommandDisplay: "nginx", Actor: owner.ID, At: p.at})
		if err != nil {
			p.t.Fatal(err)
		}
		// Far in the future so rows of other tests in the shared database
		// are already past their deadline and only ours are compared.
		base := p.at.Add(100 * 365 * 24 * time.Hour)
		old, err := p.st.BeginImageOp(p.ctx, entry.ID, stale.ID, entry.SpecHash, owner.ID, "mcp", base, base.Add(time.Minute))
		if err != nil {
			p.t.Fatal(err)
		}
		if err := p.st.SetImageOpTask(p.ctx, old.ID, uuid.New(), "rollback"); err != nil {
			p.t.Fatal(err)
		}
		live, err := p.st.BeginImageOp(p.ctx, entry.ID, fresh.ID, entry.SpecHash, owner.ID, "console", base, base.Add(time.Hour))
		if err != nil {
			p.t.Fatal(err)
		}
		at := base.Add(2 * time.Minute)
		rows, err := p.st.TimeoutStaleImageOps(p.ctx, at)
		if err != nil {
			p.t.Fatal(err)
		}
		var mine []store.DeployTask
		for _, r := range rows {
			if r.Deploy.EntryID == entry.ID {
				mine = append(mine, r)
			}
		}
		if len(mine) != 1 || mine[0].Deploy.ID != old.ID || mine[0].Action != "rollback" || mine[0].ProjectID != pr.ID || mine[0].Entry != "web" ||
			mine[0].Deploy.Outcome != "timeout" || mine[0].Deploy.InFlight || mine[0].Deploy.FinishedAt == nil || !mine[0].Deploy.FinishedAt.Equal(at) {
			p.t.Fatalf("swept %+v", mine)
		}
		if got, err := p.st.RunningImageOp(p.ctx, entry.ID, fresh.ID); err != nil || got.ID != live.ID {
			p.t.Fatalf("fresh row %+v %v", got, err)
		}
		if again, err := p.st.TimeoutStaleImageOps(p.ctx, at); err != nil || len(again) != 0 && again[0].Deploy.EntryID == entry.ID {
			p.t.Fatalf("second sweep %+v %v", again, err)
		}
		if err := p.st.ReleaseImageOp(p.ctx, live.ID); err != nil {
			p.t.Fatal(err)
		}
	})
}

// TestDeployTaskMigrationText: 0034 statements and the down script shape.
func TestDeployTaskMigrationText(t *testing.T) {
	if !numberedSQL(deployTaskMigration) || numberedSQL("0034_deploy_task.down.sql") {
		t.Fatal("0034 numbering")
	}
	body, err := migrations.FS.ReadFile(deployTaskMigration)
	if err != nil {
		t.Fatal(err)
	}
	stmts := sqlBodies(string(body))
	if len(stmts) != 5 || stmts[4] != "GRANT UPDATE (task_id, task_action) ON TABLE app.image_deploys TO deploybot_api" {
		t.Fatalf("0034 statements %d %q", len(stmts), stmts)
	}
	down, err := migrations.FS.ReadFile("0034_deploy_task.down.sql")
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
	if len(ds) != 6 || ds[0] != "BEGIN" || ds[5] != "COMMIT" ||
		!strings.Contains(string(down), "DELETE FROM app.schema_migrations WHERE name = '"+deployTaskMigration+"'") {
		t.Fatalf("down script %q", ds)
	}
}

// TestDeployTaskMigration: up, down (with a task id stored) and up again in
// one transaction that is rolled back, and the deploybot_api grants.
func TestDeployTaskMigration(t *testing.T) {
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
	run := func(file string) error {
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
				return err
			}
		}
		return nil
	}
	cols := func() int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, `
SELECT (SELECT count(*) FROM information_schema.columns WHERE table_schema = 'app' AND table_name = 'image_deploys' AND column_name IN ('task_id', 'task_action'))
     + (SELECT count(*) FROM pg_indexes WHERE schemaname = 'app' AND indexname = 'image_deploys_task_uidx')`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if cols() != 3 {
		t.Fatal("after up")
	}
	if err := run("0034_deploy_task.down.sql"); err != nil {
		t.Fatalf("down: %v", err)
	}
	if cols() != 0 {
		t.Fatal("after down")
	}
	if err := run("0034_deploy_task.down.sql"); err != nil {
		t.Fatalf("down twice: %v", err)
	}
	if err := run(deployTaskMigration); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if cols() != 3 {
		t.Fatal("after up again")
	}
	for _, c := range []struct {
		col  string
		want bool
	}{{"task_id", true}, {"task_action", true}, {"entry_id", false}, {"host_id", false}} {
		var got bool
		if err := tx.QueryRow(ctx, `SELECT has_column_privilege('deploybot_api', 'app.image_deploys', $1, 'UPDATE')`, c.col).Scan(&got); err != nil || got != c.want {
			t.Fatalf("deploybot_api UPDATE %s: %v %v", c.col, got, err)
		}
	}
}
