package db

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
	"github.com/more-than-code/bot-control-plane/api/migrations"
)

const refreshMigration = "0028_registry_github_refresh.sql"

// TestParityRegistryRefresh is spec-github-token-refresh acceptance 8 at
// the store: the refresh material round-trips through PutRegistry, a
// manual write drops it, RefreshRegistry and ClearRegistryRefresh are
// conditional on the old refresh nonce, and ListRegistryRefreshDue lists
// only github rows with a refresh token expiring before the cut-off.
func TestParityRegistryRefresh(t *testing.T) {
	runParity(t, func(p parity) {
		t := p.t
		owner := p.user("rr")
		org := p.org(owner.ID)
		proj := p.project(org, owner.ID, "rr")
		other := p.project(org, owner.ID, "rr2")
		access := p.at.Add(time.Hour)
		refreshAt := p.at.Add(180 * 24 * time.Hour)
		w := store.RegistryWrite{
			Username: "octo", Nonce: bytes.Repeat([]byte{1}, 12), Ciphertext: bytes.Repeat([]byte{2}, 16), KeyVersion: 1,
			Source: store.RegistrySourceGitHub, ConnectedBy: &owner.ID,
			RegistryRefreshMaterial: store.RegistryRefreshMaterial{
				RefreshNonce: bytes.Repeat([]byte{3}, 12), RefreshCiphertext: bytes.Repeat([]byte{4}, 16),
				AccessExpiresAt: &access, RefreshExpiresAt: &refreshAt,
			},
		}
		if _, err := p.st.PutRegistry(p.ctx, proj.ID, "ghcr.io", w, owner.ID, p.at); err != nil {
			t.Fatal(err)
		}
		mat, err := p.st.GetRegistryMaterial(p.ctx, proj.ID, "ghcr.io")
		if err != nil || !mat.HasRefresh() || !bytes.Equal(mat.RefreshNonce, w.RefreshNonce) || !bytes.Equal(mat.RefreshCiphertext, w.RefreshCiphertext) ||
			mat.AccessExpiresAt == nil || !mat.AccessExpiresAt.Equal(access) || mat.RefreshExpiresAt == nil || !mat.RefreshExpiresAt.Equal(refreshAt) {
			t.Fatalf("material %+v %v", mat.RegistryRefreshMaterial, err)
		}

		// Due: before access_expires_at only; other rows never.
		manual := store.RegistryWrite{Username: "bot", Nonce: bytes.Repeat([]byte{5}, 12), Ciphertext: bytes.Repeat([]byte{6}, 16), KeyVersion: 1,
			RegistryRefreshMaterial: w.RegistryRefreshMaterial}
		if _, err := p.st.PutRegistry(p.ctx, other.ID, "ghcr.io", manual, owner.ID, p.at); err != nil {
			t.Fatal(err)
		}
		if m, _ := p.st.GetRegistryMaterial(p.ctx, other.ID, "ghcr.io"); m.HasRefresh() || m.AccessExpiresAt != nil || m.RefreshExpiresAt != nil {
			t.Fatal("manual row kept refresh material")
		}
		due := func(before time.Time) []store.RegistryLogin {
			t.Helper()
			rows, err := p.st.ListRegistryRefreshDue(p.ctx, before)
			if err != nil {
				t.Fatal(err)
			}
			var out []store.RegistryLogin
			for _, r := range rows {
				if r.ProjectID == proj.ID || r.ProjectID == other.ID {
					out = append(out, r)
				}
			}
			return out
		}
		if got := due(access); len(got) != 0 {
			t.Fatalf("due at expiry %+v", got)
		}
		if got := due(access.Add(time.Second)); len(got) != 1 || got[0].ProjectID != proj.ID || got[0].OrgID != org || got[0].Host != "ghcr.io" {
			t.Fatalf("due %+v", got)
		}

		// Refresh: a wrong old nonce writes nothing.
		newAccess := p.at.Add(8 * time.Hour)
		in := store.RegistryRefresh{
			Nonce: bytes.Repeat([]byte{7}, 12), Ciphertext: bytes.Repeat([]byte{8}, 16), KeyVersion: 1,
			RegistryRefreshMaterial: store.RegistryRefreshMaterial{
				RefreshNonce: bytes.Repeat([]byte{9}, 12), RefreshCiphertext: bytes.Repeat([]byte{10}, 16),
				AccessExpiresAt: &newAccess, RefreshExpiresAt: &refreshAt,
			},
		}
		if ok, err := p.st.RefreshRegistry(p.ctx, proj.ID, "ghcr.io", bytes.Repeat([]byte{99}, 12), in); ok || err != nil {
			t.Fatalf("wrong nonce %v %v", ok, err)
		}
		if ok, err := p.st.RefreshRegistry(p.ctx, other.ID, "ghcr.io", w.RefreshNonce, in); ok || err != nil {
			t.Fatalf("manual row %v %v", ok, err)
		}
		if ok, err := p.st.RefreshRegistry(p.ctx, proj.ID, "ghcr.io", w.RefreshNonce, in); !ok || err != nil {
			t.Fatalf("refresh %v %v", ok, err)
		}
		// The race loser's write (same old nonce) is refused.
		if ok, err := p.st.RefreshRegistry(p.ctx, proj.ID, "ghcr.io", w.RefreshNonce, in); ok || err != nil {
			t.Fatalf("second refresh with the old nonce %v %v", ok, err)
		}
		mat, _ = p.st.GetRegistryMaterial(p.ctx, proj.ID, "ghcr.io")
		if !bytes.Equal(mat.Nonce, in.Nonce) || !bytes.Equal(mat.Ciphertext, in.Ciphertext) || !bytes.Equal(mat.RefreshNonce, in.RefreshNonce) ||
			!mat.AccessExpiresAt.Equal(newAccess) || mat.Username != "octo" || mat.Source != store.RegistrySourceGitHub {
			t.Fatalf("after refresh %+v", mat.RegistryRefreshMaterial)
		}
		items, err := p.st.ListRegistries(p.ctx, proj.ID)
		if err != nil || len(items) != 1 || !items[0].UpdatedAt.Equal(p.at) || items[0].UpdatedBy != owner.ID {
			t.Fatalf("refresh changed updated_at %+v %v", items, err)
		}
		if got := due(access.Add(time.Second)); len(got) != 0 {
			t.Fatalf("still due %+v", got)
		}

		// Clear: conditional, keeps the access token and its expiry.
		if ok, err := p.st.ClearRegistryRefresh(p.ctx, proj.ID, "ghcr.io", w.RefreshNonce); ok || err != nil {
			t.Fatalf("clear old nonce %v %v", ok, err)
		}
		if ok, err := p.st.ClearRegistryRefresh(p.ctx, proj.ID, "ghcr.io", in.RefreshNonce); !ok || err != nil {
			t.Fatalf("clear %v %v", ok, err)
		}
		if ok, _ := p.st.ClearRegistryRefresh(p.ctx, proj.ID, "ghcr.io", in.RefreshNonce); ok {
			t.Fatal("cleared twice")
		}
		mat, _ = p.st.GetRegistryMaterial(p.ctx, proj.ID, "ghcr.io")
		if mat.HasRefresh() || mat.RefreshExpiresAt != nil || mat.AccessExpiresAt == nil || !mat.AccessExpiresAt.Equal(newAccess) || !bytes.Equal(mat.Nonce, in.Nonce) {
			t.Fatalf("after clear %+v", mat.RegistryRefreshMaterial)
		}
		if got := due(newAccess.Add(time.Hour)); len(got) != 0 {
			t.Fatalf("cleared row due %+v", got)
		}
		if ok, _ := p.st.RefreshRegistry(p.ctx, proj.ID, "ghcr.io", nil, in); ok {
			t.Fatal("refresh without a nonce")
		}
		// A missing row: false, no error.
		if ok, err := p.st.RefreshRegistry(p.ctx, proj.ID, "docker.io", in.RefreshNonce, in); ok || err != nil {
			t.Fatalf("missing row %v %v", ok, err)
		}
	})
}

