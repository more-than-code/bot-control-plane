package db

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
	"github.com/more-than-code/bot-control-plane/api/migrations"
)

// personalOrg is the user's oldest owned org (UpsertGoogleUser creates one, D-8).
func personalOrg(t *testing.T, d *DB, ctx context.Context, userID uuid.UUID) uuid.UUID {
	t.Helper()
	orgs, err := d.ListUserOrgs(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range orgs {
		if o.Role == store.OrgRoleOwner {
			return o.ID
		}
	}
	t.Fatalf("user %s owns no org", userID)
	return uuid.Nil
}

// grantDB gives projectID level on the host. The project must already be in
// the host's org (D-3); tests create it there with projectIn.
func grantDB(t *testing.T, d *DB, ctx context.Context, hostID, projectID uuid.UUID, level string, by uuid.UUID, at time.Time) (store.HostAccess, bool, error) {
	t.Helper()
	return d.PutHostAccess(ctx, hostID, projectID, level, by, at)
}

// projectIn creates a project in orgID with owner as project owner.
func projectIn(t *testing.T, d *DB, ctx context.Context, orgID, owner uuid.UUID, name, slug string, at time.Time) store.Project {
	t.Helper()
	p, err := d.CreateProject(ctx, orgID, owner, name, slug, at)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func dbUser(t *testing.T, d *DB, ctx context.Context, tag string, at time.Time) store.User {
	t.Helper()
	u, err := d.UpsertGoogleUser(ctx, store.GoogleSignIn{Subject: "sub-" + tag, Email: tag + "@example.com", Name: "U " + tag, At: at})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func enrollOrgHost(t *testing.T, d *DB, ctx context.Context, orgID uuid.UUID, grant *store.TokenGrant, by uuid.UUID, name string, at time.Time) store.Host {
	t.Helper()
	_, hash, err := secret.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateJoinToken(ctx, orgID, hash, nil, grant, by, at.Add(time.Hour), at); err != nil {
		t.Fatal(err)
	}
	_, credHash, err := secret.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	host, err := d.ConsumeJoinToken(ctx, hash, store.EnrollInput{
		Name: name, NodeType: "pi", OS: "linux", Arch: "arm64", AlwaysOn: true, AgentVersion: "0.2.8",
	}, uuid.New(), credHash, at)
	if err != nil {
		t.Fatal(err)
	}
	return host
}

// orgsIntegration is the Postgres half of api-v1 v1.24: host facts, access
// grants (cross-org refused), 409 and ?reassign=org, enrolment per org with
// a grant, project delete keeping hosts, org members and reach, org audit,
// org delete.
func orgsIntegration(t *testing.T, d *DB, ctx context.Context, owner store.User, project store.Project, host store.Host, at time.Time) {
	t.Helper()
	org := project.OrgID
	if host.OrgID != org || host.GrantProjectID == nil || *host.GrantProjectID != project.ID {
		t.Fatalf("enrolled host %+v", host)
	}
	facts, err := d.HostFacts(ctx, owner.ID, host.ID)
	if err != nil || facts.OrgRole != "owner" || len(facts.Access) != 1 || facts.Access[0].Level != "deploy" || facts.Access[0].Role != "owner" {
		t.Fatalf("owner facts %+v %v", facts, err)
	}
	if _, err := d.HostFacts(ctx, owner.ID, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing host facts %v", err)
	}

	guest := dbUser(t, d, ctx, "org-guest", at)
	other, err := d.CreateProject(ctx, org, guest.ID, "Other", "org-other", at)
	if err != nil {
		t.Fatal(err)
	}
	away, err := d.CreateProject(ctx, personalOrg(t, d, ctx, guest.ID), guest.ID, "Away", "org-away", at)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.PutHostAccess(ctx, host.ID, away.ID, "deploy", owner.ID, at); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org grant %v", err)
	}
	row, created, err := d.PutHostAccess(ctx, host.ID, other.ID, "metrics", owner.ID, at)
	if err != nil || !created || row.ProjectName != "Other" || row.GrantedBy == nil {
		t.Fatalf("grant %+v %v %v", row, created, err)
	}
	row, created, err = d.PutHostAccess(ctx, host.ID, other.ID, "deploy", guest.ID, at.Add(time.Minute))
	if err != nil || created || row.Level != "deploy" || *row.GrantedBy != owner.ID {
		t.Fatalf("update %+v %v %v", row, created, err)
	}
	gfacts, err := d.HostFacts(ctx, guest.ID, host.ID)
	if err != nil || gfacts.OrgRole != "" || len(gfacts.Access) != 1 || gfacts.Access[0].ProjectID != other.ID {
		t.Fatalf("guest facts %+v %v", gfacts, err)
	}
	listed, next, err := d.ListHosts(ctx, other.ID, 50, nil)
	if err != nil || next != nil || len(listed) != 1 || listed[0].AccessLevel == nil || *listed[0].AccessLevel != "deploy" {
		t.Fatalf("list hosts %+v %v", listed, err)
	}
	orgHosts, _, err := d.ListOrgHosts(ctx, org, 50, nil)
	if err != nil || len(orgHosts) != 1 || orgHosts[0].Access == nil || len(*orgHosts[0].Access) != 2 {
		t.Fatalf("org hosts %+v %v", orgHosts, err)
	}
	if level, err := d.HostProjectAccess(ctx, other.ID, host.ID); err != nil || level != "deploy" {
		t.Fatalf("access level %q %v", level, err)
	}

	// 409 while the project has a live service; ?reassign=org makes it org-level.
	svcID := uuid.New()
	if _, err := d.pool.Exec(ctx, `
INSERT INTO app.services (id, host_id, name, source, kind, ports, first_seen_at, last_seen_at, project_id, catalog_entry)
VALUES ($1::uuid, $2::uuid, 'org-other-svc', 'owned', 'process', '{}', $3, $3, $4::uuid, 'x')
`, svcID.String(), host.ID.String(), at, other.ID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DeleteHostAccess(ctx, host.ID, other.ID, false); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("conflict %v", err)
	}
	if _, err := d.DeleteHostAccess(ctx, host.ID, other.ID, true); err != nil {
		t.Fatal(err)
	}
	svc, err := d.GetService(ctx, svcID, at)
	if err != nil || svc.ProjectID != nil || svc.Source != "discovered" || svc.CatalogEntry != nil {
		t.Fatalf("reassigned %+v %v", svc, err)
	}
	all, err := d.ListServices(ctx, host.ID, nil, at)
	if err != nil || !svcSet(all)["org-other-svc"] {
		t.Fatalf("org-level row missing from unfiltered list %v %v", namesOfSvc(all), err)
	}
	filtered, err := d.ListServices(ctx, host.ID, []uuid.UUID{project.ID, other.ID}, at)
	if err != nil || svcSet(filtered)["org-other-svc"] {
		t.Fatalf("org-level row in a project filter %v %v", namesOfSvc(filtered), err)
	}

	// Enrolment per org: grant applied, name unique per org, grant project
	// named for the audit row.
	grantHost := enrollOrgHost(t, d, ctx, org, &store.TokenGrant{ProjectID: other.ID, Level: "deploy"}, owner.ID, host.Name, at)
	if grantHost.Name != host.Name+"-2" || grantHost.GrantProjectID == nil || *grantHost.GrantProjectID != other.ID {
		t.Fatalf("grant host %+v", grantHost)
	}
	if level, err := d.HostProjectAccess(ctx, other.ID, grantHost.ID); err != nil || level != "deploy" {
		t.Fatalf("enrolment grant %q %v", level, err)
	}
	bare := enrollOrgHost(t, d, ctx, org, nil, owner.ID, "org-bare", at)
	got, err := d.GetHost(ctx, bare.ID)
	// A host enrolled without a grant belongs to the org only.
	if err != nil || bare.GrantProjectID != nil || got.OrgID != org {
		t.Fatalf("bare host %+v %v", got, err)
	}
	awayHost := enrollOrgHost(t, d, ctx, away.OrgID, &store.TokenGrant{ProjectID: away.ID, Level: "deploy"}, guest.ID, host.Name, at)
	if awayHost.Name != host.Name {
		t.Fatalf("another org may reuse the name: %s", awayHost.Name)
	}
	_, hash, _ := secret.NewToken()
	if _, err := d.CreateJoinToken(ctx, org, hash, nil, &store.TokenGrant{ProjectID: away.ID, Level: "deploy"}, owner.ID, at.Add(time.Hour), at); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org token grant %v", err)
	}
	tokens, _, err := d.ListJoinTokens(ctx, org, &other.ID, 50, nil)
	if err != nil || len(tokens) != 1 || tokens[0].Grant == nil || tokens[0].Grant.ProjectID != other.ID {
		t.Fatalf("project tokens %+v %v", tokens, err)
	}

	// Org members, reach and audit.
	if _, err := d.AddOrgMember(ctx, org, guest.ID, "admin", owner.ID, at); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddOrgMember(ctx, org, guest.ID, "member", owner.ID, at); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate member %v", err)
	}
	reach, err := d.ListOrgProjects(ctx, org, guest.ID)
	if err != nil {
		t.Fatal(err)
	}
	var viaOrg bool
	for _, p := range reach {
		if p.ID == project.ID && p.Role == "admin" && p.Via == "org" {
			viaOrg = true
		}
	}
	if !viaOrg {
		t.Fatalf("org admin reach %+v", reach)
	}
	pa, err := d.ProjectAccess(ctx, project.ID, guest.ID)
	if err != nil || pa.MemberRole != "" || pa.OrgRole != "admin" || pa.OrgID != org {
		t.Fatalf("project access %+v %v", pa, err)
	}
	if _, err := d.UpdateOrgMemberRole(ctx, org, owner.ID, "member"); !errors.Is(err, store.ErrOwner) {
		t.Fatalf("demote owner %v", err)
	}
	if err := d.TransferOrgOwnership(ctx, org, guest.ID); err != nil {
		t.Fatal(err)
	}
	if r, _ := d.OrgRole(ctx, org, owner.ID); r != "admin" {
		t.Fatalf("old owner %q", r)
	}
	if err := d.TransferOrgOwnership(ctx, org, owner.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveOrgMember(ctx, org, guest.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertAudit(ctx, store.AuditRow{At: at, Via: "console", OrgID: &org, HostID: &host.ID, Action: "host.access_grant", Decision: "allowed"}); err != nil {
		t.Fatal(err)
	}
	rows, _, err := d.ListOrgAudit(ctx, org, 50, nil)
	if err != nil || len(rows) == 0 || rows[0].OrgID == nil || *rows[0].OrgID != org {
		t.Fatalf("org audit %+v %v", rows, err)
	}

	// Project delete keeps hosts (v1.24 item 7).
	if err := d.DeleteProject(ctx, other.ID, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	gh, err := d.GetHost(ctx, grantHost.ID)
	if err != nil || gh.RevokedAt != nil {
		t.Fatalf("project delete revoked a host %+v %v", gh, err)
	}
	if level, _ := d.HostProjectAccess(ctx, other.ID, grantHost.ID); level != "" {
		t.Fatalf("access kept %q", level)
	}

	// Org delete revokes the org's hosts.
	doomed, err := d.CreateOrg(ctx, guest.ID, "Doomed", "doomed-org", at)
	if err != nil || doomed.Slug != "doomed-org" {
		t.Fatalf("create org %+v %v", doomed, err)
	}
	dp, err := d.CreateProject(ctx, doomed.ID, guest.ID, "Doomed", "doomed-proj", at)
	if err != nil {
		t.Fatal(err)
	}
	dh := enrollOrgHost(t, d, ctx, doomed.ID, &store.TokenGrant{ProjectID: dp.ID, Level: "deploy"}, guest.ID, "doomed-host", at)
	ids, err := d.DeleteOrg(ctx, doomed.ID, at)
	if err != nil || len(ids) != 1 || ids[0] != dh.ID {
		t.Fatalf("delete org %v %v", ids, err)
	}
	if h, _ := d.GetHost(ctx, dh.ID); h.RevokedAt == nil {
		t.Fatal("org delete left the host live")
	}
	if _, err := d.GetProject(ctx, dp.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("org delete left the project %v", err)
	}
}

// TestOrgsMigration covers migration 0015 and its down script on Postgres
// (acceptance 1 and 9): the round trip makes the data v0.2.29-readable, the
// default rule backfills one org per owner, the mapping places projects,
// require_all_projects refuses a gap, and a bad mapping applies nothing.
func TestOrgsMigration(t *testing.T) {
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
		// Leave the database migrated for the next test.
		cctx, ccancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer ccancel()
		if onTestDatabase(cctx, d) {
			_ = Migrate(cctx, url)
		}
	})
	// 0016 dropped what the down script needs: start from phase 1.
	restorePhase1Schema(t, ctx, url)
	at := time.Now().UTC().Truncate(time.Microsecond)
	tag := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	owner := dbUser(t, d, ctx, "mig-owner-"+tag, at)
	second := dbUser(t, d, ctx, "mig-second-"+tag, at)
	org := personalOrg(t, d, ctx, owner.ID)
	p, err := d.CreateProject(ctx, org, owner.ID, "Mig P", "mig-p-"+tag, at)
	if err != nil {
		t.Fatal(err)
	}
	q, err := d.CreateProject(ctx, org, owner.ID, "Mig Q", "mig-q-"+tag, at)
	if err != nil {
		t.Fatal(err)
	}
	r, err := d.CreateProject(ctx, personalOrg(t, d, ctx, second.ID), second.ID, "Mig R", "mig-r-"+tag, at)
	if err != nil {
		t.Fatal(err)
	}
	h1 := enrollOrgHost(t, d, ctx, org, &store.TokenGrant{ProjectID: p.ID, Level: "deploy"}, owner.ID, "mig-h1-"+tag, at)
	h2 := enrollOrgHost(t, d, ctx, org, nil, owner.ID, "mig-h2-"+tag, at)
	// Same name in another org: allowed now, renamed when the mapping puts
	// r into the same org as h1 (it enrolled later).
	h3 := enrollOrgHost(t, d, ctx, r.OrgID, &store.TokenGrant{ProjectID: r.ID, Level: "deploy"}, second.ID, h1.Name, at.Add(time.Second))
	// A second collision in the same target org, at the 63-character limit
	// (review B13): the earlier copy is revoked, so the live later copy keeps
	// the name and the revoked one is renamed.
	longName := ("mig-long-" + tag + "-" + strings.Repeat("x", 63))[:63]
	hLongOld := enrollOrgHost(t, d, ctx, org, nil, owner.ID, longName, at)
	if err := d.RevokeHost(ctx, hLongOld.ID, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	hLongLive := enrollOrgHost(t, d, ctx, r.OrgID, &store.TokenGrant{ProjectID: r.ID, Level: "deploy"}, second.ID, longName, at.Add(2*time.Second))
	if err := d.UpsertDiscovered(ctx, h1.ID, []store.Discovered{{Name: "mig-sshd", Kind: "process", Ports: []int{22}}}, at, true, true); err != nil {
		t.Fatal(err)
	}
	// Metrics access from h1 to q, same org: the down script writes it back as
	// a v0.2.29 share row (review B9), and 0015 turns it into access again.
	if _, _, err := d.PutHostAccess(ctx, h1.ID, q.ID, "metrics", owner.ID, at); err != nil {
		t.Fatal(err)
	}

	// 1. The down script makes every row v0.2.29-readable.
	runDown(t, ctx, url)
	for _, col := range []string{"hosts.project_id", "services.project_id", "join_tokens.project_id", "service_confirmations.project_id"} {
		table, column, _ := strings.Cut(col, ".")
		var nullable string
		if err := d.pool.QueryRow(ctx, `
SELECT is_nullable FROM information_schema.columns
WHERE table_schema = 'app' AND table_name = $1 AND column_name = $2
`, table, column).Scan(&nullable); err != nil || nullable != "NO" {
			t.Fatalf("%s nullable %q %v", col, nullable, err)
		}
	}
	if got := hostShareRows(t, d, ctx, h1.ID); got != q.ID.String()+"/metrics" {
		t.Fatalf("h1 shares after down %q", got)
	}
	shareBefore := legacyShareRows(t, d, ctx)
	for _, sql := range []string{
		`SELECT id::text, project_id::text, name FROM app.hosts`,
		`SELECT id::text, host_id::text, project_id::text, name FROM app.services`,
		`SELECT id::text, project_id::text FROM app.join_tokens`,
	} {
		assertNonNullScan(t, d, ctx, sql)
	}
	var h2Project string
	if err := d.pool.QueryRow(ctx, `SELECT project_id::text FROM app.hosts WHERE id = $1::uuid`, h2.ID.String()).Scan(&h2Project); err != nil || (h2Project != p.ID.String() && h2Project != q.ID.String()) {
		t.Fatalf("no-grant host project %q %v", h2Project, err)
	}
	var sshdProject string
	if err := d.pool.QueryRow(ctx, `SELECT project_id::text FROM app.services WHERE host_id = $1::uuid AND name = 'mig-sshd'`, h1.ID.String()).Scan(&sshdProject); err != nil || sshdProject != p.ID.String() {
		t.Fatalf("org-level service project %q %v", sshdProject, err)
	}
	var orgsLeft bool
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('app.orgs') IS NOT NULL`).Scan(&orgsLeft); err != nil || orgsLeft {
		t.Fatalf("orgs table kept %v %v", orgsLeft, err)
	}

	// 2. A bad mapping and require_all_projects apply nothing.
	if _, err := d.pool.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '0015_orgs.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := migrateUntil(ctx, url, OrgSource{}, upTo0015); err == nil || !strings.Contains(err.Error(), "needs BOT_API_ORG_MAPPING_FILE") {
		t.Fatalf("pending 0015 without a mapping choice: %v", err)
	}
	unknown := &OrgMapping{Orgs: []OrgMappingOrg{{Slug: "mig-x-" + tag, Name: "X", OwnerEmail: owner.Email, Projects: []string{"no-such-" + tag}}}}
	if err := migrateUntil(ctx, url, OrgSource{Chosen: true, Mapping: unknown}, upTo0015); err == nil || !strings.Contains(err.Error(), "unknown project slug") {
		t.Fatalf("unknown slug %v", err)
	}
	strict := &OrgMapping{RequireAllProjects: true, Orgs: []OrgMappingOrg{{Slug: "mig-x-" + tag, Name: "X", OwnerEmail: owner.Email, Projects: []string{p.Slug}}}}
	if err := migrateUntil(ctx, url, OrgSource{Chosen: true, Mapping: strict}, upTo0015); err == nil || !strings.Contains(err.Error(), "require_all_projects") || !strings.Contains(err.Error(), q.Slug) {
		t.Fatalf("require_all %v", err)
	}
	// The SQL duplicate-slug check (review B14), reached without
	// ParseOrgMapping, which refuses the same mapping first.
	dup := &OrgMapping{Orgs: []OrgMappingOrg{
		{Slug: "mig-d1-" + tag, Name: "D1", OwnerEmail: owner.Email, Projects: []string{p.Slug}},
		{Slug: "mig-d2-" + tag, Name: "D2", OwnerEmail: owner.Email, Projects: []string{p.Slug}},
	}}
	if err := migrateUntil(ctx, url, OrgSource{Chosen: true, Mapping: dup}, upTo0015); err == nil || !strings.Contains(err.Error(), "a project slug is listed more than once") {
		t.Fatalf("duplicate slug %v", err)
	}
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('app.orgs') IS NOT NULL`).Scan(&orgsLeft); err != nil || orgsLeft {
		t.Fatalf("a refused mapping applied 0015 %v %v", orgsLeft, err)
	}

	// 3. The mapping places p, q and r in one org; h3 collides with h1 and is renamed.
	mapped := &OrgMapping{Orgs: []OrgMappingOrg{{Slug: "mig-mtc-" + tag, Name: "More Than Code " + tag, OwnerEmail: strings.ToUpper(owner.Email), Projects: []string{p.Slug, q.Slug, r.Slug}}}}
	if err := migrateUntil(ctx, url, OrgSource{Chosen: true, Mapping: mapped}, upTo0015); err != nil {
		t.Fatalf("mapped migrate: %v", err)
	}
	assertOrgOf(t, d, ctx, p.ID, "mig-mtc-"+tag, owner.ID)
	assertOrgOf(t, d, ctx, q.ID, "mig-mtc-"+tag, owner.ID)
	assertOrgOf(t, d, ctx, r.ID, "mig-mtc-"+tag, owner.ID)
	wantName := h1.Name + "-" + h3.ID.String()[:8]
	var h3Name, from, to, renameOrg string
	if err := d.pool.QueryRow(ctx, `SELECT name FROM app.hosts WHERE id = $1::uuid`, h3.ID.String()).Scan(&h3Name); err != nil || h3Name != wantName {
		t.Fatalf("renamed host %q want %q %v", h3Name, wantName, err)
	}
	if err := d.pool.QueryRow(ctx, `
SELECT a.detail->>'from', a.detail->>'to', o.slug
FROM app.audit_log a JOIN app.orgs o ON o.id = a.org_id
WHERE a.action = 'host.migration_rename' AND a.host_id = $1::uuid
ORDER BY a.id DESC LIMIT 1
`, h3.ID.String()).Scan(&from, &to, &renameOrg); err != nil || from != h1.Name || to != wantName || renameOrg != "mig-mtc-"+tag {
		t.Fatalf("rename audit %q %q %q %v", from, to, renameOrg, err)
	}
	wantLong := longName[:54] + "-" + hLongOld.ID.String()[:8]
	var oldName, liveName string
	if err := d.pool.QueryRow(ctx, `SELECT name FROM app.hosts WHERE id = $1::uuid`, hLongOld.ID.String()).Scan(&oldName); err != nil || oldName != wantLong || len(oldName) != 63 {
		t.Fatalf("revoked long host %q want %q %v", oldName, wantLong, err)
	}
	if err := d.pool.QueryRow(ctx, `SELECT name FROM app.hosts WHERE id = $1::uuid`, hLongLive.ID.String()).Scan(&liveName); err != nil || liveName != longName {
		t.Fatalf("live long host %q want %q %v", liveName, longName, err)
	}
	var hostOrg string
	if err := d.pool.QueryRow(ctx, `SELECT o.slug FROM app.hosts h JOIN app.orgs o ON o.id = h.org_id WHERE h.id = $1::uuid`, h1.ID.String()).Scan(&hostOrg); err != nil || hostOrg != "mig-mtc-"+tag {
		t.Fatalf("host org %q %v", hostOrg, err)
	}
	if level, err := d.HostProjectAccess(ctx, p.ID, h1.ID); err != nil || level != "deploy" {
		t.Fatalf("former owner access %q %v", level, err)
	}
	if level, err := d.HostProjectAccess(ctx, q.ID, h1.ID); err != nil || level != "metrics" {
		t.Fatalf("share became access %q %v", level, err)
	}
	var sshdNull bool
	if err := d.pool.QueryRow(ctx, `SELECT project_id IS NULL FROM app.services WHERE host_id = $1::uuid AND name = 'mig-sshd'`, h1.ID.String()).Scan(&sshdNull); err != nil || !sshdNull {
		t.Fatalf("discovered service not org-level %v %v", sshdNull, err)
	}
	if legacyShareRows(t, d, ctx) != shareBefore {
		t.Fatal("migration changed host_shares")
	}
	var members int
	if err := d.pool.QueryRow(ctx, `
SELECT count(*) FROM app.org_members m JOIN app.orgs o ON o.id = m.org_id WHERE o.slug = $1
`, "mig-mtc-"+tag).Scan(&members); err != nil || members != 1 {
		t.Fatalf("mapped org members %d %v", members, err)
	}

	// 4. Round trip once more, then the default rule only.
	runDown(t, ctx, url)
	if _, err := d.pool.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '0015_orgs.sql'`); err != nil {
		t.Fatal(err)
	}
	if err := migrateUntil(ctx, url, OrgSource{Chosen: true}, upTo0015); err != nil {
		t.Fatalf("default migrate: %v", err)
	}
	assertOrgOf(t, d, ctx, p.ID, store.PersonalOrgSlug(owner.ID), owner.ID)
	assertOrgOf(t, d, ctx, q.ID, store.PersonalOrgSlug(owner.ID), owner.ID)
	assertOrgOf(t, d, ctx, r.ID, store.PersonalOrgSlug(second.ID), second.ID)
}

// runDown runs 0015_orgs.down.sql as deploybot_owner on one connection, after
// filling hosts.project_id as the v1.24 API did (fillHostMirrorSQL).
func runDown(t *testing.T, ctx context.Context, url string) {
	t.Helper()
	body, err := migrations.FS.ReadFile("0015_orgs.down.sql")
	if err != nil {
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
	// 0023 references app.orgs, which the 0015 down drops: take 0023 down
	// first (its rows, then its down script). Migrate re-applies it.
	dropEdgeSchema(t, ctx, conn)
	if _, err := conn.Exec(ctx, fillHostMirrorSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.PgConn().Exec(ctx, string(body)).ReadAll(); err != nil {
		t.Fatalf("down script: %v", err)
	}
}

// dropEdgeSchema clears the 0023 rows and runs its down script on conn
// (already deploybot_owner), then forgets 0023 so Migrate applies it again.
// A database without 0023 is left as it is.
func dropEdgeSchema(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'app' AND table_name = 'routes'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		return
	}
	for _, table := range []string{"edge_confirmations", "routes", "domain_assignments", "org_domains", "host_edges"} {
		if _, err := conn.Exec(ctx, `DELETE FROM app.`+table); err != nil {
			t.Fatalf("clear %s: %v", table, err)
		}
	}
	body, err := migrations.FS.ReadFile("0023_domains_edge_routes.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.PgConn().Exec(ctx, string(body)).ReadAll(); err != nil {
		t.Fatalf("0023 down: %v", err)
	}
	if _, err := conn.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name = '`+edgeMigration+`'`); err != nil {
		t.Fatal(err)
	}
}

