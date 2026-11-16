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
	"github.com/more-than-code/bot-control-plane/api/internal/memstore"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
	"github.com/more-than-code/bot-control-plane/api/migrations"
)

func TestValueRequestsMigrationText(t *testing.T) {
	body, err := migrations.FS.ReadFile("0018_env_value_requests.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"CREATE TABLE app.env_value_requests",
		"REFERENCES app.users (id) ON DELETE CASCADE",
		"REFERENCES app.projects (id) ON DELETE CASCADE",
		"REFERENCES app.env_sets (project_id, name) ON DELETE CASCADE",
		"GRANT UPDATE (state, done_at, expires_at) ON TABLE app.env_value_requests TO deploybot_api",
		"CREATE INDEX env_value_requests_expires_idx",
		"CREATE INDEX env_value_requests_set_idx",
		"CREATE INDEX env_value_requests_open_idx",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("0018 missing %s", want)
		}
	}
	// --migrate splits on semicolons outside quotes and does not skip
	// comments: a ; or ' in a comment would break the file.
	if stmts := splitSQL(text); len(stmts) != 7 || !strings.HasPrefix(stmts[0], "--") || !strings.Contains(stmts[0], "CREATE TABLE app.env_value_requests") {
		t.Fatalf("0018 splits into %d statements, want 7", len(stmts))
	}
	// No value column (api-v1 v1.28 item 8).
	for _, col := range []string{"\n    value ", "nonce", "ciphertext"} {
		if strings.Contains(text, col) {
			t.Fatalf("0018 has %q", col)
		}
	}
	down, err := migrations.FS.ReadFile("0018_env_value_requests.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downText := string(down)
	if !strings.Contains(downText, "DELETE FROM app.schema_migrations WHERE name = '0018_env_value_requests.sql'") ||
		!strings.Contains(downText, "DROP TABLE IF EXISTS app.env_value_requests;") ||
		!strings.Contains(downText, "\nBEGIN;") || !strings.HasSuffix(strings.TrimSpace(downText), "COMMIT;") {
		t.Fatal("down script")
	}
}

// TestValueRequestsMigration runs 0018 up, down, and up again on a test database.
func TestValueRequestsMigration(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() { restoreValueRequests(url) })
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
	assertValueRequestTable(t, d, ctx, true)
	assertTablePrivs(t, d, ctx, "env_value_requests", true, true, false, true, []string{"done_at", "expires_at", "state"})

	// The CHECKs refuse a bad state and a pending row with done_at.
	at := time.Now().UTC().Truncate(time.Second)
	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	owner := catalogUser(t, d, ctx, "vr-mig-"+tag, at)
	home := catalogProject(t, d, ctx, owner.ID, "VRMig", "vrmig"+tag, at)
	if _, err := d.PutEnvSet(ctx, home.ID, "svc", []store.EnvSecret{{Key: "A", Plain: "a"}}, owner.ID, at, false); err != nil {
		t.Fatal(err)
	}
	_, hash, _ := secret.NewToken()
	if err := d.CreateValueRequest(ctx, hash, store.ValueRequest{RequestMeta: store.RequestMeta{UserID: owner.ID, ProjectID: home.ID, CreatedAt: at, ExpiresAt: at.Add(store.RequestTTL)}, Set: "svc", Key: "NEW"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE app.env_value_requests SET state = 'expired' WHERE id_hash = $1`, hash); !checkViolation(err) {
		t.Fatalf("stored expired %v", err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE app.env_value_requests SET done_at = now() WHERE id_hash = $1`, hash); !checkViolation(err) {
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
	down, err := migrations.FS.ReadFile("0018_env_value_requests.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitSQL(string(down)) {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("down: %v", err)
		}
	}
	assertValueRequestTable(t, d, ctx, false)
	// Values stay in their sets.
	if set, err := d.GetEnvSet(ctx, home.ID, "svc"); err != nil || len(set.Keys) != 1 {
		t.Fatalf("set after down %#v %v", set, err)
	}
	if _, err := conn.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '0018_env_value_requests.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("restore migrate: %v", err)
	}
	assertValueRequestTable(t, d, ctx, true)
	assertTablePrivs(t, d, ctx, "env_value_requests", true, true, false, true, []string{"done_at", "expires_at", "state"})
}

