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
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// isTestDatabase is the guard every integration test applies before it
// migrates or truncates: the database is deploybot_test or a separate test
// database named deploybot_test_<suffix>. Anything else, deploybot included,
// is refused.
func isTestDatabase(name string) bool {
	const prefix = "deploybot_test_"
	return name == "deploybot_test" || (strings.HasPrefix(name, prefix) && len(name) > len(prefix))
}

// onTestDatabase reports whether d is connected to a test database. Restore
// helpers call it before they migrate.
func onTestDatabase(ctx context.Context, d *DB) bool {
	var name string
	if err := d.pool.QueryRow(ctx, `SELECT current_database()`).Scan(&name); err != nil {
		return false
	}
	return isTestDatabase(name)
}

func TestIsTestDatabase(t *testing.T) {
	for name, want := range map[string]bool{
		"deploybot_test": true, "deploybot_test_progsec": true,
		"deploybot": false, "deploybot_test_": false, "deploybot_testx": false,
		"xdeploybot_test": false, "postgres": false, "": false,
	} {
		if isTestDatabase(name) != want {
			t.Fatalf("isTestDatabase(%q) = %v, want %v", name, !want, want)
		}
	}
}

func TestIntegration(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second) // one context for every assertion below, over the network to the test database
	defer cancel()

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

	legacySession, seededLegacy := seedLegacyMCPSession(t, d, ctx)
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if seededLegacy {
		assertLegacyMCPExpired(t, d, ctx, legacySession)
	}
	replayMCPCutover(t, d, ctx)
	assertOAuthSchema(t, d, ctx)
	if _, err := d.pool.Exec(ctx, `
TRUNCATE TABLE
    app.edge_confirmations,
    app.routes,
    app.domain_assignments,
    app.org_domains,
    app.host_edges,
    app.env_value_requests,
    app.file_requests,
    app.registry_connect_requests,
    app.image_deploys,
    app.env_values,
    app.env_sets,
    app.registries,
    app.project_files,
    app.service_confirmations,
    app.catalog_write_confirmations,
    app.env_set_confirmations,
    app.catalog_confirmations,
    app.catalog_grants,
    app.idempotency_keys,
    app.catalog_entries,
    metrics.ingest_batches,
    metrics.host_cpu,
    metrics.host_mem,
    metrics.host_disk,
    metrics.host_diskio,
    metrics.host_net,
    metrics.host_system,
    metrics.host_temp,
    metrics.process_samples,
    metrics.health_results,
    app.audit_log,
    app.health_checks,
    app.host_access,
    app.services,
    app.agent_credentials,
    app.invites,
    app.join_tokens,
    app.device_codes,
    app.oauth_codes,
    app.oauth_requests,
    app.oauth_grants,
    app.problems,
    app.notification_reads,
    app.host_file_applies,
    app.host_file_versions,
    app.hosts,
    app.project_members,
    app.sessions,
    app.user_identities,
    app.projects,
    app.org_members,
    app.orgs,
    app.users
`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// Migration-seeded internal clients (deploybot-cli) must survive, or the next run's schema check fails.
	if _, err := d.pool.Exec(ctx, `DELETE FROM app.oauth_clients WHERE kind <> 'internal'`); err != nil {
		t.Fatalf("clear clients: %v", err)
	}

	names := map[string]bool{}
	rows, err := d.pool.Query(ctx, `SELECT name FROM app.schema_migrations`)
	if err != nil {
		t.Fatalf("migrations: %v", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("migrations: %v", err)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("migrations: %v", err)
	}
	rows.Close()
	if !names["0001_app.sql"] || !names["0002_metrics.sql"] || !names["0003_shared_hosts.sql"] || !names["0004_service_names.sql"] || !names["0005_mcp_oauth.sql"] || !names["0006_invites.sql"] || !names["0007_catalog.sql"] || !names["0008_service_gone.sql"] || !names["0009_catalog_images.sql"] || !names["0010_project_files.sql"] || !names["0011_service_confirmations.sql"] || !names["0012_revoked_host_rows.sql"] || !names["0013_catalog_write_confirmations.sql"] || !names["0014_auto_actions.sql"] || !names["0015_orgs.sql"] || !names["0016_orgs_phase2.sql"] || !names["0017_env_generate.sql"] || names["0015_orgs.down.sql"] || names["0003_shared_hosts.down.sql"] || names["0004_service_names.down.sql"] || names["0005_mcp_oauth.down.sql"] || names["0006_invites.down.sql"] || names["0007_catalog.down.sql"] || names["0008_service_gone.down.sql"] || names["0009_catalog_images.down.sql"] || names["0010_project_files.down.sql"] || names["0011_service_confirmations.down.sql"] || names["0012_revoked_host_rows.down.sql"] || names["0013_catalog_write_confirmations.down.sql"] || names["0014_auto_actions.down.sql"] || names["0017_env_generate.down.sql"] {
		t.Fatalf("schema_migrations = %v", names)
	}

	at := time.Now().UTC().Truncate(time.Microsecond)
	user, err := d.UpsertGoogleUser(ctx, store.GoogleSignIn{
		Subject: "google-subject",
		Email:   "Owner@Example.com",
		Name:    "Owner",
		At:      at,
	})
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	project, err := d.CreateProject(ctx, personalOrg(t, d, ctx, user.ID), user.ID, "Lab", "lab", at)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	_, tokenHash, err := secret.NewToken()
	if err != nil {
		t.Fatal("token")
	}
	if _, err := d.CreateJoinToken(ctx, project.OrgID, tokenHash, nil, &store.TokenGrant{ProjectID: project.ID, Level: "deploy"}, user.ID, at.Add(time.Hour), at); err != nil {
		t.Fatalf("join token: %v", err)
	}

	type cred struct {
		id   uuid.UUID
		hash []byte
	}
	creds := make([]cred, 32)
	for i := range creds {
		creds[i].id = uuid.New()
		_, hash, err := secret.NewToken()
		if err != nil {
			t.Fatal("credential token")
		}
		creds[i].hash = hash
	}
	in := store.EnrollInput{
		Name:         "pi-node",
		NodeType:     "pi",
		OS:           "linux",
		Arch:         "arm64",
		AlwaysOn:     true,
		AgentVersion: "1.0.0",
	}
	var mu sync.Mutex
	var success int
	var bad []string
	var enrolled store.Host
	var wg sync.WaitGroup
	for i := range creds {
		wg.Add(1)
		go func(c cred) {
			defer wg.Done()
			host, err := d.ConsumeJoinToken(ctx, tokenHash, in, c.id, c.hash, at)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
				enrolled = host
				return
			}
			if !errors.Is(err, store.ErrUnauthorized) {
				bad = append(bad, err.Error())
			}
		}(creds[i])
	}
	wg.Wait()
	if success != 1 {
		t.Fatalf("consume successes = %d, want 1", success)
	}
	if len(bad) > 0 {
		t.Fatalf("consume errors: %v", bad)
	}
	if enrolled.ID == uuid.Nil {
		t.Fatal("enrolled host missing")
	}

	sessPlain, sessHash, err := secret.NewToken()
	if err != nil {
		t.Fatal("session token")
	}
	sess, err := d.CreateSession(ctx, user.ID, sessHash, "console", at.Add(time.Hour), at)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	var stored []byte
	if err := d.pool.QueryRow(ctx, `SELECT token_hash FROM app.sessions WHERE id = $1::uuid`, sess.ID.String()).Scan(&stored); err != nil {
		t.Fatalf("session hash: %v", err)
	}
	if !bytes.Equal(stored, secret.Hash(sessPlain)) {
		t.Fatal("session token_hash is not secret.Hash(plain)")
	}
	if bytes.Equal(stored, []byte(sessPlain)) {
		t.Fatal("session token_hash is plaintext")
	}

	first := at.Add(-time.Hour)
	later := first.Add(time.Minute)
	serviceID := uuid.New()
	if _, err := d.pool.Exec(ctx, `
INSERT INTO app.services (id, host_id, name, source, kind, ports, first_seen_at, last_seen_at, project_id)
VALUES ($1::uuid, $2::uuid, 'web', 'declared', 'process', $3::integer[], $4, $4, $5::uuid)
`, serviceID.String(), enrolled.ID.String(), []int32{80, 443}, first, project.ID.String()); err != nil {
		t.Fatalf("seed service: %v", err)
	}
	if err := d.UpsertDiscovered(ctx, enrolled.ID, []store.Discovered{{
		Name:  "web",
		Kind:  "container",
		Ports: []int{9},
	}}, later, true, true); err != nil {
		t.Fatalf("upsert discovered: %v", err)
	}
	var source string
	var ports []int32
	var seen time.Time
	var projectText string
	if err := d.pool.QueryRow(ctx, `
SELECT source, ports, last_seen_at, project_id::text
FROM app.services
WHERE id = $1::uuid
`, serviceID.String()).Scan(&source, &ports, &seen, &projectText); err != nil {
		t.Fatalf("read service: %v", err)
	}
	if source != "declared" {
		t.Fatalf("source = %s, want declared", source)
	}
	if len(ports) != 2 || ports[0] != 80 || ports[1] != 443 {
		t.Fatalf("ports = %v, want [80 443]", ports)
	}
	if !seen.Equal(later) {
		t.Fatalf("last_seen_at = %s, want %s", seen.Format(time.RFC3339Nano), later.Format(time.RFC3339Nano))
	}
	if projectText != project.ID.String() {
		t.Fatalf("project_id = %s, want %s", projectText, project.ID)
	}
	orgsIntegration(t, d, ctx, user, project, enrolled, at)
	oauthHTTPIntegration(t, d, ctx)
	assertInvites(t, d, ctx)
	assertCatalog(t, d, ctx, at)
	assertImages(t, d, ctx, at)
	assertEnvReplaceKeepsVisibility(t, d, ctx, at)
	assertEnvKeyUpsertDelete(t, d, ctx, at)
	assertEnvMergeCreate(t, d, ctx, at)
	assertEnvMakeSecret(t, d, ctx, at)
	assertProgramSecrets(t, d, ctx, at)
	assertEnvGenerate(t, d, ctx, at)
}

