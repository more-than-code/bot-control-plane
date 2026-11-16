package db

import (
	"bytes"
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
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
	"github.com/more-than-code/bot-control-plane/api/migrations"
)

// api-v1 v1.33: entry-private env sets (migration 0022), confirmed env set
// changes and the open-requests list, in both stores.

const privateMigration = "0022_env_private_sets.sql"

func TestEnvPrivateSetsMigrationText(t *testing.T) {
	if !numberedSQL(privateMigration) || numberedSQL("0022_env_private_sets.down.sql") {
		t.Fatal("0022 numbering")
	}
	names, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(names, ","), privateMigration) {
		t.Fatalf("migrations = %v", names)
	}
	body, err := migrations.FS.ReadFile(privateMigration)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"ALTER TABLE app.env_sets ADD COLUMN entry_id uuid NULL",
		"REFERENCES app.catalog_entries (id) ON DELETE CASCADE",
		"CREATE UNIQUE INDEX env_sets_entry_idx",
		"DROP CONSTRAINT env_sets_name_chk",
		"(entry_id IS NOT NULL AND name ~ '^@[a-z0-9][a-z0-9-]{0,47}$')",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("0022 missing %s", want)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if trim := strings.TrimSpace(line); strings.HasPrefix(trim, "--") && strings.ContainsAny(trim, ";'") {
			t.Fatalf("comment contains a semicolon or an apostrophe: %s", trim)
		}
	}
	// The column, the foreign key, the index, and the name check dropped and added.
	if stmts := splitSQL(text); len(stmts) != 5 {
		t.Fatalf("0022 splits into %d statements, want 5", len(stmts))
	}
	down, err := migrations.FS.ReadFile("0022_env_private_sets.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downText := string(down)
	if !strings.Contains(downText, "DELETE FROM app.schema_migrations WHERE name = '0022_env_private_sets.sql'") ||
		!strings.Contains(downText, "CHECK (entry_id IS NULL)") ||
		!strings.Contains(downText, "DROP COLUMN IF EXISTS entry_id") ||
		!strings.Contains(downText, "\nBEGIN;") || !strings.HasSuffix(strings.TrimSpace(downText), "COMMIT;") {
		t.Fatal("down script")
	}
	// The refusal comes first, before anything is dropped.
	stmts := splitSQL(downText)
	if len(stmts) < 2 || !strings.Contains(stmts[1], "env_sets_no_private_chk") {
		t.Fatalf("down order %v", stmts)
	}
}

// imageWrite is a stored image entry write with a private set.
func imageWrite(t *testing.T, name string, secretEnv map[string]string, private *store.PrivateEnv, actor uuid.UUID, at time.Time) store.CatalogWrite {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"kind": "service", "image": "nginx", "secret_env": secretEnv})
	names := map[string][]string{}
	if private != nil {
		names[catalog.PrivateSet(name)] = private.Keys
	}
	img, err := catalog.BuildImage(name, raw, names, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store.CatalogWrite{
		Name: name, Kind: "image", ImageRaw: json.RawMessage(catalog.SpecJSON(img)),
		SpecHash: catalog.ImageHash(img, catalog.KeyDigests(catalog.ReferencedSets(img), names)), CommandDisplay: img.Image,
		Actor: actor, At: at, Private: private,
	}
}

