package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
	"github.com/more-than-code/bot-control-plane/api/migrations"
)

// Connect requests (api-v1 v1.31, migration 0021).

const connectMigration = "0021_registry_connect_requests.sql"

func TestConnectRequestsMigrationText(t *testing.T) {
	if !numberedSQL(connectMigration) || numberedSQL("0021_registry_connect_requests.down.sql") {
		t.Fatal("0021 numbering")
	}
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(names, ","), connectMigration) {
		t.Fatalf("migrations = %v", names)
	}
	body, err := migrations.FS.ReadFile(connectMigration)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"CREATE TABLE app.registry_connect_requests",
		"REFERENCES app.users (id) ON DELETE CASCADE",
		"REFERENCES app.projects (id) ON DELETE CASCADE",
		"CHECK (state IN ('pending', 'connected', 'declined'))",
		"GRANT SELECT, INSERT, DELETE ON TABLE app.registry_connect_requests TO deploybot_api",
		"GRANT UPDATE (state, done_at, expires_at) ON TABLE app.registry_connect_requests TO deploybot_api",
		"CREATE INDEX registry_connect_requests_open_idx",
		"CREATE INDEX registry_connect_requests_expires_idx",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("0021 missing %s", want)
		}
	}
	// --migrate splits on semicolons outside quotes and does not skip
	// comments: a ; or ' in a comment would break the file.
	for _, line := range strings.Split(text, "\n") {
		if trim := strings.TrimSpace(line); strings.HasPrefix(trim, "--") && strings.ContainsAny(trim, ";'") {
			t.Fatalf("comment contains a semicolon or an apostrophe: %s", trim)
		}
	}
	// The table, two indexes, the revoke and two grants.
	if stmts := splitSQL(text); len(stmts) != 6 || !strings.Contains(stmts[0], "CREATE TABLE app.registry_connect_requests") {
		t.Fatalf("0021 splits into %d statements, want 6", len(stmts))
	}
	// No token column (item 9).
	for _, col := range []string{"token", "nonce", "ciphertext"} {
		if strings.Contains(strings.SplitN(text, "CREATE TABLE", 2)[1], "\n    "+col) {
			t.Fatalf("0021 has %q", col)
		}
	}
	down, err := migrations.FS.ReadFile("0021_registry_connect_requests.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downText := string(down)
	if !strings.Contains(downText, "DELETE FROM app.schema_migrations WHERE name = '0021_registry_connect_requests.sql'") ||
		!strings.Contains(downText, "DROP TABLE IF EXISTS app.registry_connect_requests;") ||
		!strings.Contains(downText, "\nBEGIN;") || !strings.HasSuffix(strings.TrimSpace(downText), "COMMIT;") {
		t.Fatal("down script")
	}
}

// TestConnectRequestsMigration runs 0021 up, down, and up again on a test database.
func TestConnectRequestsMigration(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() { restoreConnectRequests(url) })
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
	assertConnectRequestTable(t, d, ctx, true)
	assertTablePrivs(t, d, ctx, "registry_connect_requests", true, true, false, true, []string{"done_at", "expires_at", "state"})

	// The CHECKs refuse a stored expired state and a pending row with done_at.
	at := time.Now().UTC().Truncate(time.Second)
	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	owner := catalogUser(t, d, ctx, "cr-mig-"+tag, at)
	home := catalogProject(t, d, ctx, owner.ID, "CRMig", "crmig"+tag, at)
	_, hash, _ := secret.NewToken()
	if err := d.CreateConnectRequest(ctx, hash, store.ConnectRequest{RequestMeta: store.RequestMeta{UserID: owner.ID, ProjectID: home.ID, CreatedAt: at, ExpiresAt: at.Add(store.RequestTTL)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE app.registry_connect_requests SET state = 'expired' WHERE id_hash = $1`, hash); !checkViolation(err) {
		t.Fatalf("stored expired %v", err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE app.registry_connect_requests SET done_at = now() WHERE id_hash = $1`, hash); !checkViolation(err) {
		t.Fatalf("pending done_at %v", err)
	}

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal("connect failed")
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `SET ROLE deploybot_owner`); err != nil {
		t.Fatal(err)
	}
	down, err := migrations.FS.ReadFile("0021_registry_connect_requests.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitSQL(string(down)) {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("down: %v", err)
		}
	}
	assertConnectRequestTable(t, d, ctx, false)
	if _, err := conn.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '`+connectMigration+`'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("restore migrate: %v", err)
	}
	assertConnectRequestTable(t, d, ctx, true)
	assertTablePrivs(t, d, ctx, "registry_connect_requests", true, true, false, true, []string{"done_at", "expires_at", "state"})
}