// TestRegistryRefreshMigrationText: 0028 adds the four columns and the
// grant; its down script drops them and never refuses.
func TestRegistryRefreshMigrationText(t *testing.T) {
	if !numberedSQL(refreshMigration) || numberedSQL("0028_registry_github_refresh.down.sql") {
		t.Fatal("0028 numbering")
	}
	body, err := migrations.FS.ReadFile(refreshMigration)
	if err != nil {
		t.Fatal(err)
	}
	stmts := sqlBodies(string(body))
	if len(stmts) != 2 || !strings.HasPrefix(stmts[0], "ALTER TABLE app.registries") ||
		stmts[1] != "GRANT UPDATE (refresh_nonce, refresh_ciphertext, access_expires_at, refresh_expires_at) ON TABLE app.registries TO deploybot_api" {
		t.Fatalf("0028 statements %q", stmts)
	}
	for _, col := range []string{"refresh_nonce bytea NULL", "refresh_ciphertext bytea NULL", "access_expires_at timestamptz NULL", "refresh_expires_at timestamptz NULL"} {
		if !strings.Contains(stmts[0], "ADD COLUMN "+col) {
			t.Fatalf("0028 missing %s", col)
		}
	}
	down, err := migrations.FS.ReadFile("0028_registry_github_refresh.down.sql")
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
	if len(ds) != 7 || ds[0] != "BEGIN" || ds[6] != "COMMIT" || strings.Contains(string(down), "RAISE") || strings.Contains(string(down), "DELETE FROM app.registries") ||
		!strings.Contains(string(down), "DELETE FROM app.schema_migrations WHERE name = '"+refreshMigration+"'") {
		t.Fatalf("down script %q", ds)
	}
}

