package db

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
	"github.com/more-than-code/bot-control-plane/api/migrations"
)

func TestEnvGenerateMigrationText(t *testing.T) {
	body, err := migrations.FS.ReadFile("0017_env_generate.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"ALTER TABLE app.env_values ADD COLUMN reveal_pending boolean NOT NULL DEFAULT false",
		"ALTER TABLE app.env_values ADD COLUMN needs_value boolean NOT NULL DEFAULT false",
		"ALTER TABLE app.env_values ADD COLUMN generated boolean NOT NULL DEFAULT false",
		"CONSTRAINT env_values_reveal_chk CHECK (NOT reveal_pending OR is_secret)",
		"CONSTRAINT env_values_generated_chk CHECK (NOT generated OR is_secret)",
		"GRANT UPDATE (reveal_pending, needs_value, generated) ON TABLE app.env_values TO deploybot_api",
		"CREATE TABLE app.env_set_confirmations",
		"token_hash bytea PRIMARY KEY",
		"CONSTRAINT env_set_confirmations_token_len CHECK (octet_length(token_hash) = 32)",
		"CONSTRAINT env_set_confirmations_request_len CHECK (octet_length(request_hash) = 32)",
		"CONSTRAINT env_set_confirmations_name_chk CHECK (set_name ~ '^[a-z0-9][a-z0-9-]{0,47}$')",
		"CONSTRAINT env_set_confirmations_key_chk CHECK (idempotency_key ~ '^[A-Za-z0-9._:-]{1,128}$')",
		"FOREIGN KEY (user_id) REFERENCES app.users (id) ON DELETE CASCADE",
		"FOREIGN KEY (project_id) REFERENCES app.projects (id) ON DELETE CASCADE",
		"REVOKE ALL ON TABLE app.env_set_confirmations FROM deploybot_api",
		"GRANT SELECT, INSERT, DELETE ON TABLE app.env_set_confirmations TO deploybot_api",
		"GRANT UPDATE (used_at) ON TABLE app.env_set_confirmations TO deploybot_api",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("0017 missing %s", want)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "--") && strings.Contains(trim, ";") {
			t.Fatalf("comment with a semicolon would split: %s", trim)
		}
	}
	if n := len(splitSQL(text)); n != 10 {
		t.Fatalf("statements %d", n)
	}
	down, err := migrations.FS.ReadFile("0017_env_generate.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downText := string(down)
	if !strings.Contains(downText, "DELETE FROM app.schema_migrations WHERE name = '0017_env_generate.sql'") {
		t.Fatal("down script does not name its schema_migrations row")
	}
	if !strings.Contains(downText, "\nBEGIN;") || !strings.HasSuffix(strings.TrimSpace(downText), "COMMIT;") {
		t.Fatal("down script must run in one transaction")
	}
	for _, want := range []string{
		"DROP TABLE IF EXISTS app.env_set_confirmations",
		"DROP CONSTRAINT IF EXISTS env_values_reveal_chk",
		"DROP CONSTRAINT IF EXISTS env_values_generated_chk",
		"DROP COLUMN IF EXISTS generated",
		"DROP COLUMN IF EXISTS needs_value",
		"DROP COLUMN IF EXISTS reveal_pending",
	} {
		if !strings.Contains(downText, want) {
			t.Fatalf("down missing %s", want)
		}
	}
}