func assertConnectRequestTable(t *testing.T, d *DB, ctx context.Context, applied bool) {
	t.Helper()
	var table *string
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('app.registry_connect_requests')::text`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if applied != (table != nil) {
		t.Fatalf("0021 applied %v, table %v", applied, table)
	}
	if !applied {
		return
	}
	var tokenCols int
	if err := d.pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = 'app' AND table_name = 'registry_connect_requests'
  AND column_name IN ('token', 'nonce', 'ciphertext', 'id')
`).Scan(&tokenCols); err != nil || tokenCols != 0 {
		t.Fatalf("token or id columns %d %v", tokenCols, err)
	}
}

// restoreConnectRequests re-applies 0021 when a failed run left it rolled back.
func restoreConnectRequests(url string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	d, err := Open(ctx, url)
	if err != nil {
		return
	}
	defer d.Close()
	if !onTestDatabase(ctx, d) {
		return
	}
	var table *string
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('app.registry_connect_requests')::text`).Scan(&table); err != nil || table != nil {
		return
	}
	if _, err := d.pool.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '`+connectMigration+`'`); err != nil {
		return
	}
	_ = Migrate(ctx, url)
}

// TestParityConnectRequests is api-v1 v1.31 items 2, 6–8 in both stores.
func TestParityConnectRequests(t *testing.T) {
	box, err := seal.New(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	runParity(t, func(p parity) {
		t := p.t
		owner, other := p.user("cro"), p.user("crx")
		proj := p.project(p.org(owner.ID), owner.ID, "cr")
		second := p.project(p.org(owner.ID), owner.ID, "cr2")
		create := func(user, project uuid.UUID, at time.Time) ([]byte, error) {
			_, hash, _ := secret.NewToken()
			return hash, p.st.CreateConnectRequest(p.ctx, hash, store.ConnectRequest{RequestMeta: store.RequestMeta{
				UserID: user, ProjectID: project, Reason: "why", CreatedAt: at, ExpiresAt: at.Add(store.RequestTTL),
			}})
		}
		mustCreate := func(user, project uuid.UUID, at time.Time) []byte {
			t.Helper()
			h, err := create(user, project, at)
			if err != nil {
				t.Fatal(err)
			}
			return h
		}
		write := func(login string) store.RegistryWrite {
			nonce, ciphertext, err := box.Seal(seal.RegistryAAD(proj.ID.String(), store.GitHubRegistry), []byte("gho_planted"))
			if err != nil {
				t.Fatal(err)
			}
			by := owner.ID
			return store.RegistryWrite{Username: login, Nonce: nonce, Ciphertext: ciphertext, KeyVersion: seal.KeyVersion, Source: store.RegistrySourceGitHub, ConnectedBy: &by}
		}
		stateOf := func(h []byte, at time.Time) string {
			t.Helper()
			r, err := p.st.GetConnectRequest(p.ctx, h)
			if err != nil {
				t.Fatal(err)
			}
			return r.StateAt(at)
		}

		h := mustCreate(owner.ID, proj.ID, p.at)
		got, err := p.st.GetConnectRequest(p.ctx, h)
		if err != nil || got.UserID != owner.ID || got.ProjectID != proj.ID || got.Reason != "why" || got.State != store.RequestPending || got.DoneAt != nil {
			t.Fatalf("get %#v %v", got, err)
		}
		if _, err := p.st.GetConnectRequest(p.ctx, secret.Hash("unknown")); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown %v", err)
		}
		if _, err := create(owner.ID, uuid.New(), p.at); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("no project %v", err)
		}

		// Connect: only the bound request of the actor and project is marked.
		h2 := mustCreate(owner.ID, second.ID, p.at)
		reg, err := p.st.ConnectRegistry(p.ctx, proj.ID, store.GitHubRegistry, write("octo"), other.ID, p.at, h)
		if err != nil || reg.Username != "octo" {
			t.Fatalf("another actor %#v %v", reg, err)
		}
		if stateOf(h, p.at) != store.RequestPending {
			t.Fatal("marked for another actor")
		}
		if _, err := p.st.ConnectRegistry(p.ctx, proj.ID, store.GitHubRegistry, write("octo"), owner.ID, p.at, h2); err != nil || stateOf(h2, p.at) != store.RequestPending {
			t.Fatalf("another project's request %v", err)
		}
		if _, err := p.st.ConnectRegistry(p.ctx, proj.ID, store.GitHubRegistry, write("octo"), owner.ID, p.at, h); err != nil {
			t.Fatalf("connect %v", err)
		}
		if r, _ := p.st.GetConnectRequest(p.ctx, h); r.State != store.RequestConnected || r.DoneAt == nil {
			t.Fatalf("after connect %#v", r)
		}
		if stateOf(h2, p.at) != store.RequestPending {
			t.Fatal("the other request was marked")
		}
		// Single use: a second connect writes the login and leaves it connected.
		before, _ := p.st.GetConnectRequest(p.ctx, h)
		if _, err := p.st.ConnectRegistry(p.ctx, proj.ID, store.GitHubRegistry, write("octo2"), owner.ID, p.at.Add(time.Second), h); err != nil {
			t.Fatalf("second connect %v", err)
		}
		if again, _ := p.st.GetConnectRequest(p.ctx, h); again.State != store.RequestConnected || !again.DoneAt.Equal(*before.DoneAt) {
			t.Fatalf("second connect changed the request %#v", again)
		}
		regs, err := p.st.ListRegistries(p.ctx, proj.ID)
		if err != nil || len(regs) != 1 || regs[0].Username != "octo2" || regs[0].Source != store.RegistrySourceGitHub {
			t.Fatalf("registries %#v %v", regs, err)
		}
		// An unknown request still writes the login.
		if _, err := p.st.ConnectRegistry(p.ctx, proj.ID, store.GitHubRegistry, write("octo3"), owner.ID, p.at, secret.Hash("nope")); err != nil {
			t.Fatalf("unknown request %v", err)
		}
		if regs, _ := p.st.ListRegistries(p.ctx, proj.ID); len(regs) != 1 || regs[0].Username != "octo3" {
			t.Fatalf("unknown request did not write %#v", regs)
		}
		if err := p.st.DeclineConnectRequest(p.ctx, h, owner.ID, p.at); !errors.Is(err, store.ErrRequestUsed) {
			t.Fatalf("decline connected %v", err)
		}

		// Expiry: at the expiry time it is expired; a connect then does not mark it.
		e := mustCreate(owner.ID, second.ID, p.at) // replaces h2
		if stateOf(h2, p.at) != store.RequestExpired {
			t.Fatal("h2 not replaced")
		}
		late := p.at.Add(store.RequestTTL)
		if err := p.st.DeclineConnectRequest(p.ctx, e, owner.ID, late); !errors.Is(err, store.ErrRequestExpired) {
			t.Fatalf("expired decline %v", err)
		}
		if _, err := p.st.ConnectRegistry(p.ctx, second.ID, store.GitHubRegistry, write("late"), owner.ID, late, e); err != nil || stateOf(e, late) != store.RequestExpired {
			t.Fatalf("expired connect %v", err)
		}

		// Decline.
		dcl := mustCreate(owner.ID, proj.ID, p.at)
		if err := p.st.DeclineConnectRequest(p.ctx, dcl, other.ID, p.at); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("other decline %v", err)
		}
		if err := p.st.DeclineConnectRequest(p.ctx, dcl, owner.ID, p.at); err != nil {
			t.Fatal(err)
		}
		if r, _ := p.st.GetConnectRequest(p.ctx, dcl); r.State != store.RequestDeclined || r.DoneAt == nil {
			t.Fatalf("declined %#v", r)
		}

		// Connect and decline race: the decline either wins (the request
		// stays declined) or the connect marks it; never both.
		race := mustCreate(owner.ID, proj.ID, p.at)
		var wg sync.WaitGroup
		var declineErr error
		var connectErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, connectErr = p.st.ConnectRegistry(p.ctx, proj.ID, store.GitHubRegistry, write("race"), owner.ID, p.at, race)
		}()
		go func() {
			defer wg.Done()
			declineErr = p.st.DeclineConnectRequest(p.ctx, race, owner.ID, p.at)
		}()
		wg.Wait()
		raceState := stateOf(race, p.at)
		if connectErr != nil || (declineErr != nil && !errors.Is(declineErr, store.ErrRequestUsed)) ||
			(raceState == store.RequestConnected) != (declineErr != nil) {
			t.Fatalf("race state %s connect %v decline %v", raceState, connectErr, declineErr)
		}

		// Open limit per user: 20 projects, the 21st refused, a replace allowed.
		limited := p.user("crl")
		lorg := p.org(limited.ID)
		var first []byte
		var firstProj uuid.UUID
		for i := 0; i < store.MaxOpenRequests; i++ {
			lp := p.project(lorg, limited.ID, fmt.Sprintf("crl%d", i))
			hh := mustCreate(limited.ID, lp.ID, p.at)
			if i == 0 {
				first, firstProj = hh, lp.ID
			}
		}
		over := p.project(lorg, limited.ID, "crlover")
		if _, err := create(limited.ID, over.ID, p.at); !errors.Is(err, store.ErrTooManyConnectRequests) {
			t.Fatalf("limit %v", err)
		}
		if stateOf(first, p.at) != store.RequestPending {
			t.Fatal("refused create changed the first")
		}
		mustCreate(limited.ID, firstProj, p.at)
		if stateOf(first, p.at) != store.RequestExpired {
			t.Fatal("first not replaced at the cap")
		}
		mustCreate(limited.ID, over.ID, late)

		// Rows 7 days past expiry are removed by the next create.
		week := p.at.Add(2*store.RequestTTL + 7*24*time.Hour + time.Minute)
		mustCreate(limited.ID, over.ID, week)
		if _, err := p.st.GetConnectRequest(p.ctx, first); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("old row kept %v", err)
		}

		// A deleted project's requests are gone.
		gone := mustCreate(owner.ID, second.ID, week)
		if err := p.st.DeleteProject(p.ctx, second.ID, week); err != nil {
			t.Fatal(err)
		}
		if _, err := p.st.GetConnectRequest(p.ctx, gone); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("after project delete %v", err)
		}
	})
}

