package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
	"github.com/more-than-code/bot-control-plane/api/migrations"
)

const hostFilesMigration = "0029_host_files.sql"

func plainSave(p parity, host store.Host, by store.User, name, content string, base *int) store.HostFileSave {
	sum := sha256.Sum256([]byte(content))
	return store.HostFileSave{HostID: host.ID, Name: name, Size: len(content), Plain: content, SHA256: hex.EncodeToString(sum[:]), BaseVersion: base, CreatedBy: by.ID, At: p.at}
}

func secretSave(p parity, host store.Host, by store.User, name string, size int) store.HostFileSave {
	return store.HostFileSave{HostID: host.ID, Name: name, Secret: true, Size: size, CreatedBy: by.ID, At: p.at,
		Seal: func(version int) ([]byte, []byte, int, error) {
			return bytes.Repeat([]byte{byte(version)}, 12), bytes.Repeat([]byte{7}, size+16), 1, nil
		}}
}

// TestParityHostFiles is spec-host-config-files §5 items 11 and 12 at the
// store: versions number from 1, a stale base is ErrConflict, a secret
// version keeps ciphertext only, applies give the applied version, 51
// saves keep the newest 50 and the applied one, and the status round-trips.
func TestParityHostFiles(t *testing.T) {
	runParity(t, func(p parity) {
		t := p.t
		owner := p.user("hf")
		org := p.org(owner.ID)
		host := p.enroll(org, nil, owner.ID, "hf")
		zero := 0
		if v, err := p.st.SaveHostFileVersion(p.ctx, plainSave(p, host, owner, "dd-policy", "a = 1\n", &zero)); err != nil || v != 1 {
			t.Fatalf("first save %d %v", v, err)
		}
		if _, err := p.st.SaveHostFileVersion(p.ctx, plainSave(p, host, owner, "dd-policy", "b\n", &zero)); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale base %v", err)
		}
		bad := plainSave(p, host, owner, "dd-policy", "x", nil)
		bad.SHA256 = "nope"
		if _, err := p.st.SaveHostFileVersion(p.ctx, bad); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("invalid %v", err)
		}
		if _, err := p.st.SaveHostFileVersion(p.ctx, plainSave(p, host, owner, "dd-policy", "a\x00b", nil)); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("NUL %v", err)
		}
		got, err := p.st.GetHostFileVersion(p.ctx, host.ID, "dd-policy", 0)
		if err != nil || got.Version != 1 || got.Plain != "a = 1\n" || got.Secret || len(got.SHA256) != 64 || got.CreatedBy != owner.ID {
			t.Fatalf("latest %+v %v", got, err)
		}
		if _, err := p.st.GetHostFileVersion(p.ctx, host.ID, "dd-policy", 9); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("missing version %v", err)
		}
		// Secret.
		if v, err := p.st.SaveHostFileVersion(p.ctx, secretSave(p, host, owner, "dd-secrets", 20)); err != nil || v != 1 {
			t.Fatalf("secret save %d %v", v, err)
		}
		sv, err := p.st.GetHostFileVersion(p.ctx, host.ID, "dd-secrets", 1)
		if err != nil || !sv.Secret || sv.SHA256 != "" || sv.Plain != "" || len(sv.Nonce) != 12 || sv.Nonce[0] != 1 || len(sv.Ciphertext) != 36 || sv.KeyVersion != 1 {
			t.Fatalf("secret %+v %v", sv, err)
		}
		// Applies: unchanged counts as applied, rolled_back does not.
		for _, a := range []struct {
			v       int
			outcome string
		}{{1, store.HostFileUnchanged}, {1, store.HostFileRolledBack}} {
			if err := p.st.RecordHostFileApply(p.ctx, store.HostFileApply{HostID: host.ID, Name: "dd-policy", Version: a.v, Outcome: a.outcome, Stage: "health", Reason: "not_connected", TookMS: 5, By: owner.ID, At: p.at}); err != nil {
				t.Fatal(err)
			}
			p.at = p.at.Add(time.Second)
		}
		if err := p.st.RecordHostFileApply(p.ctx, store.HostFileApply{HostID: host.ID, Name: "dd-policy", Version: 1, Outcome: "weird", By: owner.ID, At: p.at}); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("bad outcome %v", err)
		}
		for range 51 {
			if _, err := p.st.SaveHostFileVersion(p.ctx, plainSave(p, host, owner, "dd-policy", "n\n", nil)); err != nil {
				t.Fatal(err)
			}
		}
		rows, err := p.st.ListHostFileVersions(p.ctx, host.ID, "dd-policy")
		if err != nil || len(rows) != 51 || rows[0].Version != 52 || rows[49].Version != 3 || rows[50].Version != 1 || rows[0].Plain != "" {
			t.Fatalf("retention %d %v", len(rows), err)
		}
		states, err := p.st.HostFileStates(p.ctx, host.ID)
		if err != nil || len(states) != 2 || states[0].Name != "dd-policy" || *states[0].LatestVersion != 52 || *states[0].AppliedVersion != 1 ||
			states[0].LastApply == nil || states[0].LastApply.Outcome != store.HostFileRolledBack || states[0].LastApply.Reason != "not_connected" ||
			states[1].Name != "dd-secrets" || states[1].AppliedVersion != nil || states[1].LastApply != nil {
			t.Fatalf("states %+v %v", states, err)
		}
		// Status.
		if st, err := p.st.GetHostFilesStatus(p.ctx, host.ID); err != nil || st != nil {
			t.Fatalf("no status %+v %v", st, err)
		}
		tr, size, since := true, 10, p.at.Add(-time.Hour)
		in := store.HostFilesStatus{At: p.at, Files: []store.HostFileStatus{
			{Name: "dd-policy", Exists: true, OwnerOK: true, MatchesApplied: &tr, Size: &size, SHA256: strings.Repeat("c", 64)},
			{Name: "dd-secrets", Exists: false, Reason: "missing", BadSince: &since},
		}}
		if err := p.st.SetHostFilesStatus(p.ctx, host.ID, in); err != nil {
			t.Fatal(err)
		}
		st, err := p.st.GetHostFilesStatus(p.ctx, host.ID)
		if err != nil || st == nil || !st.At.Equal(p.at) || len(st.Files) != 2 || st.Files[0].Word() != store.HostFileInSync || *st.Files[0].Size != 10 ||
			st.Files[1].MatchesApplied != nil || !st.Files[1].BadSince.Equal(since) {
			t.Fatalf("status %+v %v", st, err)
		}
		all, err := p.st.ListHostFilesStatuses(p.ctx)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range all {
			if r.HostID == host.ID {
				found = r.OrgID == org && len(r.Status.Files) == 2
			}
		}
		if !found {
			t.Fatal("status row not listed")
		}
		// A problem of kind host_file is accepted.
		hid := host.ID
		if _, err := p.st.OpenProblem(p.ctx, store.ProblemOpen{Kind: store.ProblemHostFile, Subject: host.ID.String() + ":dd-policy", OrgID: org, HostID: &hid,
			Level: store.ProblemNeedsAction, Title: "dd-policy could not be restored"}, p.at); err != nil {
			t.Fatalf("host_file problem %v", err)
		}
	})
}

