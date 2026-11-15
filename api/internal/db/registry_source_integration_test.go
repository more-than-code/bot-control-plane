package db

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
	"github.com/more-than-code/bot-control-plane/api/migrations"
)

func TestRegistrySourceMigrationText(t *testing.T) {
	body, err := migrations.FS.ReadFile("0019_registry_source.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"ADD COLUMN source text NOT NULL DEFAULT 'manual'",
		"CHECK (source IN ('manual', 'github'))",
		"ADD COLUMN connected_by uuid NULL",
		"REFERENCES app.users (id) ON DELETE SET NULL",
		"GRANT UPDATE (source, connected_by) ON TABLE app.registries TO deploybot_api",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("0019 missing %s", want)
		}
	}
	// splitSQL does not know comments: a semicolon in one splits the file,
	// and an apostrophe opens a quote that swallows the next statements.
	for _, line := range strings.Split(text, "\n") {
		if trim := strings.TrimSpace(line); strings.HasPrefix(trim, "--") && strings.ContainsAny(trim, ";'") {
			t.Fatalf("comment contains a semicolon or an apostrophe: %s", trim)
		}
	}
	// Two columns and one grant; the first statement carries the comments.
	if stmts := splitSQL(text); len(stmts) != 3 || !strings.HasPrefix(stmts[0], "--") || !strings.Contains(stmts[0], "ADD COLUMN source") {
		t.Fatalf("0019 splits into %d statements, want 3: %q", len(stmts), stmts)
	}
	down, err := migrations.FS.ReadFile("0019_registry_source.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downText := string(down)
	for _, want := range []string{
		"--migrate does not apply",
		"DELETE FROM app.schema_migrations WHERE name = '0019_registry_source.sql'",
		"DROP COLUMN IF EXISTS connected_by",
		"DROP COLUMN IF EXISTS source",
		"\nBEGIN;",
	} {
		if !strings.Contains(downText, want) {
			t.Fatalf("down script missing %s", want)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(downText), "COMMIT;") || strings.Contains(downText, "DELETE FROM app.registries") {
		t.Fatal("down script must keep rows and end with COMMIT")
	}
}

// TestRegistrySourceMigration runs 0019 up, down, and up again on a test
// database. Rows stay through the down script.
func TestRegistrySourceMigration(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() { restoreRegistrySource(url) })
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
	assertRegistrySource(t, d, ctx, true)
	at := time.Now().UTC().Truncate(time.Second)
	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	owner := catalogUser(t, d, ctx, "rs-mig-"+tag, at)
	home := catalogProject(t, d, ctx, owner.ID, "RSMig", "rsmig"+tag, at)
	box, err := seal.New(bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	nonce, ct, err := box.Seal(seal.RegistryAAD(home.ID.String(), "ghcr.io"), []byte("gho_token"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.PutRegistry(ctx, home.ID, "ghcr.io", store.RegistryWrite{
		Username: "octo", Nonce: nonce, Ciphertext: ct, KeyVersion: seal.KeyVersion,
		Source: store.RegistrySourceGitHub, ConnectedBy: &owner.ID,
	}, owner.ID, at); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE app.registries SET source = 'gitlab' WHERE project_id = $1::uuid`, home.ID.String()); !checkViolation(err) {
		t.Fatalf("source check %v", err)
	}

	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal("connect failed")
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `SET ROLE deploybot_owner`); err != nil {
		t.Fatal(err)
	}
	down, err := migrations.FS.ReadFile("0019_registry_source.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range splitSQL(string(down)) {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("down: %v", err)
		}
	}
	assertRegistrySource(t, d, ctx, false)
	var n int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM app.registries WHERE project_id = $1::uuid AND username = 'octo'`, home.ID.String()).Scan(&n); err != nil || n != 1 {
		t.Fatalf("row after down %d %v", n, err)
	}
	if _, err := conn.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '0019_registry_source.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("restore migrate: %v", err)
	}
	assertRegistrySource(t, d, ctx, true)
	// The kept row comes back as manual: the token acts as a manual login.
	items, err := d.ListRegistries(ctx, home.ID)
	if err != nil || len(items) != 1 || items[0].Source != "manual" || items[0].ConnectedBy != nil {
		t.Fatalf("after up %+v %v", items, err)
	}
}

func assertRegistrySource(t *testing.T, d *DB, ctx context.Context, applied bool) {
	t.Helper()
	var cols int
	if err := d.pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = 'app' AND table_name = 'registries' AND column_name IN ('source', 'connected_by')
`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	want := 0
	update := []string{"username", "nonce", "ciphertext", "key_version", "updated_at", "updated_by"}
	if applied {
		want = 2
		update = append(update, "source", "connected_by")
	}
	if cols != want {
		t.Fatalf("0019 applied %v, columns %d", applied, cols)
	}
	assertTablePrivs(t, d, ctx, "registries", true, true, false, true, update)
}

// restoreRegistrySource re-applies 0019 when a failed run left it rolled back.
func restoreRegistrySource(url string) {
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
	var cols int
	if err := d.pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = 'app' AND table_name = 'registries' AND column_name = 'source'
`).Scan(&cols); err != nil || cols != 0 {
		return
	}
	if _, err := d.pool.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '0019_registry_source.sql'`); err != nil {
		return
	}
	_ = Migrate(ctx, url)
}

// TestParityRegistrySource is api-v1 v1.29 item 5 in both stores.
func TestParityRegistrySource(t *testing.T) {
	runParity(t, func(p parity) {
		t := p.t
		owner, admin := p.user("rso"), p.user("rsa")
		proj := p.project(p.org(owner.ID), owner.ID, "rs")
		w := store.RegistryWrite{Username: "bot", Nonce: bytes.Repeat([]byte{1}, 12), Ciphertext: bytes.Repeat([]byte{2}, 16), KeyVersion: 1}
		reg, err := p.st.PutRegistry(p.ctx, proj.ID, "ghcr.io", w, owner.ID, p.at)
		if err != nil || reg.Source != "manual" || reg.ConnectedBy != nil {
			t.Fatalf("manual %+v %v", reg, err)
		}
		// ConnectedBy is dropped for a manual row.
		w.ConnectedBy = &admin.ID
		if reg, err = p.st.PutRegistry(p.ctx, proj.ID, "ghcr.io", w, owner.ID, p.at); err != nil || reg.ConnectedBy != nil {
			t.Fatalf("manual with connected_by %+v %v", reg, err)
		}
		w.Source, w.Username = store.RegistrySourceGitHub, "octo"
		reg, err = p.st.PutRegistry(p.ctx, proj.ID, "ghcr.io", w, admin.ID, p.at)
		if err != nil || reg.Source != "github" || reg.ConnectedBy == nil || reg.ConnectedBy.ID != admin.ID || reg.ConnectedBy.Name != "rsa" || reg.Username != "octo" {
			t.Fatalf("github %+v %v", reg, err)
		}
		items, err := p.st.ListRegistries(p.ctx, proj.ID)
		if err != nil || len(items) != 1 || items[0].Source != "github" || items[0].ConnectedBy == nil || items[0].ConnectedBy.Name != "rsa" {
			t.Fatalf("list %+v %v", items, err)
		}
		mat, err := p.st.GetRegistryMaterial(p.ctx, proj.ID, "ghcr.io")
		if err != nil || mat.Source != "github" || mat.Username != "octo" {
			t.Fatalf("material %+v %v", mat, err)
		}
		// A manual write over the github row makes it manual.
		w.Source, w.ConnectedBy, w.Username = "", nil, "bot"
		if reg, err = p.st.PutRegistry(p.ctx, proj.ID, "ghcr.io", w, owner.ID, p.at); err != nil || reg.Source != "manual" || reg.ConnectedBy != nil {
			t.Fatalf("manual over github %+v %v", reg, err)
		}
		if mat, _ = p.st.GetRegistryMaterial(p.ctx, proj.ID, "ghcr.io"); mat.Source != "manual" {
			t.Fatalf("material source %q", mat.Source)
		}
	})
}

// TestParityLatestCatalogDeploy is api-v1 v1.29 item 7: the newest
// catalog.deploy row for an entry on a host, in both stores.
func TestParityLatestCatalogDeploy(t *testing.T) {
	runParity(t, func(p parity) {
		t := p.t
		owner := p.user("lcd")
		org := p.org(owner.ID)
		proj := p.project(org, owner.ID, "lcd")
		host := p.enroll(org, &store.TokenGrant{ProjectID: proj.ID, Level: "deploy"}, owner.ID, "lcd")
		other := p.enroll(org, &store.TokenGrant{ProjectID: proj.ID, Level: "deploy"}, owner.ID, "lcd2")
		if _, err := p.st.LatestCatalogDeploy(p.ctx, proj.ID, "web", host.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("none %v", err)
		}
		put := func(at time.Time, action string, hostID uuid.UUID, entry, decision, reason string) {
			if err := p.st.InsertAudit(p.ctx, store.AuditRow{
				At: at, ActorUserID: &owner.ID, Via: "console", ProjectID: &proj.ID, HostID: &hostID,
				Action: action, Decision: decision, Detail: map[string]any{"entry": entry, "reason": reason},
			}); err != nil {
				t.Fatal(err)
			}
		}
		put(p.at, "catalog.deploy", host.ID, "web", "denied", "policy")
		put(p.at.Add(2*time.Second), "catalog.deploy", other.ID, "web", "allowed", "")
		put(p.at.Add(3*time.Second), "catalog.deploy", host.ID, "api", "allowed", "")
		put(p.at.Add(4*time.Second), "catalog.rollback", host.ID, "web", "allowed", "")
		got, err := p.st.LatestCatalogDeploy(p.ctx, proj.ID, "web", host.ID)
		if err != nil || got.Decision != "denied" || got.Action != "catalog.deploy" || !strings.Contains(string(got.Detail), `"reason": "policy"`) && !strings.Contains(string(got.Detail), `"reason":"policy"`) {
			t.Fatalf("latest %+v %v", got, err)
		}
		put(p.at.Add(5*time.Second), "catalog.deploy", host.ID, "web", "allowed", "")
		if got, err = p.st.LatestCatalogDeploy(p.ctx, proj.ID, "web", host.ID); err != nil || got.Decision != "allowed" {
			t.Fatalf("newer %+v %v", got, err)
		}
	})
}