// TestConnectRequestsAsAPIRole runs create, replace, connect, decline and
// prune through a deploybot_api login, the way the server connects, so a
// missing grant fails here. DEPLOYBOT_TEST_API_DATABASE_URL is that login
// on the same test database; with the main URL set it is required.
func TestConnectRequestsAsAPIRole(t *testing.T) {
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
	var role, dbName, mainDB string
	if err := api.pool.QueryRow(ctx, `SELECT current_user, current_database()`).Scan(&role, &dbName); err != nil {
		t.Fatal(err)
	}
	if err := d.pool.QueryRow(ctx, `SELECT current_database()`).Scan(&mainDB); err != nil {
		t.Fatal(err)
	}
	if role != "deploybot_api" || dbName != mainDB {
		t.Fatalf("api login is %s on %s, want deploybot_api on %s", role, dbName, mainDB)
	}

	at := time.Now().UTC().Truncate(time.Second)
	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	owner := catalogUser(t, d, ctx, "cr-api-"+tag, at)
	home := catalogProject(t, d, ctx, owner.ID, "CRApi", "crapi"+tag, at)
	box, err := seal.New(bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	create := func(when time.Time) []byte {
		t.Helper()
		_, hash, _ := secret.NewToken()
		if err := api.CreateConnectRequest(ctx, hash, store.ConnectRequest{RequestMeta: store.RequestMeta{UserID: owner.ID, ProjectID: home.ID, CreatedAt: when, ExpiresAt: when.Add(store.RequestTTL)}}); err != nil {
			t.Fatalf("create as deploybot_api: %v", err)
		}
		return hash
	}
	first := create(at)
	second := create(at) // replaces first: UPDATE expires_at
	if got, _ := api.GetConnectRequest(ctx, first); got.StateAt(at) != store.RequestExpired {
		t.Fatalf("first not replaced: %s", got.StateAt(at))
	}
	nonce, ciphertext, err := box.Seal(seal.RegistryAAD(home.ID.String(), store.GitHubRegistry), []byte("gho_planted"))
	if err != nil {
		t.Fatal(err)
	}
	by := owner.ID
	if _, err := api.ConnectRegistry(ctx, home.ID, store.GitHubRegistry, store.RegistryWrite{
		Username: "octo", Nonce: nonce, Ciphertext: ciphertext, KeyVersion: seal.KeyVersion, Source: store.RegistrySourceGitHub, ConnectedBy: &by,
	}, owner.ID, at, second); err != nil {
		t.Fatalf("connect as deploybot_api: %v", err)
	}
	if got, _ := api.GetConnectRequest(ctx, second); got.State != store.RequestConnected {
		t.Fatalf("not connected as deploybot_api: %s", got.State)
	}
	third := create(at)
	if err := api.DeclineConnectRequest(ctx, third, owner.ID, at); err != nil {
		t.Fatalf("decline as deploybot_api: %v", err)
	}
	later := at.Add(2*store.RequestTTL + 7*24*time.Hour + time.Minute)
	create(later)
	if _, err := api.GetConnectRequest(ctx, first); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old row kept as deploybot_api: %v", err)
	}
}