func sealedKey(t *testing.T, box *seal.Box, project uuid.UUID, set, key, plain string, generated bool) store.EnvKeyCreate {
	t.Helper()
	nonce, ct, err := box.Seal(seal.EnvAAD(project.String(), set, key), []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	return store.EnvKeyCreate{Key: key, Secret: true, Generated: generated, Nonce: nonce, Ciphertext: ct, KeyVersion: seal.KeyVersion}
}

// TestParityPrivateSets is item 6 and item 3 in both stores.
func TestParityPrivateSets(t *testing.T) {
	box, err := seal.New(bytes.Repeat([]byte{6}, 32))
	if err != nil {
		t.Fatal(err)
	}
	runParity(t, func(p parity) {
		owner := p.user("ps")
		pr := p.project(p.org(owner.ID), owner.ID, "privs")
		refs := map[string]string{"TOKEN": "@web.TOKEN", "SIGN": "@web.SIGN"}
		private := &store.PrivateEnv{Keys: []string{"SIGN", "TOKEN"}, Add: []store.EnvKeyCreate{
			sealedKey(p.t, box, pr.ID, "@web", "SIGN", "generated-sign-value-0123", true),
			sealedKey(p.t, box, pr.ID, "@web", "TOKEN", "", false),
		}}
		entry, err := p.st.CreateCatalogEntry(p.ctx, pr.ID, imageWrite(p.t, "web", refs, private, owner.ID, p.at))
		if err != nil {
			p.t.Fatal(err)
		}
		set, err := p.st.GetEnvSet(p.ctx, pr.ID, "@web")
		if err != nil || set.Entry != "web" || len(set.Keys) != 2 {
			p.t.Fatalf("private set %+v %v", set, err)
		}
		flags := map[string][3]bool{}
		for _, k := range set.Keys {
			flags[k.Key] = [3]bool{k.IsSecret, k.RevealPending, k.NeedsValue}
		}
		if flags["SIGN"] != [3]bool{true, true, false} || flags["TOKEN"] != [3]bool{true, false, true} {
			p.t.Fatalf("flags %v", flags)
		}
		names, err := p.st.ListEnvKeyNames(p.ctx, pr.ID)
		if err != nil || strings.Join(names["@web"], ",") != "SIGN,TOKEN" {
			p.t.Fatalf("names %v %v", names, err)
		}
		all, err := p.st.ListEnvSets(p.ctx, pr.ID)
		if err != nil || len(all) != 1 || all[0].Entry != "web" {
			p.t.Fatalf("list %v %v", all, err)
		}
		// A shared set is not named @x.
		if _, err := p.st.CreateEnvSetNames(p.ctx, pr.ID, "@web", nil, nil, owner.ID, p.at); !errors.Is(err, store.ErrInvalid) {
			p.t.Fatalf("shared @ name %v", err)
		}

		// A value request on the private set; filling TOKEN, then rotating SIGN.
		request := func(key string) []byte {
			p.t.Helper()
			_, hash, _ := secret.NewToken()
			if err := p.st.CreateValueRequest(p.ctx, hash, store.ValueRequest{RequestMeta: store.RequestMeta{UserID: owner.ID, ProjectID: pr.ID, CreatedAt: p.at, ExpiresAt: p.at.Add(store.RequestTTL)}, Set: "@web", Key: key}); err != nil {
				p.t.Fatalf("request %s: %v", key, err)
			}
			return hash
		}
		fill := func(hash []byte, key, plain string) bool {
			p.t.Helper()
			k := sealedKey(p.t, box, pr.ID, "@web", key, plain, false)
			got, err := p.st.FillValueRequest(p.ctx, hash, owner.ID, p.at, store.EnvKeyPut{Key: key, Nonce: k.Nonce, Ciphertext: k.Ciphertext, KeyVersion: k.KeyVersion})
			if err != nil {
				p.t.Fatalf("fill %s: %v", key, err)
			}
			return got.Replaced
		}
		// A placeholder fill is not a replace; a rotation is (review E).
		if fill(request("TOKEN"), "TOKEN", "token-value-0123") {
			p.t.Fatal("placeholder fill reported replaced")
		}
		rotate := request("SIGN")
		if !fill(rotate, "SIGN", "rotated-value-0123") {
			p.t.Fatal("rotation not reported replaced")
		}
		mats, err := p.st.ListEnvMaterial(p.ctx, pr.ID, "@web")
		if err != nil {
			p.t.Fatal(err)
		}
		for _, m := range mats {
			if m.NeedsValue || m.Generated {
				p.t.Fatalf("after fill %s needs %v generated %v", m.Key, m.NeedsValue, m.Generated)
			}
		}
		// A rotation request for TOKEN stays open while the next replace
		// drops the key; its set row remains, so the request is not cascaded.
		request("TOKEN")

		// A replace that drops TOKEN removes it and its open request.
		keep := &store.PrivateEnv{Keys: []string{"SIGN"}}
		if _, _, err := p.st.ReplaceCatalogEntry(p.ctx, pr.ID, "web", imageWrite(p.t, "web", map[string]string{"SIGN": "@web.SIGN"}, keep, owner.ID, p.at)); err != nil {
			p.t.Fatal(err)
		}
		names, _ = p.st.ListEnvKeyNames(p.ctx, pr.ID)
		if strings.Join(names["@web"], ",") != "SIGN" {
			p.t.Fatalf("after replace %v", names["@web"])
		}
		// A replace with no private keys deletes the set.
		if _, _, err := p.st.ReplaceCatalogEntry(p.ctx, pr.ID, "web", imageWrite(p.t, "web", map[string]string{}, &store.PrivateEnv{}, owner.ID, p.at)); err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.GetEnvSet(p.ctx, pr.ID, "@web"); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("empty private set kept %v", err)
		}
		// Recreated, then deleted with the entry.
		again := &store.PrivateEnv{Keys: []string{"TOKEN"}, Add: []store.EnvKeyCreate{sealedKey(p.t, box, pr.ID, "@web", "TOKEN", "", false)}}
		if _, _, err := p.st.ReplaceCatalogEntry(p.ctx, pr.ID, "web", imageWrite(p.t, "web", map[string]string{"TOKEN": "@web.TOKEN"}, again, owner.ID, p.at)); err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.DeleteCatalogEntry(p.ctx, pr.ID, entry.Name); err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.GetEnvSet(p.ctx, pr.ID, "@web"); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("private set left after entry delete %v", err)
		}
	})
}