// TestEnvGenerateMigration runs 0017 up, down, and up again on a test database (isTestDatabase).
func TestEnvGenerateMigration(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() { restoreEnvGenerate(url) })
	d, err := Open(ctx, url)
	if err != nil {
		t.Fatal("connect failed")
	}
	defer d.Close()
	var dbName string
	if err := d.pool.QueryRow(ctx, `SELECT current_database()`).Scan(&dbName); err != nil {
		t.Fatal("database name lookup failed")
	}
	if !isTestDatabase(dbName) {
		t.Fatalf("refusing database %s: integration tests run only on deploybot_test or deploybot_test_*", dbName)
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	assertEnvGenerateSchema(t, d, ctx, true)
	assertTablePrivs(t, d, ctx, "env_set_confirmations", true, true, false, true, []string{"used_at"})

	// The CHECK refuses a pending plain value.
	at := time.Now().UTC().Truncate(time.Second)
	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	owner := catalogUser(t, d, ctx, "envgen-mig-"+tag, at)
	home := catalogProject(t, d, ctx, owner.ID, "EnvGenMig", "envgenmig"+tag, at)
	if _, err := d.PutEnvSet(ctx, home.ID, "svc", []store.EnvSecret{{Key: "A", Plain: "a"}}, owner.ID, at, false); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE app.env_values SET reveal_pending = true WHERE project_id = $1::uuid AND set_name = 'svc' AND key = 'A'`, home.ID.String()); !checkViolation(err) {
		t.Fatalf("pending plain value %v", err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE app.env_values SET generated = true WHERE project_id = $1::uuid AND set_name = 'svc' AND key = 'A'`, home.ID.String()); !checkViolation(err) {
		t.Fatalf("generated plain value %v", err)
	}

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal("connect failed")
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `SET ROLE deploybot_owner`); err != nil {
		t.Fatal(err)
	}
	down, err := migrations.FS.ReadFile("0017_env_generate.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitSQL(string(down)) {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("down: %v", err)
		}
	}
	assertEnvGenerateSchema(t, d, ctx, false)
	if _, err := conn.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '0017_env_generate.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("restore migrate: %v", err)
	}
	assertEnvGenerateSchema(t, d, ctx, true)
	// Existing rows come back with both flags false.
	set, err := d.GetEnvSet(ctx, home.ID, "svc")
	if err != nil || len(set.Keys) != 1 || set.Keys[0].RevealPending || set.Keys[0].NeedsValue || set.Keys[0].Generated {
		t.Fatalf("after up %#v %v", set, err)
	}
}

