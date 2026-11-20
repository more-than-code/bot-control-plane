package db

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
	"github.com/more-than-code/bot-control-plane/api/migrations"
)

// upTo0015 stops a test migrate after phase 1, the schema the 0015 down
// script and the 0016 upgrade start from.
const upTo0015 = "0015_orgs.sql"

// restorePhase1Schema turns a fully migrated test database back into the
// schema v0.2.30 left (0015 applied, 0016 not): host_shares with its trigger
// and function from 0003, catalog_host_approvals from 0007, and
// hosts.project_id with hosts_project_name_unique. The objects come from the
// migration files themselves. hosts.project_id is filled as the v1.24 API
// wrote it (fillHostMirror). Migrate re-applies 0016 afterwards.
func restorePhase1Schema(t *testing.T, ctx context.Context, url string) {
	t.Helper()
	var stmts []string
	for file, object := range map[string]string{"0003_shared_hosts.sql": "host_shares", "0007_catalog.sql": "catalog_host_approvals"} {
		body, err := migrations.FS.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, stmt := range splitSQL(string(body)) {
			if strings.Contains(stmt, object) {
				stmts = append(stmts, stmt)
			}
		}
	}
	stmts = append(stmts,
		`ALTER TABLE app.hosts ADD COLUMN project_id uuid REFERENCES app.projects (id)`,
		fillHostMirrorSQL,
		`ALTER TABLE app.hosts ADD CONSTRAINT hosts_project_name_unique UNIQUE (project_id, name)`,
		`DELETE FROM app.schema_migrations WHERE name = '0016_orgs_phase2.sql'`,
	)
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
	for _, stmt := range stmts {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("restore phase 1: %s: %v", strings.SplitN(stmt, "\n", 2)[0], err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// fillHostMirrorSQL sets a NULL hosts.project_id the way the v1.24 API did
// at enrolment (review A6): the project with deploy access granted first,
// otherwise the org's oldest live project. Hosts enrolled by the current
// code in a restored phase 1 schema get the mirror before the down script.
const fillHostMirrorSQL = `
UPDATE app.hosts AS h
SET project_id = COALESCE(
    (SELECT a.project_id FROM app.host_access a
     WHERE a.host_id = h.id AND a.level = 'deploy'
     ORDER BY a.granted_at, a.project_id LIMIT 1),
    (SELECT p.id FROM app.projects p
     WHERE p.org_id = h.org_id AND p.deleted_at IS NULL
     ORDER BY p.created_at, p.id LIMIT 1))
WHERE h.project_id IS NULL`

// TestOrgsPhase2Migration is api-v1 v1.26 item 3: a phase 1 (0015) database
// holding same-org and cross-org shares, approvals and hosts.project_id
// migrates to 0016 without BOT_API_ORG_MAPPING_FILE. The legacy objects are
// gone, the per-org name rule and every access row stay, and the API reads
// hosts as before.
func TestOrgsPhase2Migration(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	lockMigrations(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
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
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer ccancel()
		if onTestDatabase(cctx, d) {
			_ = Migrate(cctx, url)
		}
	})
	restorePhase1Schema(t, ctx, url)

	at := time.Now().UTC().Truncate(time.Microsecond)
	tag := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", strings.SplitN(strings.TrimSpace(sql), "\n", 2)[0], err)
		}
	}
	owner := dbUser(t, d, ctx, "p2-owner-"+tag, at)
	stranger := dbUser(t, d, ctx, "p2-stranger-"+tag, at)
	org := personalOrg(t, d, ctx, owner.ID)
	p, err := d.CreateProject(ctx, org, owner.ID, "P2 P", "p2-p-"+tag, at)
	if err != nil {
		t.Fatal(err)
	}
	q, err := d.CreateProject(ctx, org, owner.ID, "P2 Q", "p2-q-"+tag, at)
	if err != nil {
		t.Fatal(err)
	}
	away, err := d.CreateProject(ctx, personalOrg(t, d, ctx, stranger.ID), stranger.ID, "P2 Away", "p2-away-"+tag, at)
	if err != nil {
		t.Fatal(err)
	}
	host := enrollOrgHost(t, d, ctx, org, &store.TokenGrant{ProjectID: p.ID, Level: "deploy"}, owner.ID, "p2-h-"+tag, at)
	if _, _, err := d.PutHostAccess(ctx, host.ID, q.ID, "metrics", owner.ID, at); err != nil {
		t.Fatal(err)
	}
	exec(fillHostMirrorSQL)
	// A same-org share (already converted to access by 0015) and a cross-org
	// share 0015 kept without access.
	exec(`INSERT INTO app.host_shares (host_id, project_id, level, created_by) VALUES ($1::uuid, $2::uuid, 'metrics', $3::uuid)`, host.ID.String(), q.ID.String(), owner.ID.String())
	exec(`INSERT INTO app.host_shares (host_id, project_id, level, created_by) VALUES ($1::uuid, $2::uuid, 'deploy', $3::uuid)`, host.ID.String(), away.ID.String(), owner.ID.String())
	// The trigger is still there in phase 1.
	if _, err := d.pool.Exec(ctx, `INSERT INTO app.host_shares (host_id, project_id, level) VALUES ($1::uuid, $2::uuid, 'metrics')`, host.ID.String(), p.ID.String()); err == nil || !strings.Contains(err.Error(), "host share cannot be the owning project") {
		t.Fatalf("phase 1 trigger %v", err)
	}
	spec, hash, display := catalogSpec(t, "pnpm run dev")
	entry, err := d.CreateCatalogEntry(ctx, p.ID, store.CatalogWrite{Name: "p2-entry", Spec: spec, SpecHash: hash, CommandDisplay: display, Actor: owner.ID, At: at})
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO app.catalog_host_approvals (entry_id, host_id, spec_hash, status, requested_by, requested_at, approved_by, approved_at)
VALUES ($1::uuid, $2::uuid, $3, 'approved', $4::uuid, now(), $4::uuid, now())`, entry.ID.String(), host.ID.String(), hash, owner.ID.String())
	accessBefore := count(t, d, ctx, `SELECT count(*) FROM app.host_access`)

	// 0016 alone needs no mapping choice: OrgSource{} is BOT_API_ORG_MAPPING_FILE unset.
	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	err = MigrateWith(ctx, url, OrgSource{})
	slog.SetDefault(prev)
	if err != nil {
		t.Fatalf("0016 without a mapping: %v", err)
	}
	// restorePhase1Schema made both tables empty, so the counts are this
	// test's rows only: two shares (one cross-org) and one approval.
	for _, want := range []string{
		"org migration phase 2: dropping 2 host_shares rows (1 cross-org, no access) and 1 catalog_host_approvals rows",
		"org migration phase 2: dropping cross-org host shares: " + host.ID.String() + " -> " + away.Slug + " (deploy)",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("0016 notice %q missing from the log:\n%s", want, logs.String())
		}
	}

	if n := count(t, d, ctx, `SELECT count(*) FROM app.schema_migrations WHERE name = '0016_orgs_phase2.sql'`); n != 1 {
		t.Fatalf("0016 recorded %d times", n)
	}
	for _, rel := range []string{"app.host_shares", "app.catalog_host_approvals"} {
		var gone bool
		if err := d.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NULL`, rel).Scan(&gone); err != nil || !gone {
			t.Fatalf("%s still exists %v", rel, err)
		}
	}
	if n := count(t, d, ctx, `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'app' AND p.proname = 'host_shares_not_owner'`); n != 0 {
		t.Fatal("host_shares_not_owner function kept")
	}
	if n := count(t, d, ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = 'app' AND table_name = 'hosts' AND column_name = 'project_id'`); n != 0 {
		t.Fatal("hosts.project_id kept")
	}
	if n := count(t, d, ctx, `SELECT count(*) FROM pg_constraint WHERE conname = 'hosts_project_name_unique'`); n != 0 {
		t.Fatal("hosts_project_name_unique kept")
	}
	// The per-org name rule survives 0016. Since 0025 (api-v1 v1.38) it is
	// the partial index over live hosts, not the plain constraint.
	if n := count(t, d, ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname = 'app' AND indexname = 'hosts_org_name_live_uidx'`); n != 1 {
		t.Fatal("hosts_org_name_live_uidx missing")
	}
	if n := count(t, d, ctx, `SELECT count(*) FROM pg_constraint WHERE conname = 'hosts_org_name_unique'`); n != 0 {
		t.Fatal("hosts_org_name_unique still there after 0025")
	}
	if n := count(t, d, ctx, `SELECT count(*) FROM app.host_access`); n != accessBefore {
		t.Fatalf("host_access rows %d, was %d", n, accessBefore)
	}
	if level, err := d.HostProjectAccess(ctx, p.ID, host.ID); err != nil || level != "deploy" {
		t.Fatalf("deploy access %q %v", level, err)
	}
	if level, err := d.HostProjectAccess(ctx, q.ID, host.ID); err != nil || level != "metrics" {
		t.Fatalf("metrics access %q %v", level, err)
	}
	if level, _ := d.HostProjectAccess(ctx, away.ID, host.ID); level != "" {
		t.Fatalf("cross-org share became access %q", level)
	}
	got, err := d.GetHost(ctx, host.ID)
	if err != nil || got.Name != host.Name || got.OrgID != org {
		t.Fatalf("host after 0016 %+v %v", got, err)
	}
	// A spec change no longer touches an approvals table.
	nextSpec, nextHash, nextDisplay := catalogSpec(t, "pnpm run start")
	if _, _, err := d.ReplaceCatalogEntry(ctx, p.ID, "p2-entry", store.CatalogWrite{Name: "p2-entry", Spec: nextSpec, SpecHash: nextHash, CommandDisplay: nextDisplay, Actor: owner.ID, At: at}); err != nil {
		t.Fatalf("replace after 0016: %v", err)
	}
	// Enrolment and the per-org name rule still work.
	again := enrollOrgHost(t, d, ctx, org, nil, owner.ID, host.Name, at)
	if again.Name != host.Name+"-2" {
		t.Fatalf("per-org suffix %q", again.Name)
	}
	// Migrate again is a no-op.
	if err := MigrateWith(ctx, url, OrgSource{}); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