// TestRegistryRefreshMigration is acceptance 8: up, down (with a github row
// holding refresh material) and up again, in one transaction that is
// rolled back. The row and its access token survive the down script.
func TestRegistryRefreshMigration(t *testing.T) {
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
	owner := p.user("mig28")
	proj := p.project(p.org(owner.ID), owner.ID, "mig28")
	access := p.at.Add(time.Hour)
	if _, err := d.PutRegistry(ctx, proj.ID, "ghcr.io", store.RegistryWrite{
		Username: "octo", Nonce: bytes.Repeat([]byte{1}, 12), Ciphertext: bytes.Repeat([]byte{2}, 16), KeyVersion: 1,
		Source: store.RegistrySourceGitHub, ConnectedBy: &owner.ID,
		RegistryRefreshMaterial: store.RegistryRefreshMaterial{RefreshNonce: bytes.Repeat([]byte{3}, 12), RefreshCiphertext: bytes.Repeat([]byte{4}, 16), AccessExpiresAt: &access},
	}, owner.ID, p.at); err != nil {
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
	columns := func() int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, `
SELECT count(*) FROM information_schema.columns
WHERE table_schema = 'app' AND table_name = 'registries'
  AND column_name IN ('refresh_nonce', 'refresh_ciphertext', 'access_expires_at', 'refresh_expires_at')`).Scan(&n); err != nil {
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
	pairHolds := func(when string) {
		t.Helper()
		if _, err := tx.Exec(ctx, `SAVEPOINT pair`); err != nil {
			t.Fatal(err)
		}
		_, err := tx.Exec(ctx, `UPDATE app.registries SET refresh_ciphertext = NULL WHERE project_id = $1::uuid`, proj.ID.String())
		if err == nil || !strings.Contains(err.Error(), "registries_refresh_pair_chk") {
			t.Fatalf("%s: pair check %v", when, err)
		}
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT pair`); err != nil {
			t.Fatal(err)
		}
	}
	if columns() != 4 {
		t.Fatal("after up: columns missing")
	}
	pairHolds("up")
	if err := run("0028_registry_github_refresh.down.sql"); err != nil {
		t.Fatalf("down: %v", err)
	}
	if columns() != 0 {
		t.Fatal("after down: columns left")
	}
	var kept int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.registries WHERE project_id = $1::uuid AND username = 'octo' AND source = 'github' AND length(ciphertext) = 16`, proj.ID.String()).Scan(&kept); err != nil || kept != 1 {
		t.Fatalf("row after down %d %v", kept, err)
	}
	// The down script runs again without refusing.
	if err := run("0028_registry_github_refresh.down.sql"); err != nil {
		t.Fatalf("down twice: %v", err)
	}
	if err := run(refreshMigration); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if columns() != 4 {
		t.Fatal("after up again: columns missing")
	}
	if _, err := tx.Exec(ctx, `UPDATE app.registries SET refresh_nonce = $2, refresh_ciphertext = $3 WHERE project_id = $1::uuid`,
		proj.ID.String(), bytes.Repeat([]byte{3}, 12), bytes.Repeat([]byte{4}, 16)); err != nil {
		t.Fatal(err)
	}
	pairHolds("again")
}

// TestRegistryRefreshAsAPIRole runs the 0028 writes as deploybot_api: the
// callback upsert with refresh material, the conditional refresh, the
// clear and the due list (a missing column grant fails here).
func TestRegistryRefreshAsAPIRole(t *testing.T) {
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
	owner := p.user("rr-api")
	proj := p.project(p.org(owner.ID), owner.ID, "rrapi")
	access := p.at.Add(time.Minute)
	w := store.RegistryWrite{
		Username: "octo", Nonce: bytes.Repeat([]byte{1}, 12), Ciphertext: bytes.Repeat([]byte{2}, 16), KeyVersion: 1,
		Source: store.RegistrySourceGitHub, ConnectedBy: &owner.ID,
		RegistryRefreshMaterial: store.RegistryRefreshMaterial{RefreshNonce: bytes.Repeat([]byte{3}, 12), RefreshCiphertext: bytes.Repeat([]byte{4}, 16), AccessExpiresAt: &access},
	}
	// Twice: the second is the ON CONFLICT update (a reconnect).
	for range 2 {
		if _, err := api.PutRegistry(ctx, proj.ID, "ghcr.io", w, owner.ID, p.at); err != nil {
			t.Fatalf("put as deploybot_api: %v", err)
		}
	}
	due, err := api.ListRegistryRefreshDue(ctx, access.Add(time.Second))
	if err != nil {
		t.Fatalf("due as deploybot_api: %v", err)
	}
	found := false
	for _, l := range due {
		found = found || l.ProjectID == proj.ID
	}
	if !found {
		t.Fatal("row not due")
	}
	next := store.RegistryRefresh{Nonce: bytes.Repeat([]byte{5}, 12), Ciphertext: bytes.Repeat([]byte{6}, 16), KeyVersion: 1,
		RegistryRefreshMaterial: store.RegistryRefreshMaterial{RefreshNonce: bytes.Repeat([]byte{7}, 12), RefreshCiphertext: bytes.Repeat([]byte{8}, 16), AccessExpiresAt: &access}}
	if ok, err := api.RefreshRegistry(ctx, proj.ID, "ghcr.io", w.RefreshNonce, next); !ok || err != nil {
		t.Fatalf("refresh as deploybot_api: %v %v", ok, err)
	}
	if ok, err := api.ClearRegistryRefresh(ctx, proj.ID, "ghcr.io", next.RefreshNonce); !ok || err != nil {
		t.Fatalf("clear as deploybot_api: %v %v", ok, err)
	}
	if err := api.DeleteRegistry(ctx, proj.ID, "ghcr.io"); err != nil {
		t.Fatal(err)
	}
}
