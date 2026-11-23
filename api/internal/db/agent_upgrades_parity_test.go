package db

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
	"github.com/more-than-code/bot-control-plane/api/migrations"
)

const agentUpgradesMigration = "0032_agent_upgrades.sql"

// TestParityAgentUpgrades: start, one open row per host, the state
// machine, newest-first lists, the newest row per host, stale rows and the
// agent_upgrade problem kind (api-v1 v1.48).
func TestParityAgentUpgrades(t *testing.T) {
	runParity(t, func(p parity) {
		owner := p.user("au")
		org := p.org(owner.ID)
		host := p.enroll(org, nil, owner.ID, "auh")
		other := p.enroll(org, nil, owner.ID, "auo")
		start := func(h store.Host, at time.Time) (store.AgentUpgrade, error) {
			return p.st.StartAgentUpgrade(p.ctx, store.AgentUpgrade{ID: uuid.New(), HostID: h.ID, From: "0.2.14", To: "0.2.15", StartedBy: owner.ID, StartedAt: at})
		}
		if _, err := p.st.StartAgentUpgrade(p.ctx, store.AgentUpgrade{ID: uuid.New(), HostID: uuid.New(), From: "0.2.14", To: "0.2.15", StartedBy: owner.ID, StartedAt: p.at}); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("unknown host %v", err)
		}
		first, err := start(host, p.at)
		if err != nil || first.State != store.AgentUpgradeStarted || first.StartedByName != "au" || first.FinishedAt != nil || !first.StartedAt.Equal(p.at) {
			p.t.Fatalf("start %+v %v", first, err)
		}
		if _, err := start(host, p.at.Add(time.Second)); !errors.Is(err, store.ErrConflict) {
			p.t.Fatalf("second start %v", err)
		}
		if ok, err := p.st.SettleAgentUpgrade(p.ctx, first.ID, []string{store.AgentUpgradeStarted}, store.AgentUpgradeRunning, "", p.at); !ok || err != nil {
			p.t.Fatalf("running %v %v", ok, err)
		}
		if _, err := start(host, p.at.Add(time.Second)); !errors.Is(err, store.ErrConflict) {
			p.t.Fatalf("start while running %v", err)
		}
		open, err := p.st.OpenAgentUpgrade(p.ctx, host.ID)
		if err != nil || open.ID != first.ID || open.State != store.AgentUpgradeRunning || open.FinishedAt != nil {
			p.t.Fatalf("open %+v %v", open, err)
		}
		// A transition from the wrong state writes nothing.
		if ok, err := p.st.SettleAgentUpgrade(p.ctx, first.ID, []string{store.AgentUpgradeStarted}, store.AgentUpgradeFailed, "busy", p.at); ok || err != nil {
			p.t.Fatalf("wrong from %v %v", ok, err)
		}
		if _, err := p.st.SettleAgentUpgrade(p.ctx, first.ID, store.AgentUpgradeOpenStates, "done", "", p.at); !errors.Is(err, store.ErrInvalid) {
			p.t.Fatalf("bad state %v", err)
		}
		stale, err := p.st.ListStaleAgentUpgrades(p.ctx, p.at.Add(time.Minute))
		if err != nil || !hasUpgrade(stale, first.ID) {
			p.t.Fatalf("stale %+v %v", stale, err)
		}
		if stale, _ := p.st.ListStaleAgentUpgrades(p.ctx, p.at); hasUpgrade(stale, first.ID) {
			p.t.Fatal("stale before start")
		}
		done := p.at.Add(2 * time.Minute)
		if ok, err := p.st.SettleAgentUpgrade(p.ctx, first.ID, store.AgentUpgradeOpenStates, store.AgentUpgradeRolledBack, "not_reconnected", done); !ok || err != nil {
			p.t.Fatalf("settle %v %v", ok, err)
		}
		if _, err := p.st.OpenAgentUpgrade(p.ctx, host.ID); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("open after settle %v", err)
		}
		second, err := start(host, p.at.Add(3*time.Minute))
		if err != nil {
			p.t.Fatal(err)
		}
		list, err := p.st.ListAgentUpgrades(p.ctx, host.ID, 20)
		if err != nil || len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID ||
			list[1].State != store.AgentUpgradeRolledBack || list[1].Reason != "not_reconnected" || list[1].FinishedAt == nil || !list[1].FinishedAt.Equal(done) {
			p.t.Fatalf("list %+v %v", list, err)
		}
		if one, _ := p.st.ListAgentUpgrades(p.ctx, host.ID, 1); len(one) != 1 || one[0].ID != second.ID {
			p.t.Fatalf("limit %+v", one)
		}
		latest, err := p.st.LatestAgentUpgrades(p.ctx, []uuid.UUID{host.ID, other.ID})
		if err != nil || len(latest) != 1 || latest[host.ID].ID != second.ID {
			p.t.Fatalf("latest %+v %v", latest, err)
		}
		if empty, err := p.st.LatestAgentUpgrades(p.ctx, nil); err != nil || len(empty) != 0 {
			p.t.Fatalf("latest none %+v %v", empty, err)
		}
		hid := host.ID
		if _, err := p.st.OpenProblem(p.ctx, store.ProblemOpen{Kind: store.ProblemAgentUpgrade, Subject: "au-" + p.tag, OrgID: org, HostID: &hid,
			Level: store.ProblemNeedsAction, Title: "upgrade rolled back"}, p.at); err != nil {
			p.t.Fatalf("agent_upgrade problem %v", err)
		}
	})
}

