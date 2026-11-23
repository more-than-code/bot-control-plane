package db

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
	"github.com/more-than-code/bot-control-plane/api/migrations"
)

const problemsMigration = "0027_problems.sql"

// dropProblemsSchema takes app.problems down on conn (already
// deploybot_owner) and forgets every migration that built it, so Migrate
// rebuilds it as the newest migration defines it: first 0033 (it widens
// the kind check again and adds the host alert settings), then 0032 (it
// widens the check and adds the agent upgrade table), then 0029 (it widens
// the check and adds the host file objects), then 0027. 0031 (deploy
// reasons) does not touch app.problems and stays applied: its down script
// would delete refused deploy rows.
// Forgetting only 0027 would re-create the table with the 0027 check while
// 0029 stays recorded. A database without app.problems (already taken
// down, both rows forgotten) is left as it is.
func dropProblemsSchema(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'app' AND table_name = 'problems'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		return
	}
	for _, m := range []struct{ down, up string }{
		{"0033_host_alerts.down.sql", hostAlertsMigration},
		{"0032_agent_upgrades.down.sql", agentUpgradesMigration},
		{"0029_host_files.down.sql", hostFilesMigration},
		{"0027_problems.down.sql", problemsMigration},
	} {
		body, err := migrations.FS.ReadFile(m.down)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.PgConn().Exec(ctx, string(body)).ReadAll(); err != nil {
			t.Fatalf("%s: %v", m.down, err)
		}
		if _, err := conn.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '`+m.up+`'`); err != nil {
			t.Fatal(err)
		}
	}
}

// assertNewestProblemsSchema fails when the live problems kind check is not
// the one the newest migration defines (it must allow agent_upgrade, 0032,
// and host_resource, 0033).
func assertNewestProblemsSchema(t *testing.T, ctx context.Context, d *DB) {
	t.Helper()
	var def string
	if err := d.pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'problems_kind_chk'`).Scan(&def); err != nil {
		t.Errorf("problems_kind_chk: %v", err)
		return
	}
	if !strings.Contains(def, "'host_file'") || !strings.Contains(def, "'agent_upgrade'") || !strings.Contains(def, "'host_resource'") {
		t.Errorf("problems_kind_chk is not the newest definition: %s", def)
	}
}

// openRows is the open rows for (kind, subject).
func openRows(p parity, kind, subject string) []store.Problem {
	p.t.Helper()
	rows, err := p.st.ListProblems(p.ctx, store.ProblemFilter{All: true, Kind: kind})
	if err != nil {
		p.t.Fatal(err)
	}
	var out []store.Problem
	for _, r := range rows {
		if r.Subject == subject && r.ResolvedAt == nil {
			out = append(out, r)
		}
	}
	return out
}