// TestHostFilesMigrationText: 0029 statements and the down script shape.
func TestHostFilesMigrationText(t *testing.T) {
	if !numberedSQL(hostFilesMigration) || numberedSQL("0029_host_files.down.sql") {
		t.Fatal("0029 numbering")
	}
	body, err := migrations.FS.ReadFile(hostFilesMigration)
	if err != nil {
		t.Fatal(err)
	}
	stmts := sqlBodies(string(body))
	grants := strings.Join(stmts, "\n")
	for _, want := range []string{
		"GRANT SELECT, INSERT, DELETE ON TABLE app.host_file_versions TO deploybot_api",
		"GRANT SELECT, INSERT ON TABLE app.host_file_applies TO deploybot_api",
	} {
		if !strings.Contains(grants, want) {
			t.Fatalf("0029 lacks %s", want)
		}
	}
	if len(stmts) != 11 || !strings.HasPrefix(stmts[0], "CREATE TABLE app.host_file_versions (") || !strings.HasPrefix(stmts[1], "CREATE TABLE app.host_file_applies (") ||
		stmts[10] != "GRANT UPDATE (host_files_status) ON TABLE app.hosts TO deploybot_api" {
		t.Fatalf("0029 statements %d %q", len(stmts), stmts)
	}
	down, err := migrations.FS.ReadFile("0029_host_files.down.sql")
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
	if len(ds) != 8 || ds[0] != "BEGIN" || ds[7] != "COMMIT" || strings.Contains(string(down), "RAISE") ||
		!strings.Contains(string(down), "DELETE FROM app.schema_migrations WHERE name = '"+hostFilesMigration+"'") {
		t.Fatalf("down script %q", ds)
	}
}