// TestParityEnvSetChange is items 1 and 2 in both stores.
func TestParityEnvSetChange(t *testing.T) {
	box, err := seal.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	runParity(t, func(p parity) {
		owner, other := p.user("ec"), p.user("ex")
		pr := p.project(p.org(owner.ID), owner.ID, "chg")
		if _, err := p.st.PutEnvSet(p.ctx, pr.ID, "app", []store.EnvSecret{{Key: "A", Plain: "a"}, {Key: "B", Plain: "b"}, {Key: "C", Plain: "c"}}, owner.ID, p.at, false); err != nil {
			p.t.Fatal(err)
		}
		sealer := func(key, plain string) (store.EnvSecret, error) {
			nonce, ct, err := box.Seal(seal.EnvAAD(pr.ID.String(), "app", key), []byte(plain))
			return store.EnvSecret{Key: key, Nonce: nonce, Ciphertext: ct, KeyVersion: seal.KeyVersion}, err
		}
		plan := func(name string) []byte {
			p.t.Helper()
			_, hash, _ := secret.NewToken()
			if err := p.st.PutEnvSetConfirmation(p.ctx, hash, store.EnvSetConfirmation{
				UserID: owner.ID, ProjectID: pr.ID, SetName: name, Body: json.RawMessage(`{"action":"update"}`),
				RequestHash: bytes.Repeat([]byte{1}, 32), IdempotencyKey: "k", CreatedAt: p.at, ExpiresAt: p.at.Add(store.ConfirmationTTL),
			}); err != nil {
				p.t.Fatal(err)
			}
			return hash
		}
		ch := store.EnvSetChange{
			Set:        map[string]string{"A": "a2"},
			Add:        []store.EnvKeyCreate{sealedKey(p.t, box, pr.ID, "app", "G", "gen-value-0123456789", true), {Key: "P", Value: strPtr("plain")}},
			Remove:     []string{"B"},
			MakeSecret: []string{"C"},
		}
		// A plan bound to an older state is refused under the lock (review C).
		state := func() string {
			p.t.Helper()
			mats, err := p.st.ListEnvMaterial(p.ctx, pr.ID, "app")
			if err != nil {
				p.t.Fatal(err)
			}
			return store.EnvStateHash(mats)
		}
		before := state()
		if _, _, err := p.st.PutEnvKey(p.ctx, pr.ID, "app", store.EnvKeyPut{Key: "B", Plain: "b2"}, owner.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		if state() == before {
			p.t.Fatal("a key write did not change the state hash")
		}
		stale := ch
		stale.StateHash = before
		if _, err := p.st.ApplyEnvSetChange(p.ctx, pr.ID, "app", stale, nil, sealer, owner.ID, p.at); !errors.Is(err, store.ErrEnvSetChanged) {
			p.t.Fatalf("stale state %v", err)
		}
		if _, err := p.st.ApplyEnvSetChange(p.ctx, pr.ID, "app", store.EnvSetChange{Delete: true, StateHash: before}, nil, sealer, owner.ID, p.at); !errors.Is(err, store.ErrEnvSetChanged) {
			p.t.Fatalf("stale delete %v", err)
		}
		ch.StateHash = state()

		// Another user's plan is not found and writes nothing.
		h := plan("app")
		if _, err := p.st.ApplyEnvSetChange(p.ctx, pr.ID, "app", ch, h, sealer, other.ID, p.at); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("foreign plan %v", err)
		}
		// A key that no longer fits is a conflict and leaves the plan unused.
		bad := ch
		bad.Remove = []string{"NOPE"}
		if _, err := p.st.ApplyEnvSetChange(p.ctx, pr.ID, "app", bad, h, sealer, owner.ID, p.at); !errors.Is(err, store.ErrConflict) {
			p.t.Fatalf("missing key %v", err)
		}
		set, err := p.st.ApplyEnvSetChange(p.ctx, pr.ID, "app", ch, h, sealer, owner.ID, p.at)
		if err != nil {
			p.t.Fatal(err)
		}
		got := map[string]store.EnvKey{}
		for _, k := range set.Keys {
			got[k.Key] = k
		}
		if got["A"].Value == nil || *got["A"].Value != "a2" || got["B"].Key != "" || !got["C"].IsSecret || !got["G"].RevealPending || got["P"].Value == nil || *got["P"].Value != "plain" {
			p.t.Fatalf("after change %+v", got)
		}
		if c, _ := p.st.GetEnvSetConfirmation(p.ctx, h); c.UsedAt == nil {
			p.t.Fatal("plan not marked used")
		}
		if _, err := p.st.ApplyEnvSetChange(p.ctx, pr.ID, "app", ch, h, sealer, owner.ID, p.at); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("second use %v", err)
		}
		// A secret key cannot be set as plain.
		if _, err := p.st.ApplyEnvSetChange(p.ctx, pr.ID, "app", store.EnvSetChange{Set: map[string]string{"C": "x"}}, nil, sealer, owner.ID, p.at); !errors.Is(err, store.ErrConflict) {
			p.t.Fatalf("set on secret %v", err)
		}
		// Delete is refused while an entry names the set, then applies.
		if _, err := p.st.CreateCatalogEntry(p.ctx, pr.ID, imageWrite(p.t, "web", map[string]string{}, nil, owner.ID, p.at)); err != nil {
			p.t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"kind": "service", "image": "nginx", "env_sets": []string{"app"}})
		img, err := catalog.BuildImage("user", raw, map[string][]string{"app": {"A"}}, nil)
		if err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.CreateCatalogEntry(p.ctx, pr.ID, store.CatalogWrite{Name: "user", Kind: "image", ImageRaw: json.RawMessage(catalog.SpecJSON(img)), SpecHash: strings.Repeat("a", 64), CommandDisplay: "nginx", Actor: owner.ID, At: p.at}); err != nil {
			p.t.Fatal(err)
		}
		d := plan("app")
		if _, err := p.st.ApplyEnvSetChange(p.ctx, pr.ID, "app", store.EnvSetChange{Delete: true}, d, sealer, owner.ID, p.at); !errors.Is(err, store.ErrConflict) {
			p.t.Fatalf("delete used set %v", err)
		}
		if _, err := p.st.DeleteCatalogEntry(p.ctx, pr.ID, "user"); err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.ApplyEnvSetChange(p.ctx, pr.ID, "app", store.EnvSetChange{Delete: true}, d, sealer, owner.ID, p.at); err != nil {
			p.t.Fatalf("delete %v", err)
		}
		if _, err := p.st.GetEnvSet(p.ctx, pr.ID, "app"); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("set left %v", err)
		}
	})
}