func assertEnvGenerateSchema(t *testing.T, d *DB, ctx context.Context, applied bool) {
	t.Helper()
	var cols int
	if err := d.pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = 'app' AND table_name = 'env_values' AND column_name IN ('reveal_pending', 'needs_value', 'generated')
`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	var table *string
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('app.env_set_confirmations')::text`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	var checks int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conname IN ('env_values_reveal_chk', 'env_values_generated_chk')`).Scan(&checks); err != nil {
		t.Fatal(err)
	}
	if applied && (cols != 3 || table == nil || checks != 2) {
		t.Fatalf("0017 not applied: cols %d table %v checks %d", cols, table, checks)
	}
	if !applied && (cols != 0 || table != nil || checks != 0) {
		t.Fatalf("0017 not rolled back: cols %d table %v checks %d", cols, table, checks)
	}
	if applied {
		for _, col := range []string{"reveal_pending", "needs_value", "generated"} {
			var ok bool
			if err := d.pool.QueryRow(ctx, `SELECT has_column_privilege('deploybot_api', 'app.env_values', $1, 'UPDATE')`, col).Scan(&ok); err != nil || !ok {
				t.Fatalf("deploybot_api UPDATE on env_values.%s: %v %v", col, ok, err)
			}
		}
	}
}

// restoreEnvGenerate re-applies 0017 when a failed run left it rolled back.
func restoreEnvGenerate(url string) {
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
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('app.env_set_confirmations')::text`).Scan(&table); err != nil || table != nil {
		return
	}
	if _, err := d.pool.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '0017_env_generate.sql'`); err != nil {
		return
	}
	_ = Migrate(ctx, url)
}

// assertEnvGenerate covers the v1.27 flags in the Postgres store: a
// generated PutEnvKey sets reveal_pending, RevealEnvKey is single use (also
// under concurrency), and every other value write clears both flags.
func assertEnvGenerate(t *testing.T, d *DB, ctx context.Context, at time.Time) {
	t.Helper()
	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	owner := catalogUser(t, d, ctx, "env-generate-"+tag, at)
	home := catalogProject(t, d, ctx, owner.ID, "EnvGenerate", "envgen"+tag, at)
	box, err := seal.New(bytes.Repeat([]byte{6}, 32))
	if err != nil {
		t.Fatal(err)
	}
	pid := home.ID.String()
	sealed := func(key, plain string) store.EnvSecret {
		t.Helper()
		nonce, ciphertext, err := box.Seal(seal.EnvAAD(pid, "svc", key), []byte(plain))
		if err != nil {
			t.Fatal(err)
		}
		return store.EnvSecret{Key: key, Plain: plain, Nonce: nonce, Ciphertext: ciphertext, KeyVersion: seal.KeyVersion}
	}
	// putGenerated drops the key first: generate makes new keys only (rev 3).
	putGenerated := func(key, plain string) {
		t.Helper()
		if err := d.DeleteEnvKey(ctx, home.ID, "svc", key, owner.ID, at); err != nil && !errors.Is(err, store.ErrNotFound) {
			t.Fatal(err)
		}
		s := sealed(key, plain)
		yes := true
		got, _, err := d.PutEnvKey(ctx, home.ID, "svc", store.EnvKeyPut{Key: key, Plain: plain, Secret: &yes, Nonce: s.Nonce, Ciphertext: s.Ciphertext, KeyVersion: s.KeyVersion, Generated: true}, owner.ID, at)
		if err != nil || !got.RevealPending || !got.IsSecret || !got.Generated {
			t.Fatalf("generated put %#v %v", got, err)
		}
	}
	// flags returns reveal_pending and needs_value, and checks that the
	// view and the material agree on generated.
	generatedOf := func(key string) bool {
		t.Helper()
		set, err := d.GetEnvSet(ctx, home.ID, "svc")
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range set.Keys {
			if k.Key == key {
				if envRows(t, d, ctx, home.ID, "svc")[key].Generated != k.Generated {
					t.Fatalf("%s generated differs between view and material", key)
				}
				return k.Generated
			}
		}
		t.Fatalf("no key %s", key)
		return false
	}
	flags := func(key string) (bool, bool) {
		t.Helper()
		set, err := d.GetEnvSet(ctx, home.ID, "svc")
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range set.Keys {
			if k.Key == key {
				return k.RevealPending, k.NeedsValue
			}
		}
		t.Fatalf("no key %s", key)
		return false, false
	}

	putGenerated("PW", "generated-one")
	if pending, needs := flags("PW"); !pending || needs {
		t.Fatalf("flags %v %v", pending, needs)
	}
	mat, err := d.RevealEnvKey(ctx, home.ID, "svc", "PW")
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := box.Open(seal.EnvAAD(pid, "svc", "PW"), mat.Nonce, mat.Ciphertext); err != nil || string(opened) != "generated-one" || mat.KeyVersion != seal.KeyVersion {
		t.Fatalf("reveal did not open %v", err)
	}
	if _, err := d.RevealEnvKey(ctx, home.ID, "svc", "PW"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second reveal %v", err)
	}
	// The reveal clears reveal_pending only: the value is still generated.
	if !mat.Generated || !generatedOf("PW") {
		t.Fatal("reveal cleared generated")
	}
	if _, err := d.RevealEnvKey(ctx, home.ID, "svc", "GONE"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing key %v", err)
	}
	if _, err := d.RevealEnvKey(ctx, home.ID, "nope", "PW"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing set %v", err)
	}

	// Concurrent reveals: exactly one success.
	putGenerated("PW", "generated-two")
	// This pins single use at the database: the UPDATE ... WHERE
	// reveal_pending RETURNING lets one statement win.
	var wg sync.WaitGroup
	var mu sync.Mutex
	start := make(chan struct{})
	wins, conflicts := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := d.RevealEnvKey(ctx, home.ID, "svc", "PW")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else if errors.Is(err, store.ErrConflict) {
				conflicts++
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins != 1 || conflicts != 7 {
		t.Fatalf("concurrent reveals wins %d conflicts %d", wins, conflicts)
	}

	// setNeeds simulates a phase B placeholder.
	setNeeds := func(key string) {
		t.Helper()
		if _, err := d.pool.Exec(ctx, `UPDATE app.env_values SET needs_value = true WHERE project_id = $1::uuid AND set_name = 'svc' AND key = $2`, pid, key); err != nil {
			t.Fatal(err)
		}
	}
	writers := []struct {
		name  string
		write func()
	}{
		{"key put", func() {
			s := sealed("PW", "typed")
			if _, _, err := d.PutEnvKey(ctx, home.ID, "svc", store.EnvKeyPut{Key: "PW", Plain: "typed", Nonce: s.Nonce, Ciphertext: s.Ciphertext, KeyVersion: s.KeyVersion}, owner.ID, at); err != nil {
				t.Fatal(err)
			}
		}},
		{"merge", func() {
			if _, _, err := d.MergeEnvSet(ctx, home.ID, "svc", []store.EnvSecret{sealed("PW", "merged")}, owner.ID, at, true); err != nil {
				t.Fatal(err)
			}
		}},
		{"replace", func() {
			if _, err := d.PutEnvSet(ctx, home.ID, "svc", []store.EnvSecret{sealed("PW", "replaced")}, owner.ID, at, true); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, wr := range writers {
		putGenerated("PW", "generated-"+wr.name)
		setNeeds("PW")
		wr.write()
		if pending, needs := flags("PW"); pending || needs || generatedOf("PW") {
			t.Fatalf("%s left pending=%v needs=%v generated", wr.name, pending, needs)
		}
	}
	// Generate over an existing secret key is ErrKeyExists and writes
	// nothing (rev 3). Phase B will let it fill a needs_value placeholder.
	putGenerated("PW", "generated-again")
	beforeRow := envRows(t, d, ctx, home.ID, "svc")["PW"]
	yes := true
	over := sealed("PW", "would-replace")
	if _, _, err := d.PutEnvKey(ctx, home.ID, "svc", store.EnvKeyPut{Key: "PW", Plain: "would-replace", Secret: &yes, Nonce: over.Nonce, Ciphertext: over.Ciphertext, KeyVersion: over.KeyVersion, Generated: true}, owner.ID, at.Add(time.Hour)); !errors.Is(err, store.ErrKeyExists) {
		t.Fatalf("generate over secret %v", err)
	}
	if after := envRows(t, d, ctx, home.ID, "svc")["PW"]; !bytes.Equal(after.Ciphertext, beforeRow.Ciphertext) || !after.Generated {
		t.Fatal("refused generate changed the secret key")
	}
	if pending, needs := flags("PW"); !pending || needs {
		t.Fatalf("refused generate changed flags %v %v", pending, needs)
	}
	if view, err := d.GetEnvSet(ctx, home.ID, "svc"); err != nil || view.UpdatedAt.Equal(at.Add(time.Hour)) {
		t.Fatalf("refused generate moved updated_at %v", err)
	}
	// Create writes both flags false.
	if _, err := d.CreateEnvSet(ctx, home.ID, "fresh", []store.EnvSecret{{Key: "A", Plain: "a"}}, owner.ID, at, false); err != nil {
		t.Fatal(err)
	}
	fresh, err := d.GetEnvSet(ctx, home.ID, "fresh")
	if err != nil || fresh.Keys[0].RevealPending || fresh.Keys[0].NeedsValue {
		t.Fatalf("create flags %#v %v", fresh, err)
	}
	// Make secret writes both flags false on a plain key.
	if _, err := d.pool.Exec(ctx, `UPDATE app.env_values SET needs_value = true WHERE project_id = $1::uuid AND set_name = 'fresh' AND key = 'A'`, pid); err != nil {
		t.Fatal(err)
	}
	sealer := func(key, plain string) (store.EnvSecret, error) {
		nonce, ciphertext, err := box.Seal(seal.EnvAAD(pid, "fresh", key), []byte(plain))
		return store.EnvSecret{Key: key, Nonce: nonce, Ciphertext: ciphertext, KeyVersion: seal.KeyVersion}, err
	}
	set, _, err := d.MakeEnvKeysSecret(ctx, home.ID, "fresh", []string{"A"}, sealer, owner.ID, at)
	if err != nil || !set.Keys[0].IsSecret || set.Keys[0].RevealPending || set.Keys[0].NeedsValue || set.Keys[0].Generated {
		t.Fatalf("make secret flags %#v %v", set, err)
	}

	// Generate over a plain key is ErrKeyExists and writes nothing.
	if _, err := d.PutEnvSet(ctx, home.ID, "plainset", []store.EnvSecret{{Key: "P", Plain: "visible"}}, owner.ID, at, false); err != nil {
		t.Fatal(err)
	}
	s := sealed("P", "would-be-generated")
	if _, _, err := d.PutEnvKey(ctx, home.ID, "plainset", store.EnvKeyPut{Key: "P", Plain: "would-be-generated", Secret: &yes, Nonce: s.Nonce, Ciphertext: s.Ciphertext, KeyVersion: s.KeyVersion, Generated: true}, owner.ID, at); !errors.Is(err, store.ErrKeyExists) {
		t.Fatalf("generate over plain %v", err)
	}
	if row := envRows(t, d, ctx, home.ID, "plainset")["P"]; row.IsSecret || row.Plain != "visible" || row.Generated {
		t.Fatalf("plain key changed %#v", row.IsSecret)
	}
	// A deleted project's key cannot be revealed.
	putGenerated("GONE_LATER", "generated-for-a-deleted-project")
	if _, err := d.pool.Exec(ctx, `UPDATE app.projects SET deleted_at = now() WHERE id = $1::uuid`, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RevealEnvKey(ctx, home.ID, "svc", "GONE_LATER"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted project reveal %v", err)
	}
}