// TestHostFilesMigration is §5 item 12: up, down (with rows in every new
// object and a host_file problem) and up again, in one transaction that is
// rolled back.
func TestHostFilesMigration(t *testing.T) {
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
	owner := p.user("mig29")
	org := p.org(owner.ID)
	host := p.enroll(org, nil, owner.ID, "mig29")
	if _, err := d.SaveHostFileVersion(ctx, plainSave(p, host, owner, "dd-policy", "x\n", nil)); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordHostFileApply(ctx, store.HostFileApply{HostID: host.ID, Name: "dd-policy", Version: 1, Outcome: store.HostFileSucceeded, By: owner.ID, At: p.at}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetHostFilesStatus(ctx, host.ID, store.HostFilesStatus{At: p.at, Files: []store.HostFileStatus{{Name: "dd-policy", Exists: true}}}); err != nil {
		t.Fatal(err)
	}
	hid := host.ID
	if _, err := d.OpenProblem(ctx, store.ProblemOpen{Kind: store.ProblemHostFile, Subject: "mig29-" + p.tag, OrgID: org, HostID: &hid, Level: store.ProblemHeadsUp, Title: "t"}, p.at); err != nil {
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
	objects := func() int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, `
SELECT (SELECT count(*) FROM information_schema.tables WHERE table_schema = 'app' AND table_name IN ('host_file_versions', 'host_file_applies'))
     + (SELECT count(*) FROM information_schema.columns WHERE table_schema = 'app' AND table_name = 'hosts' AND column_name = 'host_files_status')`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
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
VALUES (gen_random_uuid(), 'host_file', 'kind-check', $1::uuid, 'heads_up', 't', '', now(), now())`, org.String())
		if _, rerr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT k`); rerr != nil {
			t.Fatal(rerr)
		}
		return err == nil
	}
	if objects() != 3 || !kindAllowed() {
		t.Fatal("after up")
	}
	// 0032 sits on the 0029 kind check: take it down first.
	if err := run("0032_agent_upgrades.down.sql"); err != nil {
		t.Fatalf("0032 down: %v", err)
	}
	if err := run("0029_host_files.down.sql"); err != nil {
		t.Fatalf("down: %v", err)
	}
	if objects() != 0 || kindAllowed() {
		t.Fatal("after down")
	}
	if err := run("0029_host_files.down.sql"); err != nil {
		t.Fatalf("down twice: %v", err)
	}
	if err := run(hostFilesMigration); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if objects() != 3 || !kindAllowed() {
		t.Fatal("after up again")
	}
	for _, c := range []struct {
		table, priv string
		want        bool
	}{
		{"app.host_file_versions", "SELECT", true}, {"app.host_file_versions", "INSERT", true}, {"app.host_file_versions", "DELETE", true},
		{"app.host_file_versions", "UPDATE", false},
		{"app.host_file_applies", "SELECT", true}, {"app.host_file_applies", "INSERT", true},
		{"app.host_file_applies", "UPDATE", false}, {"app.host_file_applies", "DELETE", false},
	} {
		var got bool
		if err := tx.QueryRow(ctx, `SELECT has_table_privilege('deploybot_api', $1, $2)`, c.table, c.priv).Scan(&got); err != nil || got != c.want {
			t.Fatalf("deploybot_api %s on %s: %v %v", c.priv, c.table, got, err)
		}
	}
	var col bool
	if err := tx.QueryRow(ctx, `SELECT has_column_privilege('deploybot_api', 'app.hosts', 'host_files_status', 'UPDATE')`).Scan(&col); err != nil || !col {
		t.Fatalf("status column grant %v %v", col, err)
	}
}