// TestParityProblems is spec-bell-problems acceptance 1 at the store: open,
// update, reopen within 30 minutes, a new row after, resolve, list filters,
// prune, one open row under concurrency, and the entry cascade.
func TestParityProblems(t *testing.T) {
	runParity(t, func(p parity) {
		owner := p.user("pb")
		org := p.org(owner.ID)
		proj := p.project(org, owner.ID, "pbproj")
		other := p.project(org, owner.ID, "pbother")
		host := p.enroll(org, nil, owner.ID, "pbh")
		hid, pid := host.ID, proj.ID
		subject := "s-" + p.tag

		in := store.ProblemOpen{Kind: store.ProblemHostOffline, Subject: subject, OrgID: org, HostID: &hid,
			Level: store.ProblemNeedsAction, Title: "pi is offline", Detail: "No report for 11 min."}
		for name, bad := range map[string]func(store.ProblemOpen) store.ProblemOpen{
			"kind":    func(x store.ProblemOpen) store.ProblemOpen { x.Kind = "cert"; return x },
			"level":   func(x store.ProblemOpen) store.ProblemOpen { x.Level = "urgent"; return x },
			"title":   func(x store.ProblemOpen) store.ProblemOpen { x.Title = ""; return x },
			"long":    func(x store.ProblemOpen) store.ProblemOpen { x.Detail = strings.Repeat("é", 301); return x },
			"subject": func(x store.ProblemOpen) store.ProblemOpen { x.Subject = ""; return x },
			"org":     func(x store.ProblemOpen) store.ProblemOpen { x.OrgID = uuid.Nil; return x },
		} {
			if _, err := p.st.OpenProblem(p.ctx, bad(in), p.at); !errors.Is(err, store.ErrInvalid) {
				p.t.Fatalf("%s: %v", name, err)
			}
		}
		// 300 characters (not bytes) is fine.
		long := in
		long.Detail = strings.Repeat("é", 300)
		first, err := p.st.OpenProblem(p.ctx, long, p.at)
		if err != nil {
			p.t.Fatal(err)
		}
		if first.Kind != in.Kind || first.OrgID != org || first.HostID == nil || *first.HostID != hid || first.ProjectID != nil ||
			!first.OpenedAt.Equal(p.at) || first.ResolvedAt != nil {
			p.t.Fatalf("opened %+v", first)
		}
		// While it lasts: same row, new text and level, opened_at kept.
		in.Detail, in.Level = "No report for 12 min.", store.ProblemHeadsUp
		upd, err := p.st.OpenProblem(p.ctx, in, p.at.Add(time.Minute))
		if err != nil || upd.ID != first.ID || upd.Detail != in.Detail || upd.Level != store.ProblemHeadsUp ||
			!upd.OpenedAt.Equal(p.at) || !upd.UpdatedAt.Equal(p.at.Add(time.Minute)) {
			p.t.Fatalf("update %+v %v", upd, err)
		}
		// The same report again is no write: updated_at is kept.
		same, err := p.st.OpenProblem(p.ctx, in, p.at.Add(90*time.Second))
		if err != nil || same.ID != first.ID || !same.UpdatedAt.Equal(p.at.Add(time.Minute)) || same.Detail != in.Detail {
			p.t.Fatalf("identical %+v %v", same, err)
		}
		// Resolve, keeping or replacing text.
		if ok, err := p.st.ResolveProblem(p.ctx, in.Kind, subject, "pi is back online", "", p.at.Add(2*time.Minute)); !ok || err != nil {
			p.t.Fatalf("resolve %v %v", ok, err)
		}
		if ok, _ := p.st.ResolveProblem(p.ctx, in.Kind, subject, "", "", p.at.Add(3*time.Minute)); ok {
			p.t.Fatal("resolved twice")
		}
		if n := len(openRows(p, in.Kind, subject)); n != 0 {
			p.t.Fatalf("open after resolve %d", n)
		}
		// Back within 30 minutes: the same row reopens.
		again, err := p.st.OpenProblem(p.ctx, in, p.at.Add(32*time.Minute))
		if err != nil || again.ID != first.ID || again.ResolvedAt != nil || !again.OpenedAt.Equal(p.at) || again.Title != "pi is offline" {
			p.t.Fatalf("reopen %+v %v", again, err)
		}
		if _, err := p.st.ResolveProblem(p.ctx, in.Kind, subject, "", "", p.at.Add(40*time.Minute)); err != nil {
			p.t.Fatal(err)
		}
		// Back more than 30 minutes later: a new row.
		fresh, err := p.st.OpenProblem(p.ctx, in, p.at.Add(71*time.Minute))
		if err != nil || fresh.ID == first.ID || !fresh.OpenedAt.Equal(p.at.Add(71*time.Minute)) {
			p.t.Fatalf("new row %+v %v", fresh, err)
		}

		// Scoping: by org, by project, by kind, resolved since.
		reg, err := p.st.OpenProblem(p.ctx, store.ProblemOpen{Kind: store.ProblemRegistryLogin, Subject: pid.String() + ":ghcr.io-" + p.tag,
			OrgID: org, ProjectID: &pid, Level: store.ProblemNeedsAction, Title: "ghcr.io login was refused"}, p.at)
		if err != nil {
			p.t.Fatal(err)
		}
		ids := func(f store.ProblemFilter) map[uuid.UUID]bool {
			rows, err := p.st.ListProblems(p.ctx, f)
			if err != nil {
				p.t.Fatal(err)
			}
			out := map[uuid.UUID]bool{}
			for _, r := range rows {
				out[r.ID] = true
			}
			return out
		}
		got := ids(store.ProblemFilter{ProjectIDs: []uuid.UUID{pid}})
		if !got[reg.ID] || got[fresh.ID] || got[first.ID] {
			p.t.Fatalf("project scope %v", got)
		}
		if got := ids(store.ProblemFilter{ProjectIDs: []uuid.UUID{other.ID}}); len(got) != 0 {
			p.t.Fatalf("other project %v", got)
		}
		got = ids(store.ProblemFilter{OrgIDs: []uuid.UUID{org}})
		if !got[reg.ID] || !got[fresh.ID] || got[first.ID] {
			p.t.Fatalf("org scope open only %v", got)
		}
		since := p.at.Add(40 * time.Minute)
		if got := ids(store.ProblemFilter{OrgIDs: []uuid.UUID{org}, ResolvedSince: &since}); !got[first.ID] {
			p.t.Fatalf("resolved since %v", got)
		}
		later := p.at.Add(41 * time.Minute)
		if got := ids(store.ProblemFilter{OrgIDs: []uuid.UUID{org}, ResolvedSince: &later}); got[first.ID] {
			p.t.Fatalf("resolved before since %v", got)
		}
		if got := ids(store.ProblemFilter{OrgIDs: []uuid.UUID{org}, Kind: store.ProblemRegistryLogin}); !got[reg.ID] || got[fresh.ID] {
			p.t.Fatalf("kind %v", got)
		}
		if got := ids(store.ProblemFilter{}); len(got) != 0 {
			p.t.Fatalf("empty scope %v", got)
		}

		// Prune: resolved before the bound only.
		if _, err := p.st.PruneProblems(p.ctx, p.at.Add(39*time.Minute)); err != nil {
			p.t.Fatal(err)
		}
		if got := ids(store.ProblemFilter{OrgIDs: []uuid.UUID{org}, ResolvedSince: &since}); !got[first.ID] {
			p.t.Fatal("pruned a row resolved after the bound")
		}
		if _, err := p.st.PruneProblems(p.ctx, p.at.Add(41*time.Minute)); err != nil {
			p.t.Fatal(err)
		}
		early := p.at.Add(-time.Hour)
		if got := ids(store.ProblemFilter{OrgIDs: []uuid.UUID{org}, ResolvedSince: &early}); got[first.ID] || !got[fresh.ID] {
			p.t.Fatalf("prune %v", got)
		}

		// One open row under concurrent opens.
		race := "race-" + p.tag
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := p.st.OpenProblem(p.ctx, store.ProblemOpen{Kind: store.ProblemHostOffline, Subject: race, OrgID: org,
					Level: store.ProblemNeedsAction, Title: "race"}, p.at)
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				p.t.Fatal(err)
			}
		}
		if n := len(openRows(p, store.ProblemHostOffline, race)); n != 1 {
			p.t.Fatalf("open rows %d", n)
		}

		// Deleting the entry deletes its problems (the 0027 cascade).
		spec, hash, display := catalogSpec(p.t, "pnpm run dev")
		entry, err := p.st.CreateCatalogEntry(p.ctx, pid, store.CatalogWrite{Name: "pbweb", Spec: spec, SpecHash: hash, CommandDisplay: display, Actor: owner.ID, At: p.at})
		if err != nil {
			p.t.Fatal(err)
		}
		eid := entry.ID
		dep, err := p.st.OpenProblem(p.ctx, store.ProblemOpen{Kind: store.ProblemDeploy, Subject: eid.String() + ":" + hid.String(),
			OrgID: org, ProjectID: &pid, HostID: &hid, EntryID: &eid, Level: store.ProblemHeadsUp, Title: "pbweb deploy failed"}, p.at)
		if err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.DeleteCatalogEntry(p.ctx, pid, "pbweb"); err != nil {
			p.t.Fatal(err)
		}
		if got := ids(store.ProblemFilter{ProjectIDs: []uuid.UUID{pid}, ResolvedSince: &early}); got[dep.ID] {
			p.t.Fatal("deploy problem outlived its entry")
		}

		// The sweep inputs: live hosts, and logins of live projects.
		gone := p.enroll(org, nil, owner.ID, "pbg")
		if err := p.st.RevokeHost(p.ctx, gone.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		hosts, err := p.st.ListProblemHosts(p.ctx)
		if err != nil {
			p.t.Fatal(err)
		}
		seen := map[uuid.UUID]store.ProblemHost{}
		for _, h := range hosts {
			seen[h.ID] = h
		}
		if h, ok := seen[hid]; !ok || h.OrgID != org || !h.AlwaysOn || h.Name != host.Name {
			p.t.Fatalf("live host %+v", h)
		}
		if _, ok := seen[gone.ID]; ok {
			p.t.Fatal("revoked host listed")
		}
		w := store.RegistryWrite{Username: "u", Nonce: make([]byte, 12), Ciphertext: make([]byte, 16), KeyVersion: 1}
		for _, pr := range []store.Project{proj, other} {
			if _, err := p.st.PutRegistry(p.ctx, pr.ID, "ghcr.io", w, owner.ID, p.at); err != nil {
				p.t.Fatal(err)
			}
		}
		if err := p.st.DeleteProject(p.ctx, other.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		logins, err := p.st.ListRegistryLogins(p.ctx)
		if err != nil {
			p.t.Fatal(err)
		}
		var mine, theirs bool
		for _, l := range logins {
			mine = mine || (l.ProjectID == pid && l.Host == "ghcr.io" && l.OrgID == org)
			theirs = theirs || l.ProjectID == other.ID
		}
		if !mine || theirs {
			p.t.Fatalf("logins mine %v deleted project %v", mine, theirs)
		}
	})
}