func assertValueRequestTable(t *testing.T, d *DB, ctx context.Context, applied bool) {
	t.Helper()
	var table *string
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('app.env_value_requests')::text`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if applied != (table != nil) {
		t.Fatalf("0018 applied %v, table %v", applied, table)
	}
	if !applied {
		return
	}
	var valueCols int
	if err := d.pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = 'app' AND table_name = 'env_value_requests'
  AND column_name IN ('value', 'nonce', 'ciphertext', 'id')
`).Scan(&valueCols); err != nil || valueCols != 0 {
		t.Fatalf("value or id columns %d %v", valueCols, err)
	}
}

// restoreValueRequests re-applies 0018 when a failed run left it rolled back.
func restoreValueRequests(url string) {
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
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('app.env_value_requests')::text`).Scan(&table); err != nil || table != nil {
		return
	}
	if _, err := d.pool.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '0018_env_value_requests.sql'`); err != nil {
		return
	}
	_ = Migrate(ctx, url)
}

// markNeedsValue turns an existing key into a placeholder. The v1.27
// item 4 create route is covered by its own tests; these store tests set the flag directly.
func markNeedsValue(t *testing.T, st store.Store, projectID uuid.UUID, set, key string) {
	t.Helper()
	switch s := st.(type) {
	case *memstore.Mem:
		if !s.SetNeedsValue(projectID, set, key) {
			t.Fatal("no key")
		}
	case *DB:
		tag, err := s.pool.Exec(context.Background(), `
UPDATE app.env_values SET needs_value = true
WHERE project_id = $1::uuid AND set_name = $2 AND key = $3
`, projectID.String(), set, key)
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("placeholder %v", err)
		}
	default:
		t.Fatalf("store %T", st)
	}
}