func strPtr(s string) *string { return &s }

// TestParityOpenRequests is item 8 in both stores.
func TestParityOpenRequests(t *testing.T) {
	runParity(t, func(p parity) {
		owner, other := p.user("or"), p.user("oo")
		pr := p.project(p.org(owner.ID), owner.ID, "openr")
		if _, err := p.st.PutEnvSet(p.ctx, pr.ID, "svc", nil, owner.ID, p.at, false); err != nil {
			p.t.Fatal(err)
		}
		mk := func(user uuid.UUID, key string, created time.Time) []byte {
			p.t.Helper()
			_, hash, _ := secret.NewToken()
			if err := p.st.CreateValueRequest(p.ctx, hash, store.ValueRequest{RequestMeta: store.RequestMeta{UserID: user, ProjectID: pr.ID, CreatedAt: created, ExpiresAt: created.Add(store.RequestTTL)}, Set: "svc", Key: key}); err != nil {
				p.t.Fatal(err)
			}
			return hash
		}
		old := mk(owner.ID, "OLD", p.at.Add(-time.Minute))
		newer := mk(owner.ID, "NEW", p.at)
		mk(other.ID, "THEIRS", p.at)
		mk(owner.ID, "GONE", p.at.Add(-time.Hour)) // expired: not listed
		_, fileHash, _ := secret.NewToken()
		if err := p.st.CreateFileRequest(p.ctx, fileHash, store.FileRequest{RequestMeta: store.RequestMeta{UserID: owner.ID, ProjectID: pr.ID, CreatedAt: p.at.Add(-2 * time.Minute), ExpiresAt: p.at.Add(time.Minute)}, Name: "conf.txt", Secret: true}); err != nil {
			p.t.Fatal(err)
		}
		rows, err := p.st.ListOpenRequests(p.ctx, owner.ID, p.at, 60)
		if err != nil {
			p.t.Fatal(err)
		}
		if len(rows) != 3 || !bytes.Equal(rows[0].Hash, newer) || !bytes.Equal(rows[1].Hash, old) || rows[2].Type != store.RequestTypeFile ||
			rows[0].Subject != "svc.NEW" || rows[2].Subject != "conf.txt" || rows[0].ProjectName != pr.Name || rows[0].Type != store.RequestTypeValue {
			p.t.Fatalf("rows %+v", rows)
		}
		if rows, _ := p.st.ListOpenRequests(p.ctx, owner.ID, p.at, 1); len(rows) != 1 {
			p.t.Fatalf("limit %d", len(rows))
		}
	})
}