// TestParityHostFileRetention is §5 item 11 (B14): exactly 50 prunes
// nothing; an applied version inside the newest 50 protects nothing extra;
// a moved applied version stops protecting the old one; rolled_back does
// not protect; Protect keeps an in-flight target; names are separate.
func TestParityHostFileRetention(t *testing.T) {
	runParity(t, func(p parity) {
		t := p.t
		owner := p.user("hfr")
		host := p.enroll(p.org(owner.ID), nil, owner.ID, "hfr")
		save := func(name string, protect int) {
			t.Helper()
			in := plainSave(p, host, owner, name, "x\n", nil)
			in.Protect = func() int { return protect }
			if _, err := p.st.SaveHostFileVersion(p.ctx, in); err != nil {
				t.Fatal(err)
			}
		}
		apply := func(name string, v int, outcome string) {
			t.Helper()
			p.at = p.at.Add(time.Second)
			if err := p.st.RecordHostFileApply(p.ctx, store.HostFileApply{HostID: host.ID, Name: name, Version: v, Outcome: outcome, By: owner.ID, At: p.at}); err != nil {
				t.Fatal(err)
			}
		}
		kept := func(name string) []int {
			t.Helper()
			rows, err := p.st.ListHostFileVersions(p.ctx, host.ID, name)
			if err != nil {
				t.Fatal(err)
			}
			out := make([]int, len(rows))
			for i, r := range rows {
				out[i] = r.Version
			}
			return out
		}
		for range 50 {
			save("a", 0)
		}
		if got := kept("a"); len(got) != 50 || got[49] != 1 {
			t.Fatalf("exactly 50 %v", got)
		}
		apply("a", 10, store.HostFileSucceeded) // inside the newest 50
		save("a", 0)                            // v51: v1 goes
		if got := kept("a"); len(got) != 50 || got[49] != 2 {
			t.Fatalf("applied inside %v", got)
		}
		apply("a", 2, store.HostFileUnchanged) // oldest is applied
		save("a", 0)                           // v52: newest 3..52, v2 stays
		if got := kept("a"); len(got) != 51 || got[50] != 2 || got[49] != 3 {
			t.Fatalf("applied oldest %v", got)
		}
		apply("a", 52, store.HostFileSucceeded) // applied moves
		save("a", 0)                            // v53: v2 and v3 go
		if got := kept("a"); len(got) != 50 || got[49] != 4 {
			t.Fatalf("applied moved %v", got)
		}
		apply("a", 4, store.HostFileRolledBack) // does not protect
		save("a", 0)                            // v54: v4 goes
		if got := kept("a"); len(got) != 50 || got[49] != 5 {
			t.Fatalf("rolled_back protected %v", got)
		}
		save("a", 5) // v55: an in-flight target is kept
		if got := kept("a"); len(got) != 51 || got[50] != 5 || got[49] != 6 {
			t.Fatalf("protect %v", got)
		}
		save("b", 0)
		if got := kept("b"); len(got) != 1 || got[0] != 1 {
			t.Fatalf("per name %v", got)
		}
		// Concurrent saves on one base: one wins, the other is a conflict, versions stay contiguous.
		zero := 0
		errs := make(chan error, 2)
		for range 2 {
			go func() {
				_, err := p.st.SaveHostFileVersion(p.ctx, plainSave(p, host, owner, "c", "x\n", &zero))
				errs <- err
			}()
		}
		e1, e2 := <-errs, <-errs
		if (e1 == nil) == (e2 == nil) || (!errors.Is(e1, store.ErrConflict) && !errors.Is(e2, store.ErrConflict)) {
			t.Fatalf("concurrent %v %v", e1, e2)
		}
		one := 1
		if v, err := p.st.SaveHostFileVersion(p.ctx, plainSave(p, host, owner, "c", "y\n", &one)); err != nil || v != 2 {
			t.Fatalf("next %d %v", v, err)
		}
	})
}