// TestParityValueRequests is api-v1 v1.28 items 2 and 5–7 in both stores.
func TestParityValueRequests(t *testing.T) {
	box, err := seal.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	runParity(t, func(p parity) {
		t := p.t
		owner, other := p.user("vro"), p.user("vrx")
		proj := p.project(p.org(owner.ID), owner.ID, "vr")
		if _, err := p.st.PutEnvSet(p.ctx, proj.ID, "svc", []store.EnvSecret{{Key: "HAS", Plain: "v"}, {Key: "PH", Plain: "x"}, {Key: "PH2", Plain: "x"}}, owner.ID, p.at, false); err != nil {
			t.Fatal(err)
		}
		markNeedsValue(t, p.st, proj.ID, "svc", "PH")
		markNeedsValue(t, p.st, proj.ID, "svc", "PH2")
		put := func(key, plain string, generated bool) store.EnvKeyPut {
			nonce, ciphertext, err := box.Seal(seal.EnvAAD(proj.ID.String(), "svc", key), []byte(plain))
			if err != nil {
				t.Fatal(err)
			}
			return store.EnvKeyPut{Key: key, Nonce: nonce, Ciphertext: ciphertext, KeyVersion: seal.KeyVersion, Generated: generated}
		}
		create := func(user uuid.UUID, set, key string, at time.Time) ([]byte, error) {
			_, hash, _ := secret.NewToken()
			return hash, p.st.CreateValueRequest(p.ctx, hash, store.ValueRequest{
				RequestMeta: store.RequestMeta{UserID: user, ProjectID: proj.ID, Reason: "why", CreatedAt: at, ExpiresAt: at.Add(store.RequestTTL)},
				Set:         set,
				Key:         key,
			})
		}
		mustCreate := func(key string) []byte {
			h, err := create(owner.ID, "svc", key, p.at)
			if err != nil {
				t.Fatal(err)
			}
			return h
		}
		keyOf := func(key string) store.EnvKey {
			set, err := p.st.GetEnvSet(p.ctx, proj.ID, "svc")
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range set.Keys {
				if k.Key == key {
					return k
				}
			}
			return store.EnvKey{}
		}

		// Create rules.
		if _, err := create(owner.ID, "svc", "HAS", p.at); !errors.Is(err, store.ErrKeyExists) {
			t.Fatalf("has value %v", err)
		}
		if _, err := create(owner.ID, "nope", "PH", p.at); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("no set %v", err)
		}
		h := mustCreate("PH")
		got, err := p.st.GetValueRequest(p.ctx, h)
		if err != nil || got.UserID != owner.ID || got.Key != "PH" || got.Reason != "why" || got.State != store.RequestPending || got.DoneAt != nil {
			t.Fatalf("get %#v %v", got, err)
		}
		if _, err := p.st.GetValueRequest(p.ctx, secret.Hash("unknown")); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown %v", err)
		}

		// Fill: another user is not_found; the owner writes a sealed secret.
		if _, err := p.st.FillValueRequest(p.ctx, h, other.ID, p.at, put("PH", "typed", false)); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("other user %v", err)
		}
		if keyOf("PH").IsSecret {
			t.Fatal("changed by a refused fill")
		}
		k, err := p.st.FillValueRequest(p.ctx, h, owner.ID, p.at, put("PH", "typed", false))
		if err != nil || !k.IsSecret || k.RevealPending {
			t.Fatalf("fill %#v %v", k, err)
		}
		if ph := keyOf("PH"); !ph.IsSecret || ph.NeedsValue || ph.RevealPending || ph.Value != nil {
			t.Fatalf("PH %#v", ph)
		}
		mats, err := p.st.ListEnvMaterial(p.ctx, proj.ID, "svc")
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range mats {
			if m.Key == "PH" {
				plain, err := box.Open(seal.EnvAAD(proj.ID.String(), "svc", "PH"), m.Nonce, m.Ciphertext)
				if err != nil || string(plain) != "typed" {
					t.Fatalf("open %v", err)
				}
			}
		}
		if got, _ := p.st.GetValueRequest(p.ctx, h); got.State != store.RequestFilled || got.DoneAt == nil {
			t.Fatalf("after fill %#v", got)
		}
		if _, err := p.st.FillValueRequest(p.ctx, h, owner.ID, p.at, put("PH", "again", false)); !errors.Is(err, store.ErrRequestUsed) {
			t.Fatalf("second fill %v", err)
		}
		if err := p.st.DeclineValueRequest(p.ctx, h, owner.ID, p.at); !errors.Is(err, store.ErrRequestUsed) {
			t.Fatalf("decline filled %v", err)
		}

		// Generated fill of a new key: secret, pending reveal, revealable once.
		g := mustCreate("GEN")
		if k, err := p.st.FillValueRequest(p.ctx, g, owner.ID, p.at, put("GEN", "generated-value", true)); err != nil || !k.RevealPending {
			t.Fatalf("generated %#v %v", k, err)
		}
		if gen := keyOf("GEN"); !gen.IsSecret || !gen.RevealPending || !gen.Generated {
			t.Fatalf("GEN %#v", gen)
		}
		if _, err := p.st.RevealEnvKey(p.ctx, proj.ID, "svc", "GEN"); err != nil {
			t.Fatal(err)
		}

		// Expiry: at the expiry time the request is expired.
		e := mustCreate("PH2")
		late := p.at.Add(store.RequestTTL)
		if _, err := p.st.FillValueRequest(p.ctx, e, owner.ID, late, put("PH2", "late", false)); !errors.Is(err, store.ErrRequestExpired) {
			t.Fatalf("expired fill %v", err)
		}
		if err := p.st.DeclineValueRequest(p.ctx, e, owner.ID, late); !errors.Is(err, store.ErrRequestExpired) {
			t.Fatalf("expired decline %v", err)
		}
		if !keyOf("PH2").NeedsValue {
			t.Fatal("expired fill wrote")
		}

		// A key that got a plain value meanwhile: conflict, request stays
		// pending (a secret value would be replaced, api-v1 v1.33 item 3).
		m := mustCreate("MEANWHILE")
		plainPut := put("MEANWHILE", "console", false)
		plainPut.Secret = new(bool)
		if _, _, err := p.st.PutEnvKey(p.ctx, proj.ID, "svc", plainPut, owner.ID, p.at); err != nil {
			t.Fatal(err)
		}
		if _, err := p.st.FillValueRequest(p.ctx, m, owner.ID, p.at, put("MEANWHILE", "agent", false)); !errors.Is(err, store.ErrKeyExists) {
			t.Fatalf("meanwhile %v", err)
		}
		if got, _ := p.st.GetValueRequest(p.ctx, m); got.State != store.RequestPending {
			t.Fatalf("meanwhile state %s", got.State)
		}

		// Fill and decline race: exactly one wins.
		race := mustCreate("RACE")
		var wg sync.WaitGroup
		var mu sync.Mutex
		wins := 0
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(fill bool) {
				defer wg.Done()
				var err error
				if fill {
					_, err = p.st.FillValueRequest(p.ctx, race, owner.ID, p.at, put("RACE", "r", false))
				} else {
					err = p.st.DeclineValueRequest(p.ctx, race, owner.ID, p.at)
				}
				if err == nil {
					mu.Lock()
					wins++
					mu.Unlock()
				} else if !errors.Is(err, store.ErrRequestUsed) {
					t.Errorf("race %v", err)
				}
			}(i == 0)
		}
		wg.Wait()
		if wins != 1 {
			t.Fatalf("race wins %d", wins)
		}

		// Decline.
		dcl := mustCreate("DECL")
		if err := p.st.DeclineValueRequest(p.ctx, dcl, other.ID, p.at); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("other decline %v", err)
		}
		if err := p.st.DeclineValueRequest(p.ctx, dcl, owner.ID, p.at); err != nil {
			t.Fatal(err)
		}
		if got, _ := p.st.GetValueRequest(p.ctx, dcl); got.State != store.RequestDeclined || got.DoneAt == nil {
			t.Fatalf("declined %#v", got)
		}

		// Open limit per user: owner has MEANWHILE, PH2 (expired) and others used.
		limited := p.user("vrl")
		lp := p.project(p.org(limited.ID), limited.ID, "vrl")
		if _, err := p.st.PutEnvSet(p.ctx, lp.ID, "svc", nil, limited.ID, p.at, false); err != nil {
			t.Fatal(err)
		}
		var firstK []byte
		for i := 0; i < store.MaxOpenRequests; i++ {
			_, hash, _ := secret.NewToken()
			if i == 0 {
				firstK = hash
			}
			if err := p.st.CreateValueRequest(p.ctx, hash, store.ValueRequest{RequestMeta: store.RequestMeta{UserID: limited.ID, ProjectID: lp.ID, CreatedAt: p.at, ExpiresAt: p.at.Add(store.RequestTTL)}, Set: "svc", Key: fmt.Sprintf("K%d", i)}); err != nil {
				t.Fatal(err)
			}
		}
		_, hash, _ := secret.NewToken()
		over := store.ValueRequest{RequestMeta: store.RequestMeta{UserID: limited.ID, ProjectID: lp.ID, CreatedAt: p.at, ExpiresAt: p.at.Add(store.RequestTTL)}, Set: "svc", Key: "K"}
		if err := p.st.CreateValueRequest(p.ctx, hash, over); !errors.Is(err, store.ErrTooManyRequests) {
			t.Fatalf("limit %v", err)
		}
		// The refused create changed nothing; a create for an open key
		// replaces it (one per user and key) and is not counted (rev 3).
		if got, _ := p.st.GetValueRequest(p.ctx, firstK); got.StateAt(p.at) != store.RequestPending {
			t.Fatalf("refused create expired K0: %s", got.StateAt(p.at))
		}
		_, again, _ := secret.NewToken()
		if err := p.st.CreateValueRequest(p.ctx, again, store.ValueRequest{RequestMeta: store.RequestMeta{UserID: limited.ID, ProjectID: lp.ID, CreatedAt: p.at, ExpiresAt: p.at.Add(store.RequestTTL)}, Set: "svc", Key: "K0"}); err != nil {
			t.Fatalf("replace at limit %v", err)
		}
		if got, _ := p.st.GetValueRequest(p.ctx, firstK); got.StateAt(p.at) != store.RequestExpired {
			t.Fatalf("K0 not replaced: %s", got.StateAt(p.at))
		}
		if _, err := p.st.FillValueRequest(p.ctx, firstK, limited.ID, p.at, store.EnvKeyPut{Key: "K0"}); !errors.Is(err, store.ErrRequestExpired) {
			t.Fatalf("replaced fill %v", err)
		}
		over.CreatedAt, over.ExpiresAt = late, late.Add(store.RequestTTL)
		if err := p.st.CreateValueRequest(p.ctx, hash, over); err != nil {
			t.Fatalf("after expiry %v", err)
		}

		// Rows 7 days past expiry are removed by the next create.
		week := p.at.Add(2*store.RequestTTL + 7*24*time.Hour + time.Minute)
		_, fresh, _ := secret.NewToken()
		if err := p.st.CreateValueRequest(p.ctx, fresh, store.ValueRequest{RequestMeta: store.RequestMeta{UserID: limited.ID, ProjectID: lp.ID, CreatedAt: week, ExpiresAt: week.Add(store.RequestTTL)}, Set: "svc", Key: "K"}); err != nil {
			t.Fatal(err)
		}
		if _, err := p.st.GetValueRequest(p.ctx, hash); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("old row kept %v", err)
		}
		hash = fresh

		// A fill racing a set delete: one of them wins, no other error (no deadlock).
		if _, err := p.st.PutEnvSet(p.ctx, lp.ID, "race", nil, limited.ID, week, false); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5; i++ {
			_, rh, _ := secret.NewToken()
			if err := p.st.CreateValueRequest(p.ctx, rh, store.ValueRequest{RequestMeta: store.RequestMeta{UserID: limited.ID, ProjectID: lp.ID, CreatedAt: week, ExpiresAt: week.Add(store.RequestTTL)}, Set: "race", Key: "R"}); err != nil {
				t.Fatal(err)
			}
			nonce, ciphertext, err := box.Seal(seal.EnvAAD(lp.ID.String(), "race", "R"), []byte("r"))
			if err != nil {
				t.Fatal(err)
			}
			var fillErr, delErr error
			var rw sync.WaitGroup
			rw.Add(2)
			go func() {
				defer rw.Done()
				_, fillErr = p.st.FillValueRequest(p.ctx, rh, limited.ID, week, store.EnvKeyPut{Key: "R", Nonce: nonce, Ciphertext: ciphertext, KeyVersion: seal.KeyVersion})
			}()
			go func() {
				defer rw.Done()
				delErr = p.st.DeleteEnvSet(p.ctx, lp.ID, "race")
			}()
			rw.Wait()
			if delErr != nil || (fillErr != nil && !errors.Is(fillErr, store.ErrNotFound)) {
				t.Fatalf("race fill %v delete %v", fillErr, delErr)
			}
			if _, err := p.st.PutEnvSet(p.ctx, lp.ID, "race", nil, limited.ID, week, false); err != nil {
				t.Fatal(err)
			}
		}

		// Deleting the set removes its requests; a new set of that name does not revive them.
		if err := p.st.DeleteEnvSet(p.ctx, lp.ID, "svc"); err != nil {
			t.Fatal(err)
		}
		if _, err := p.st.PutEnvSet(p.ctx, lp.ID, "svc", nil, limited.ID, p.at, false); err != nil {
			t.Fatal(err)
		}
		if _, err := p.st.GetValueRequest(p.ctx, hash); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("after set delete %v", err)
		}
	})
}