func enrollHostDB(t *testing.T, d *DB, ctx context.Context, projectID, ownerID uuid.UUID, name string, at time.Time) store.Host {
	t.Helper()
	_, tokenHash, err := secret.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	p, err := d.GetProject(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateJoinToken(ctx, p.OrgID, tokenHash, nil, &store.TokenGrant{ProjectID: projectID, Level: "deploy"}, ownerID, at.Add(time.Hour), at); err != nil {
		t.Fatal(err)
	}
	_, credHash, err := secret.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	host, err := d.ConsumeJoinToken(ctx, tokenHash, store.EnrollInput{
		Name: name, NodeType: "pi", OS: "linux", Arch: "arm64", AlwaysOn: true, AgentVersion: "1.0.0",
	}, uuid.New(), credHash, at)
	if err != nil {
		t.Fatal(err)
	}
	return host
}

// reportAllowlistDB gives a host an agent 0.2.8 allowlist (empty is enough),
// so owners of any of its deploy projects map (spec D-10).
func reportAllowlistDB(t *testing.T, d *DB, ctx context.Context, hostID uuid.UUID) {
	t.Helper()
	if err := d.SetHostPrograms(ctx, hostID, &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, Projects: []string{}}); err != nil {
		t.Fatal(err)
	}
}

func deniedCode(err error) string {
	var denied *authz.Denied
	if errors.As(err, &denied) {
		return denied.Code
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func svcSet(items []store.Service) map[string]bool {
	out := map[string]bool{}
	for _, item := range items {
		out[item.Name] = true
	}
	return out
}

func namesOfSvc(items []store.Service) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.Name
	}
	return out
}

func sameID(got, want *uuid.UUID) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}