// TestHostFilesAsAPIRole runs every 0029 write and read as deploybot_api.
func TestHostFilesAsAPIRole(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	apiURL := os.Getenv("DEPLOYBOT_TEST_API_DATABASE_URL")
	if apiURL == "" {
		t.Fatal("DEPLOYBOT_TEST_API_DATABASE_URL is not set: give the deploybot_api login on the same test database")
	}
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
	api, err := Open(ctx, apiURL)
	if err != nil {
		t.Fatal("api login connect failed")
	}
	defer api.Close()
	var role string
	if err := api.pool.QueryRow(ctx, `SELECT current_user`).Scan(&role); err != nil || role != "deploybot_api" {
		t.Fatalf("api login is %s %v", role, err)
	}
	p := parity{t: t, st: d, ctx: ctx, at: time.Now().UTC().Truncate(time.Microsecond), tag: strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")}
	owner := p.user("hf-api")
	org := p.org(owner.ID)
	host := p.enroll(org, nil, owner.ID, "hfapi")
	if err := api.RecordHostFileApply(ctx, store.HostFileApply{HostID: host.ID, Name: "dd-policy", Version: 1, Outcome: store.HostFileSucceeded, By: owner.ID, At: p.at}); err != nil {
		t.Fatalf("apply as deploybot_api: %v", err)
	}
	// 51 saves: the prune (DELETE) runs as deploybot_api too.
	for range 51 {
		if _, err := api.SaveHostFileVersion(ctx, plainSave(p, host, owner, "dd-policy", "x\n", nil)); err != nil {
			t.Fatalf("save as deploybot_api: %v", err)
		}
	}
	if _, err := api.SaveHostFileVersion(ctx, secretSave(p, host, owner, "dd-secrets", 8)); err != nil {
		t.Fatalf("secret save as deploybot_api: %v", err)
	}
	if rows, err := api.ListHostFileVersions(ctx, host.ID, "dd-policy"); err != nil || len(rows) != 51 {
		t.Fatalf("list as deploybot_api: %d %v", len(rows), err)
	}
	if _, err := api.GetHostFileVersion(ctx, host.ID, "dd-secrets", 0); err != nil {
		t.Fatalf("get as deploybot_api: %v", err)
	}
	if _, err := api.HostFileStates(ctx, host.ID); err != nil {
		t.Fatalf("states as deploybot_api: %v", err)
	}
	if err := api.SetHostFilesStatus(ctx, host.ID, store.HostFilesStatus{At: p.at, Files: []store.HostFileStatus{}}); err != nil {
		t.Fatalf("status as deploybot_api: %v", err)
	}
	if _, err := api.GetHostFilesStatus(ctx, host.ID); err != nil {
		t.Fatalf("status read as deploybot_api: %v", err)
	}
	if _, err := api.ListHostFilesStatuses(ctx); err != nil {
		t.Fatalf("status list as deploybot_api: %v", err)
	}
}

// TestParityHostFileProtectUnderLock is re-review R5: Protect runs under
// the save's per-file lock, and a version read that starts meanwhile waits
// for the save, so it never sees a version the save is about to prune.
func TestParityHostFileProtectUnderLock(t *testing.T) {
	runParity(t, func(p parity) {
		t := p.t
		owner := p.user("hfl")
		host := p.enroll(p.org(owner.ID), nil, owner.ID, "hfl")
		for range store.HostFileKeep {
			if _, err := p.st.SaveHostFileVersion(p.ctx, plainSave(p, host, owner, "a", "x\n", nil)); err != nil {
				t.Fatal(err)
			}
		}
		read := make(chan error, 1)
		in := plainSave(p, host, owner, "a", "x\n", nil)
		in.Protect = func() int {
			// An apply begins now and reads v1: it must wait for this save.
			go func() {
				_, err := p.st.GetHostFileVersion(p.ctx, host.ID, "a", 1)
				read <- err
			}()
			time.Sleep(150 * time.Millisecond)
			return 0
		}
		if _, err := p.st.SaveHostFileVersion(p.ctx, in); err != nil {
			t.Fatal(err)
		}
		if err := <-read; !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("read during the save saw v1: %v", err)
		}
	})
}