// TestValueRequestsAsAPIRole runs create, replace, prune, fill and decline
// through a deploybot_api login, the way the server connects, so a missing
// column grant fails here (review 2026-11-15: the replace step updates
// expires_at). DEPLOYBOT_TEST_API_DATABASE_URL is that login on the same
// test database; with the main URL set it is required (the gate is 0 SKIP).
func TestValueRequestsAsAPIRole(t *testing.T) {
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
	owner := catalogUser(t, d, ctx, "vr-api-"+tag, at)
	home := catalogProject(t, d, ctx, owner.ID, "VRApi", "vrapi"+tag, at)
	if _, err := d.PutEnvSet(ctx, home.ID, "svc", nil, owner.ID, at, false); err != nil {
		t.Fatal(err)
	}
	box, err := seal.New(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	create := func(key string, when time.Time) []byte {
		t.Helper()
		_, hash, _ := secret.NewToken()
		if err := api.CreateValueRequest(ctx, hash, store.ValueRequest{RequestMeta: store.RequestMeta{UserID: owner.ID, ProjectID: home.ID, CreatedAt: when, ExpiresAt: when.Add(store.RequestTTL)}, Set: "svc", Key: key}); err != nil {
			t.Fatalf("create %s as deploybot_api: %v", key, err)
		}
		return hash
	}
	first := create("TOKEN", at)
	second := create("TOKEN", at) // replaces first: UPDATE expires_at
	if got, _ := api.GetValueRequest(ctx, first); got.StateAt(at) != store.RequestExpired {
		t.Fatalf("first not replaced: %s", got.StateAt(at))
	}
	nonce, ciphertext, err := box.Seal(seal.EnvAAD(home.ID.String(), "svc", "TOKEN"), []byte("v"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.FillValueRequest(ctx, second, owner.ID, at, store.EnvKeyPut{Key: "TOKEN", Nonce: nonce, Ciphertext: ciphertext, KeyVersion: seal.KeyVersion}); err != nil {
		t.Fatalf("fill as deploybot_api: %v", err)
	}
	other := create("OTHER", at)
	if err := api.DeclineValueRequest(ctx, other, owner.ID, at); err != nil {
		t.Fatalf("decline as deploybot_api: %v", err)
	}
	// The prune of rows 7 days past expiry (DELETE ... FOR UPDATE SKIP LOCKED).
	later := at.Add(2*store.RequestTTL + 7*24*time.Hour + time.Minute)
	create("LATER", later)
	if _, err := api.GetValueRequest(ctx, first); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old row kept as deploybot_api: %v", err)
	}
}