// TestEnvPrivateSetsMigration runs 0022 up, refuses the down while a private
// set exists, then down and up again on a test database.
func TestEnvPrivateSetsMigration(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() { restorePrivateSets(url) })
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
	assertPrivateColumn(t, d, ctx, true)
	assertTablePrivs(t, d, ctx, "env_sets", true, true, false, true, []string{"updated_at", "updated_by"})

	at := time.Now().UTC().Truncate(time.Second)
	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	owner := catalogUser(t, d, ctx, "ps-mig-"+tag, at)
	home := catalogProject(t, d, ctx, owner.ID, "PSMig", "psmig"+tag, at)
	box, err := seal.New(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	private := &store.PrivateEnv{Keys: []string{"TOKEN"}, Add: []store.EnvKeyCreate{sealedKey(t, box, home.ID, "@mig", "TOKEN", "", false)}}
	if _, err := d.CreateCatalogEntry(ctx, home.ID, imageWrite(t, "mig", map[string]string{"TOKEN": "@mig.TOKEN"}, private, owner.ID, at)); err != nil {
		t.Fatal(err)
	}
	// The name check: a private name needs entry_id, a shared one must not have it.
	if _, err := d.pool.Exec(ctx, `INSERT INTO app.env_sets (project_id, name, updated_at, updated_by) VALUES ($1::uuid, '@loose', now(), $2::uuid)`, home.ID.String(), owner.ID.String()); !checkViolation(err) {
		t.Fatalf("private name without entry %v", err)
	}

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal("connect failed")
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `SET ROLE deploybot_owner`); err != nil {
		t.Fatal(err)
	}
	down, err := migrations.FS.ReadFile("0022_env_private_sets.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	runDown := func() error {
		for _, stmt := range splitSQL(string(down)) {
			if _, err := conn.Exec(ctx, stmt); err != nil {
				_, _ = conn.Exec(ctx, "ROLLBACK")
				return err
			}
		}
		return nil
	}
	if err := runDown(); err == nil {
		t.Fatal("down ran while a private set exists")
	}
	assertPrivateColumn(t, d, ctx, true)
	if _, err := d.DeleteCatalogEntry(ctx, home.ID, "mig"); err != nil {
		t.Fatal(err)
	}
	if err := runDown(); err != nil {
		t.Fatalf("down: %v", err)
	}
	assertPrivateColumn(t, d, ctx, false)
	if _, err := conn.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '`+privateMigration+`'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("restore migrate: %v", err)
	}
	assertPrivateColumn(t, d, ctx, true)
	assertTablePrivs(t, d, ctx, "env_sets", true, true, false, true, []string{"updated_at", "updated_by"})
}