func hasUpgrade(rows []store.AgentUpgrade, id uuid.UUID) bool {
	for _, r := range rows {
		if r.ID == id {
			return true
		}
	}
	return false
}

// TestAgentUpgradesMigrationText: 0032 statements and the down script shape.
func TestAgentUpgradesMigrationText(t *testing.T) {
	if !numberedSQL(agentUpgradesMigration) || numberedSQL("0032_agent_upgrades.down.sql") {
		t.Fatal("0032 numbering")
	}
	body, err := migrations.FS.ReadFile(agentUpgradesMigration)
	if err != nil {
		t.Fatal(err)
	}
	stmts := sqlBodies(string(body))
	if len(stmts) != 8 || !strings.HasPrefix(stmts[0], "CREATE TABLE app.host_agent_upgrades (") ||
		stmts[1] != "CREATE INDEX host_agent_upgrades_host_idx ON app.host_agent_upgrades (host_id, started_at DESC)" ||
		stmts[2] != "CREATE UNIQUE INDEX host_agent_upgrades_open_uidx ON app.host_agent_upgrades (host_id) WHERE state IN ('started', 'running')" ||
		stmts[6] != "GRANT SELECT, INSERT ON TABLE app.host_agent_upgrades TO deploybot_api" ||
		stmts[7] != "GRANT UPDATE (state, reason, finished_at) ON TABLE app.host_agent_upgrades TO deploybot_api" {
		t.Fatalf("0032 statements %d %q", len(stmts), stmts)
	}
	down, err := migrations.FS.ReadFile("0032_agent_upgrades.down.sql")
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
	if len(ds) != 7 || ds[0] != "BEGIN" || ds[6] != "COMMIT" || !strings.Contains(string(down), "DROP INDEX IF EXISTS app.host_agent_upgrades_open_uidx") || strings.Contains(string(down), "RAISE") ||
		!strings.Contains(string(down), "DELETE FROM app.schema_migrations WHERE name = '"+agentUpgradesMigration+"'") {
		t.Fatalf("down script %q", ds)
	}
}