// applyEdgeSchema applies 0023 alone (it needs only 0015 and earlier) and
// records it, as Migrate would.
func applyEdgeSchema(t *testing.T, ctx context.Context, url string) {
	t.Helper()
	body, err := migrations.FS.ReadFile(edgeMigration)
	if err != nil {
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
	for _, stmt := range splitSQL(string(body)) {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			t.Fatalf("0023: %v", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.schema_migrations (name) VALUES ($1)`, edgeMigration); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func legacyShareRows(t *testing.T, d *DB, ctx context.Context) string {
	t.Helper()
	var out string
	if err := d.pool.QueryRow(ctx, `
SELECT COALESCE(string_agg(host_id::text || '/' || project_id::text || '/' || level || '/' || created_at::text, ',' ORDER BY host_id, project_id), '')
FROM app.host_shares
`).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// hostShareRows lists one host's host_shares rows as project/level.
func hostShareRows(t *testing.T, d *DB, ctx context.Context, hostID uuid.UUID) string {
	t.Helper()
	var out string
	if err := d.pool.QueryRow(ctx, `
SELECT COALESCE(string_agg(project_id::text || '/' || level, ',' ORDER BY project_id), '')
FROM app.host_shares WHERE host_id = $1::uuid
`, hostID.String()).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// assertNonNullScan reads every row into non-null strings, as v0.2.29 does.
func assertNonNullScan(t *testing.T, d *DB, ctx context.Context, sql string) {
	t.Helper()
	rows, err := d.pool.Query(ctx, sql)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := len(rows.FieldDescriptions())
	for rows.Next() {
		dest := make([]any, n)
		for i := range dest {
			dest[i] = new(string)
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func assertOrgOf(t *testing.T, d *DB, ctx context.Context, projectID uuid.UUID, slug string, owner uuid.UUID) {
	t.Helper()
	var gotSlug, gotOwner string
	err := d.pool.QueryRow(ctx, `
SELECT o.slug, m.user_id::text
FROM app.projects p
JOIN app.orgs o ON o.id = p.org_id
JOIN app.org_members m ON m.org_id = o.id AND m.role = 'owner'
WHERE p.id = $1::uuid
`, projectID.String()).Scan(&gotSlug, &gotOwner)
	if err != nil || gotSlug != slug || gotOwner != owner.String() {
		t.Fatalf("project %s org %q owner %q, want %q %s: %v", projectID, gotSlug, gotOwner, slug, owner, err)
	}
}

// TestOrgsMigrationProductionShape is review D24: 0015 on legacy (0014)
// data shaped like production (spec §2.3), with a §4.1 mapping and
// require_all_projects, then the down script with each item-9 state, and
// v0.2.29-shaped reads of owners and shares.
func TestOrgsMigrationProductionShape(t *testing.T) {
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
	runDown(t, ctx, url)
	tag := strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := d.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", strings.SplitN(strings.TrimSpace(sql), "\n", 2)[0], err)
		}
	}
	// Other test data must not trip require_all_projects.
	exec(`UPDATE app.projects SET deleted_at = now() WHERE deleted_at IS NULL`)
	joe, uat := uuid.New(), uuid.New()
	exec(`INSERT INTO app.users (id, email, name, avatar_url) VALUES ($1::uuid, $2, 'Joe', ''), ($3::uuid, $4, 'UAT owner', '')`,
		joe.String(), "joe-"+tag+"@example.com", uat.String(), "uat-"+tag+"@example.com")
	slugs := []string{"deploybot", "mohiguide", "ai-hub", "daikou", "defect-drainer", "ai-hub-uat"}
	pid := map[string]uuid.UUID{}
	for _, s := range slugs {
		id := uuid.New()
		pid[s] = id
		owner := joe
		if s == "ai-hub-uat" {
			owner = uat
		}
		exec(`INSERT INTO app.projects (id, name, slug, created_by) VALUES ($1::uuid, $2, $3, $4::uuid)`, id.String(), s, s+"-"+tag, owner.String())
		exec(`INSERT INTO app.project_members (project_id, user_id, role, added_by) VALUES ($1::uuid, $2::uuid, 'owner', $2::uuid)`, id.String(), owner.String())
	}
	exec(`INSERT INTO app.project_members (project_id, user_id, role, added_by) VALUES ($1::uuid, $2::uuid, 'admin', $3::uuid)`, pid["ai-hub-uat"].String(), joe.String(), uat.String())
	hid := map[string]uuid.UUID{}
	host := func(key, project, name string, revoked bool) {
		id := uuid.New()
		hid[key] = id
		var rev any
		if revoked {
			rev = time.Now().UTC()
		}
		exec(`INSERT INTO app.hosts (id, project_id, name, node_type, os, arch, always_on, agent_version, enrolled_at, revoked_at)
VALUES ($1::uuid, $2::uuid, $3, 'pi', 'linux', 'amd64', true, '0.2.8', now(), $4)`, id.String(), pid[project].String(), name, rev)
	}
	host("us1", "deploybot", "us1-"+tag, false)
	host("hk", "mohiguide", "hk-"+tag, false)
	host("hkdup", "deploybot", "hk-"+tag, true) // revoked duplicate, other org after 0015
	host("aihub", "ai-hub", "aihub-"+tag, false)
	host("daikou", "daikou", "daikou-"+tag, false)
	host("pi2", "defect-drainer", "pi2-"+tag, true)
	host("uat", "ai-hub-uat", "uat-"+tag, false)
	exec(`INSERT INTO app.host_shares (host_id, project_id, level, created_by) VALUES ($1::uuid, $2::uuid, 'deploy', $3::uuid)`, hid["daikou"].String(), pid["defect-drainer"].String(), joe.String())
	exec(`INSERT INTO app.host_shares (host_id, project_id, level, created_by) VALUES ($1::uuid, $2::uuid, 'metrics', $3::uuid)`, hid["us1"].String(), pid["ai-hub"].String(), joe.String())
	svc := func(hostKey, project, name, source string, entry any) uuid.UUID {
		id := uuid.New()
		exec(`INSERT INTO app.services (id, host_id, name, source, kind, ports, first_seen_at, last_seen_at, project_id, catalog_entry)
VALUES ($1::uuid, $2::uuid, $3, $4, 'process', '{}', now(), now(), $5::uuid, $6)`, id.String(), hid[hostKey].String(), name, source, pid[project].String(), entry)
		return id
	}
	ownedSvc := svc("us1", "deploybot", "api", "owned", "api")
	declaredSvc := svc("hk", "mohiguide", "web", "declared", nil)
	discoveredSvc := svc("daikou", "daikou", "sshd", "discovered", nil)
	_, tokHash, _ := secret.NewToken()
	exec(`INSERT INTO app.join_tokens (project_id, token_hash, created_by, expires_at) VALUES ($1::uuid, $2, $3::uuid, now() + interval '1 day')`, pid["daikou"].String(), tokHash, joe.String())
	membersBefore := count(t, d, ctx, `SELECT count(*) FROM app.project_members`)

	mapping := &OrgMapping{RequireAllProjects: true, Orgs: []OrgMappingOrg{
		{Slug: "mtc-" + tag, Name: "More Than Code " + tag, OwnerEmail: "joe-" + tag + "@example.com", Projects: []string{"deploybot-" + tag, "daikou-" + tag, "defect-drainer-" + tag}},
		{Slug: "sae-" + tag, Name: "SaasifyEase " + tag, OwnerEmail: "joe-" + tag + "@example.com", Projects: []string{"mohiguide-" + tag, "ai-hub-" + tag, "ai-hub-uat-" + tag}},
	}}
	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	err = migrateUntil(ctx, url, OrgSource{Chosen: true, Mapping: mapping}, upTo0015)
	slog.SetDefault(prev)
	if err != nil {
		t.Fatalf("mapped migrate: %v", err)
	}
	// The project delete below runs today's code, which clears the 0023
	// tables: put 0023 back on the phase 1 schema (runDown drops it again).
	applyEdgeSchema(t, ctx, url)

	// Placement.
	for s, org := range map[string]string{"deploybot": "mtc", "daikou": "mtc", "defect-drainer": "mtc", "mohiguide": "sae", "ai-hub": "sae", "ai-hub-uat": "sae"} {
		assertOrgOf(t, d, ctx, pid[s], org+"-"+tag, joe)
	}
	for key, org := range map[string]string{"us1": "mtc", "hkdup": "mtc", "daikou": "mtc", "pi2": "mtc", "hk": "sae", "aihub": "sae", "uat": "sae"} {
		var got string
		if err := d.pool.QueryRow(ctx, `SELECT o.slug FROM app.hosts h JOIN app.orgs o ON o.id = h.org_id WHERE h.id = $1::uuid`, hid[key].String()).Scan(&got); err != nil || got != org+"-"+tag {
			t.Fatalf("host %s org %q %v", key, got, err)
		}
	}
	// No extra org members: Joe owns both, the UAT owner is not added (D-2).
	if n := count(t, d, ctx, `SELECT count(*) FROM app.org_members m JOIN app.orgs o ON o.id = m.org_id WHERE o.slug IN ($1, $2)`, "mtc-"+tag, "sae-"+tag); n != 2 {
		t.Fatalf("org members %d", n)
	}
	if n := count(t, d, ctx, `SELECT count(*) FROM app.project_members`); n != membersBefore {
		t.Fatalf("project members %d, was %d", n, membersBefore)
	}
	// Access: every former owner deploy, the same-org share as is.
	for key, project := range map[string]string{"us1": "deploybot", "hk": "mohiguide", "aihub": "ai-hub", "daikou": "daikou", "uat": "ai-hub-uat"} {
		if level, err := d.HostProjectAccess(ctx, pid[project], hid[key]); err != nil || level != "deploy" {
			t.Fatalf("%s/%s access %q %v", key, project, level, err)
		}
	}
	if level, _ := d.HostProjectAccess(ctx, pid["defect-drainer"], hid["daikou"]); level != "deploy" {
		t.Fatalf("same-org share %q", level)
	}
	if level, _ := d.HostProjectAccess(ctx, pid["ai-hub"], hid["us1"]); level != "" {
		t.Fatalf("cross-org share became access %q", level)
	}
	if !strings.Contains(logs.String(), hid["us1"].String()+" -> ai-hub-"+tag) || !strings.Contains(logs.String(), "no access granted") {
		t.Fatalf("cross-org notice missing from the log:\n%s", logs.String())
	}
	// No rename: the HK pair ends up in different orgs.
	if n := count(t, d, ctx, `SELECT count(*) FROM app.audit_log WHERE action = 'host.migration_rename' AND host_id = ANY(CAST($1 AS uuid[]))`, []string{hid["hk"].String(), hid["hkdup"].String()}); n != 0 {
		t.Fatalf("renames %d", n)
	}
	// Services: owned and declared keep their project, discovered is org-level.
	if p := svcProject(t, d, ctx, ownedSvc); p == nil || *p != pid["deploybot"].String() {
		t.Fatalf("owned %v", p)
	}
	if p := svcProject(t, d, ctx, declaredSvc); p == nil || *p != pid["mohiguide"].String() {
		t.Fatalf("declared %v", p)
	}
	if p := svcProject(t, d, ctx, discoveredSvc); p != nil {
		t.Fatalf("discovered %v", *p)
	}
	var grantProject, grantLevel string
	if err := d.pool.QueryRow(ctx, `SELECT grant_project_id::text, grant_level FROM app.join_tokens WHERE token_hash = $1`, tokHash).Scan(&grantProject, &grantLevel); err != nil || grantProject != pid["daikou"].String() || grantLevel != "deploy" {
		t.Fatalf("token grant %s %s %v", grantProject, grantLevel, err)
	}

	// Item-9 states before the down script.
	// A post-release grant: deploybot gets metrics on the Daikou host.
	if _, _, err := d.PutHostAccess(ctx, hid["daikou"], pid["deploybot"], "metrics", joe, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// A deleted project owning a live host: the HK host loses its only deploy project.
	if err := d.DeleteProject(ctx, pid["mohiguide"], time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// A bare host (no grant) in More Than Code.
	mtc := orgID(t, d, ctx, "mtc-"+tag)
	bare := enrollOrgHost(t, d, ctx, mtc, nil, joe, "bare-"+tag, time.Now().UTC())
	// An org with no project, with a host: the down script refuses until it is deleted.
	empty, err := d.CreateOrg(ctx, joe, "Empty", "empty-"+tag, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	lonely := enrollOrgHost(t, d, ctx, empty.ID, nil, joe, "lonely-"+tag, time.Now().UTC())
	if err := tryDown(ctx, url); err == nil || !strings.Contains(err.Error(), "never had a project") {
		t.Fatalf("down with an org without projects: %v", err)
	}
	exec(`DELETE FROM app.agent_credentials WHERE host_id = $1::uuid`, lonely.ID.String())
	exec(`UPDATE app.join_tokens SET used_by_host_id = NULL WHERE used_by_host_id = $1::uuid`, lonely.ID.String())
	exec(`DELETE FROM app.hosts WHERE id = $1::uuid`, lonely.ID.String())
	runDown(t, ctx, url)

	// v0.2.29 reads: owners and shares.
	owner := func(h uuid.UUID) (string, bool) {
		t.Helper()
		var p string
		var revoked bool
		if err := d.pool.QueryRow(ctx, `SELECT project_id::text, revoked_at IS NOT NULL FROM app.hosts WHERE id = $1::uuid`, h.String()).Scan(&p, &revoked); err != nil {
			t.Fatal(err)
		}
		return p, revoked
	}
	for key, project := range map[string]string{"us1": "deploybot", "aihub": "ai-hub", "daikou": "daikou", "uat": "ai-hub-uat"} {
		if p, revoked := owner(hid[key]); p != pid[project].String() || revoked {
			t.Fatalf("%s owner %s revoked %v", key, p, revoked)
		}
	}
	if _, revoked := owner(hid["hk"]); !revoked {
		t.Fatal("live host of a deleted project was not revoked")
	}
	if p, revoked := owner(bare.ID); !revoked || p == "" {
		t.Fatalf("bare host %s revoked %v", p, revoked)
	}
	shares := legacyShareRows(t, d, ctx)
	for _, want := range []string{
		hid["daikou"].String() + "/" + pid["defect-drainer"].String() + "/deploy",
		hid["daikou"].String() + "/" + pid["deploybot"].String() + "/metrics",
		hid["us1"].String() + "/" + pid["ai-hub"].String() + "/metrics",
	} {
		if !strings.Contains(shares, want) {
			t.Fatalf("share %s missing from %s", want, shares)
		}
	}
	for _, sql := range []string{
		`SELECT id::text, project_id::text, name FROM app.hosts`,
		`SELECT id::text, host_id::text, project_id::text, name FROM app.services`,
		`SELECT id::text, project_id::text FROM app.join_tokens`,
	} {
		assertNonNullScan(t, d, ctx, sql)
	}
	if n := count(t, d, ctx, `SELECT count(*) FROM app.schema_migrations WHERE name = '0015_orgs.sql'`); n != 0 {
		t.Fatal("down script left its schema_migrations row")
	}
}

func count(t *testing.T, d *DB, ctx context.Context, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := d.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func svcProject(t *testing.T, d *DB, ctx context.Context, id uuid.UUID) *string {
	t.Helper()
	var p *string
	if err := d.pool.QueryRow(ctx, `SELECT project_id::text FROM app.services WHERE id = $1::uuid`, id.String()).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func orgID(t *testing.T, d *DB, ctx context.Context, slug string) uuid.UUID {
	t.Helper()
	var id string
	if err := d.pool.QueryRow(ctx, `SELECT id::text FROM app.orgs WHERE slug = $1`, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return uuid.MustParse(id)
}

// tryDown runs the down script and returns its error.
func tryDown(ctx context.Context, url string) error {
	body, err := migrations.FS.ReadFile("0015_orgs.down.sql")
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `SET ROLE deploybot_owner`); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, fillHostMirrorSQL); err != nil {
		return err
	}
	_, err = conn.PgConn().Exec(ctx, string(body)).ReadAll()
	return err
}