// TestProblemsMigrationText: 0027 creates the table and index, and its down
// script drops the table, never refusing.
func TestProblemsMigrationText(t *testing.T) {
	if !numberedSQL(problemsMigration) || numberedSQL("0027_problems.down.sql") {
		t.Fatal("0027 numbering")
	}
	body, err := migrations.FS.ReadFile(problemsMigration)
	if err != nil {
		t.Fatal(err)
	}
	stmts := sqlBodies(string(body))
	if len(stmts) != 7 || !strings.HasPrefix(stmts[0], "CREATE TABLE app.problems (") ||
		stmts[1] != "CREATE UNIQUE INDEX problems_open_uidx ON app.problems (kind, subject) WHERE resolved_at IS NULL" {
		t.Fatalf("0027 statements %q", stmts)
	}
	down, err := migrations.FS.ReadFile("0027_problems.down.sql")
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
	if len(ds) != 3 || ds[0] != "BEGIN" || ds[1] != "DROP TABLE IF EXISTS app.problems" || ds[2] != "COMMIT" ||
		strings.Contains(string(down), "RAISE") ||
		!strings.Contains(string(down), "DELETE FROM app.schema_migrations WHERE name = '"+problemsMigration+"'") {
		t.Fatalf("down script %q", ds)
	}
}