// TestConnectRequestsConcurrentPostgres runs create (which replaces the
// caller's open request and prunes old rows with SKIP LOCKED) against the
// callback's ConnectRegistry on Postgres: no error, no deadlock, and the
// request ends either connected or replaced, never both. A row the callback
// holds locked is skipped by the prune instead of waited on.
func TestConnectRequestsConcurrentPostgres(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
	box, err := seal.New(bytes.Repeat([]byte{6}, 32))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	owner := catalogUser(t, d, ctx, "cr-conc-"+tag, at)
	home := catalogProject(t, d, ctx, owner.ID, "CRConc", "crconc"+tag, at)
	by := owner.ID
	nonce, ciphertext, err := box.Seal(seal.RegistryAAD(home.ID.String(), store.GitHubRegistry), []byte("gho_planted"))
	if err != nil {
		t.Fatal(err)
	}
	write := store.RegistryWrite{Username: "octo", Nonce: nonce, Ciphertext: ciphertext, KeyVersion: seal.KeyVersion, Source: store.RegistrySourceGitHub, ConnectedBy: &by}
	create := func(when time.Time) ([]byte, error) {
		_, hash, _ := secret.NewToken()
		return hash, d.CreateConnectRequest(ctx, hash, store.ConnectRequest{RequestMeta: store.RequestMeta{UserID: owner.ID, ProjectID: home.ID, CreatedAt: when, ExpiresAt: when.Add(store.RequestTTL)}})
	}
	state := func(hash []byte, when time.Time) string {
		t.Helper()
		r, err := d.GetConnectRequest(ctx, hash)
		if err != nil {
			t.Fatal(err)
		}
		return r.StateAt(when)
	}
	week := 7*24*time.Hour + store.RequestTTL + time.Minute

	// 1. The prune skips a row the callback holds locked, and removes it later.
	old, err := create(at.Add(-week))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockConnectRequest(ctx, tx, old); err != nil {
		t.Fatal(err)
	}
	quick, quickCancel := context.WithTimeout(ctx, 5*time.Second)
	_, err = func() ([]byte, error) {
		_, hash, _ := secret.NewToken()
		return hash, d.CreateConnectRequest(quick, hash, store.ConnectRequest{RequestMeta: store.RequestMeta{UserID: owner.ID, ProjectID: home.ID, CreatedAt: at, ExpiresAt: at.Add(store.RequestTTL)}})
	}()
	quickCancel()
	if err != nil {
		t.Fatalf("create waited on a locked row: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetConnectRequest(ctx, old); err != nil {
		t.Fatalf("a locked row was pruned: %v", err)
	}
	if _, err := create(at); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetConnectRequest(ctx, old); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old row kept after the lock: %v", err)
	}

	// 2. Create (replace + prune) racing ConnectRegistry on the request it
	// replaces and on an old row it prunes.
	for i := 0; i < 10; i++ {
		when := at.Add(time.Duration(i+1) * time.Minute)
		// The stale row first: a create at an earlier time would replace
		// (expire) a request made after it.
		stale, err := create(when.Add(-week))
		if err != nil {
			t.Fatal(err)
		}
		pending, err := create(when)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var connectErr, staleErr, createErr error
		var fresh []byte
		wg.Add(3)
		go func() {
			defer wg.Done()
			_, connectErr = d.ConnectRegistry(ctx, home.ID, store.GitHubRegistry, write, owner.ID, when, pending)
		}()
		go func() {
			defer wg.Done()
			_, staleErr = d.ConnectRegistry(ctx, home.ID, store.GitHubRegistry, write, owner.ID, when, stale)
		}()
		go func() {
			defer wg.Done()
			fresh, createErr = create(when)
		}()
		wg.Wait()
		if connectErr != nil || staleErr != nil || createErr != nil {
			t.Fatalf("round %d: connect %v stale %v create %v", i, connectErr, staleErr, createErr)
		}
		got := state(pending, when)
		if got != store.RequestConnected && got != store.RequestExpired {
			t.Fatalf("round %d: replaced request is %s", i, got)
		}
		if state(fresh, when) != store.RequestPending {
			t.Fatalf("round %d: the new request is not pending", i)
		}
		// The stale row is never marked: either pruned or still expired.
		if r, err := d.GetConnectRequest(ctx, stale); err == nil && r.State != store.RequestPending {
			t.Fatalf("round %d: stale row marked %s", i, r.State)
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
	}
}