func assertPrivateColumn(t *testing.T, d *DB, ctx context.Context, applied bool) {
	t.Helper()
	var n int
	if err := d.pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = 'app' AND table_name = 'env_sets' AND column_name = 'entry_id'
`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if applied != (n == 1) {
		t.Fatalf("0022 applied %v, entry_id columns %d", applied, n)
	}
}

// restorePrivateSets re-applies 0022 when a failed run left it rolled back.
func restorePrivateSets(url string) {
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
	var n int
	if err := d.pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = 'app' AND table_name = 'env_sets' AND column_name = 'entry_id'
`).Scan(&n); err != nil || n == 1 {
		return
	}
	if _, err := d.pool.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '`+privateMigration+`'`); err != nil {
		return
	}
	_ = Migrate(ctx, url)
}

// TestEnvPrivateSetsAsAPIRole runs the v1.33 writes through a deploybot_api
// login, the way the server connects: the private set insert with entry_id,
// a key change plan, the cascade on entry delete, and the open-requests read.
func TestEnvPrivateSetsAsAPIRole(t *testing.T) {
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
	at := time.Now().UTC().Truncate(time.Second)
	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	owner := catalogUser(t, d, ctx, "ps-api-"+tag, at)
	home := catalogProject(t, d, ctx, owner.ID, "PSApi", "psapi"+tag, at)
	box, err := seal.New(bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	private := &store.PrivateEnv{Keys: []string{"TOKEN"}, Add: []store.EnvKeyCreate{sealedKey(t, box, home.ID, "@web", "TOKEN", "", false)}}
	if _, err := api.CreateCatalogEntry(ctx, home.ID, imageWrite(t, "web", map[string]string{"TOKEN": "@web.TOKEN"}, private, owner.ID, at)); err != nil {
		t.Fatalf("private set as deploybot_api: %v", err)
	}
	if _, err := api.PutEnvSet(ctx, home.ID, "app", []store.EnvSecret{{Key: "A", Plain: "a"}}, owner.ID, at, false); err != nil {
		t.Fatal(err)
	}
	sealer := func(key, plain string) (store.EnvSecret, error) {
		nonce, ct, err := box.Seal(seal.EnvAAD(home.ID.String(), "app", key), []byte(plain))
		return store.EnvSecret{Key: key, Nonce: nonce, Ciphertext: ct, KeyVersion: seal.KeyVersion}, err
	}
	if _, err := api.ApplyEnvSetChange(ctx, home.ID, "app", store.EnvSetChange{Set: map[string]string{"A": "b"}, MakeSecret: []string{"A"}}, nil, sealer, owner.ID, at); err != nil {
		t.Fatalf("change as deploybot_api: %v", err)
	}
	if _, err := api.ApplyEnvSetChange(ctx, home.ID, "app", store.EnvSetChange{Delete: true}, nil, sealer, owner.ID, at); err != nil {
		t.Fatalf("delete as deploybot_api: %v", err)
	}
	if _, err := api.ListOpenRequests(ctx, owner.ID, at, 60); err != nil {
		t.Fatalf("open requests as deploybot_api: %v", err)
	}
	if _, err := api.DeleteCatalogEntry(ctx, home.ID, "web"); err != nil {
		t.Fatalf("entry delete cascade as deploybot_api: %v", err)
	}
	if _, err := api.GetEnvSet(ctx, home.ID, "@web"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("private set left %v", err)
	}
}