// TestProblemsMigration is acceptance 8: up, down (with a row present) and
// up again, in one transaction that is rolled back.
func TestProblemsMigration(t *testing.T) {
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
	owner := p.user("mig27")
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
	tables := func() int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'app' AND table_name = 'problems'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
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
	insert := func(subject string) error {
		_, err := tx.Exec(ctx, `
INSERT INTO app.problems (id, kind, subject, org_id, level, title, detail, opened_at, updated_at)
VALUES ($1::uuid, 'host_offline', $2, $3::uuid, 'needs_action', 'x is offline', '', now(), now())`, uuid.NewString(), subject, org.String())
		return err
	}
	uniqueHolds := func(when string) {
		t.Helper()
		subject := "mig27-" + p.tag + "-" + when
		if err := insert(subject); err != nil {
			t.Fatalf("%s: insert %v", when, err)
		}
		if _, err := tx.Exec(ctx, `SAVEPOINT dup`); err != nil {
			t.Fatal(err)
		}
		if err := insert(subject); err == nil || !strings.Contains(err.Error(), "problems_open_uidx") {
			t.Fatalf("%s: second open row %v", when, err)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT dup`); err != nil {
			t.Fatal(err)
		}
	}
	if tables() != 1 {
		t.Fatal("after up: no table")
	}
	uniqueHolds("up")
	if err := run(script("0027_problems.down.sql")); err != nil {
		t.Fatalf("down: %v", err)
	}
	if tables() != 0 {
		t.Fatal("after down: table left")
	}
	if err := run(script(problemsMigration)); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if tables() != 1 {
		t.Fatal("after up again: no table")
	}
	uniqueHolds("again")
}