// TestAgentUpgradesMigration: up, down (with a row and an agent_upgrade
// problem) and up again, in one transaction that is rolled back, and the
// deploybot_api grants.
func TestAgentUpgradesMigration(t *testing.T) {
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
	p := parity{t: t, st: d, ctx: ctx, at: time.Now().UTC().Truncate(time.Microsecond), tag: strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")}
	owner := p.user("mig31")
	org := p.org(owner.ID)
	host := p.enroll(org, nil, owner.ID, "mig31")

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
	if _, err := tx.Exec(ctx, `
INSERT INTO app.host_agent_upgrades (id, host_id, from_version, to_version, state, started_by, started_at)
VALUES (gen_random_uuid(), $1::uuid, '0.2.14', '0.2.15', 'running', $2::uuid, now())`, host.ID.String(), owner.ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO app.problems (id, kind, subject, org_id, level, title, detail, opened_at, updated_at)
VALUES (gen_random_uuid(), 'agent_upgrade', 'mig31', $1::uuid, 'needs_action', 't', '', now(), now())`, org.String()); err != nil {
		t.Fatal(err)
	}
	tables := func() int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, `
SELECT (SELECT count(*) FROM information_schema.tables WHERE table_schema = 'app' AND table_name = 'host_agent_upgrades')
     + (SELECT count(*) FROM pg_indexes WHERE schemaname = 'app' AND indexname = 'host_agent_upgrades_open_uidx')`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// A second open row for the host is refused by the partial unique index.
	secondOpen := func() error {
		t.Helper()
		if _, err := tx.Exec(ctx, `SAVEPOINT o`); err != nil {
			t.Fatal(err)
		}
		_, err := tx.Exec(ctx, `
INSERT INTO app.host_agent_upgrades (id, host_id, from_version, to_version, state, started_by, started_at)
VALUES (gen_random_uuid(), $1::uuid, '0.2.14', '0.2.15', 'started', $2::uuid, now())`, host.ID.String(), owner.ID.String())
		if _, rerr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT o`); rerr != nil {
			t.Fatal(rerr)
		}
		return err
	}
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
	kindAllowed := func() bool {
		t.Helper()
		if _, err := tx.Exec(ctx, `SAVEPOINT k`); err != nil {
			t.Fatal(err)
		}
		_, err := tx.Exec(ctx, `
INSERT INTO app.problems (id, kind, subject, org_id, level, title, detail, opened_at, updated_at)
VALUES (gen_random_uuid(), 'agent_upgrade', 'kind-check', $1::uuid, 'heads_up', 't', '', now(), now())`, org.String())
		if _, rerr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT k`); rerr != nil {
			t.Fatal(rerr)
		}
		return err == nil
	}
	if tables() != 2 || !kindAllowed() || secondOpen() == nil {
		t.Fatal("after up")
	}
	if err := run("0032_agent_upgrades.down.sql"); err != nil {
		t.Fatalf("down: %v", err)
	}
	if tables() != 0 || kindAllowed() {
		t.Fatal("after down")
	}
	if err := run("0032_agent_upgrades.down.sql"); err != nil {
		t.Fatalf("down twice: %v", err)
	}
	if err := run(agentUpgradesMigration); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if tables() != 2 || !kindAllowed() {
		t.Fatal("after up again")
	}
	for _, c := range []struct {
		priv string
		want bool
	}{{"SELECT", true}, {"INSERT", true}, {"UPDATE", false}, {"DELETE", false}} {
		var got bool
		if err := tx.QueryRow(ctx, `SELECT has_table_privilege('deploybot_api', 'app.host_agent_upgrades', $1)`, c.priv).Scan(&got); err != nil || got != c.want {
			t.Fatalf("deploybot_api %s: %v %v", c.priv, got, err)
		}
	}
	for _, c := range []struct {
		col  string
		want bool
	}{{"state", true}, {"reason", true}, {"finished_at", true}, {"to_version", false}, {"host_id", false}} {
		var got bool
		if err := tx.QueryRow(ctx, `SELECT has_column_privilege('deploybot_api', 'app.host_agent_upgrades', $1, 'UPDATE')`, c.col).Scan(&got); err != nil || got != c.want {
			t.Fatalf("deploybot_api UPDATE %s: %v %v", c.col, got, err)
		}
	}
}

// TestParityAgentUpgradeSettleFrom: a settle from any open state still
// wins after the route moved the row to running (review L1); unknown can
// later become succeeded only from unknown (review M1 a).
func TestParityAgentUpgradeSettleFrom(t *testing.T) {
	runParity(t, func(p parity) {
		owner := p.user("asf")
		host := p.enroll(p.org(owner.ID), nil, owner.ID, "asf")
		row, err := p.st.StartAgentUpgrade(p.ctx, store.AgentUpgrade{ID: uuid.New(), HostID: host.ID, From: "0.2.14", To: "0.2.15", StartedBy: owner.ID, StartedAt: p.at})
		if err != nil {
			p.t.Fatal(err)
		}
		// The hook read the row as started; the route then sets running.
		if ok, _ := p.st.SettleAgentUpgrade(p.ctx, row.ID, []string{store.AgentUpgradeStarted}, store.AgentUpgradeRunning, "", p.at); !ok {
			p.t.Fatal("running")
		}
		if ok, err := p.st.SettleAgentUpgrade(p.ctx, row.ID, store.AgentUpgradeOpenStates, store.AgentUpgradeUnknown, "", p.at); !ok || err != nil {
			p.t.Fatalf("settle from open %v %v", ok, err)
		}
		// The route's exact started → running no longer applies.
		if ok, _ := p.st.SettleAgentUpgrade(p.ctx, row.ID, []string{store.AgentUpgradeStarted}, store.AgentUpgradeRunning, "", p.at); ok {
			p.t.Fatal("started guard ignored")
		}
		if ok, _ := p.st.SettleAgentUpgrade(p.ctx, row.ID, store.AgentUpgradeOpenStates, store.AgentUpgradeSucceeded, "", p.at); ok {
			p.t.Fatal("settled from unknown as open")
		}
		if ok, err := p.st.SettleAgentUpgrade(p.ctx, row.ID, []string{store.AgentUpgradeUnknown}, store.AgentUpgradeSucceeded, "", p.at.Add(time.Minute)); !ok || err != nil {
			p.t.Fatalf("unknown to succeeded %v %v", ok, err)
		}
		list, _ := p.st.ListAgentUpgrades(p.ctx, host.ID, 1)
		if list[0].State != store.AgentUpgradeSucceeded || list[0].FinishedAt == nil || !list[0].FinishedAt.Equal(p.at.Add(time.Minute)) {
			p.t.Fatalf("row %+v", list[0])
		}
	})
}
