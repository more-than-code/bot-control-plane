package db

import (
	"context"
	"encoding/json"
	"errors"
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

const edgeMigration = "0023_domains_edge_routes.sql"

// domainNamesMigration changes a 0023 constraint, so it is applied again
// whenever a test rolls 0023 back and forward.
const domainNamesMigration = "0036_domain_assignment_names.sql"

// TestParityDomains runs the domain store rules against memstore and,
// with a test database, Postgres.
func TestParityDomains(t *testing.T) {
	runParity(t, func(p parity) {
		owner := p.user("dom")
		org := p.org(owner.ID)
		home := p.project(org, owner.ID, "dhome")
		rival := p.user("rival")
		rivalOrg := p.org(rival.ID)
		theirs := p.project(rivalOrg, rival.ID, "dtheirs")
		name := "ex" + p.tag + ".com"
		token := strings.Repeat("t", 43)

		d, err := p.st.CreateOrgDomain(p.ctx, org, name, token, owner.ID, p.at)
		if err != nil || d.Status != store.DomainPending || d.Name != name {
			p.t.Fatalf("create %#v %v", d, err)
		}
		if _, err := p.st.CreateOrgDomain(p.ctx, org, name, token, owner.ID, p.at); !errors.Is(err, store.ErrConflict) {
			p.t.Fatalf("same name in org %v", err)
		}
		// Both orgs may add a pending name; only one can verify it.
		rd, err := p.st.CreateOrgDomain(p.ctx, rivalOrg, name, token, rival.ID, p.at)
		if err != nil {
			p.t.Fatal(err)
		}
		if d, err = p.st.MarkDomainVerified(p.ctx, org, d.ID, p.at); err != nil || d.Status != store.DomainVerified || d.VerifiedAt == nil {
			p.t.Fatalf("verify %#v %v", d, err)
		}
		if _, err := p.st.MarkDomainVerified(p.ctx, rivalOrg, rd.ID, p.at); !errors.Is(err, store.ErrDomainTaken) {
			p.t.Fatalf("rival verify %v", err)
		}
		if _, err := p.st.CreateOrgDomain(p.ctx, rivalOrg, "x."+name, token, rival.ID, p.at); err != nil {
			p.t.Fatalf("a subdomain is another name: %v", err)
		}
		if _, err := p.st.GetOrgDomain(p.ctx, rivalOrg, d.ID); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("cross-org read %v", err)
		}

		// Assignments: projects of the org only.
		if _, err := p.st.PutDomainAssignments(p.ctx, org, d.ID, []store.DomainAssignment{{ProjectID: theirs.ID, Pattern: "@"}}); !errors.Is(err, store.ErrInvalid) {
			p.t.Fatalf("foreign project %v", err)
		}
		d, err = p.st.PutDomainAssignments(p.ctx, org, d.ID, []store.DomainAssignment{{ProjectID: home.ID, Pattern: "*"}, {ProjectID: home.ID, Pattern: "@"}})
		if err != nil || len(d.Assignments) != 2 || d.Assignments[0].Pattern != "*" || d.Assignments[0].ProjectName != "dhome" {
			p.t.Fatalf("assign %#v %v", d.Assignments, err)
		}
		// api-v1 v1.52: a single name and a narrower wildcard are patterns
		// too; the same pair twice, or a bad shape, is ErrInvalid.
		narrow, err := p.st.PutDomainAssignments(p.ctx, org, d.ID, []store.DomainAssignment{
			{ProjectID: home.ID, Pattern: "app"}, {ProjectID: home.ID, Pattern: "*.dev"},
		})
		if err != nil || len(narrow.Assignments) != 2 {
			p.t.Fatalf("narrow assign %#v %v", narrow.Assignments, err)
		}
		if _, err := p.st.PutDomainAssignments(p.ctx, org, d.ID, []store.DomainAssignment{{ProjectID: home.ID, Pattern: "app"}, {ProjectID: home.ID, Pattern: "app"}}); !errors.Is(err, store.ErrInvalid) {
			p.t.Fatalf("the same pair twice %v", err)
		}
		for _, bad := range []string{"a..b", "**", "-app", strings.Repeat("a", 250)} {
			if _, err := p.st.PutDomainAssignments(p.ctx, org, d.ID, []store.DomainAssignment{{ProjectID: home.ID, Pattern: bad}}); !errors.Is(err, store.ErrInvalid) {
				p.t.Fatalf("pattern %q %v", bad, err)
			}
		}
		if d, err = p.st.PutDomainAssignments(p.ctx, org, d.ID, []store.DomainAssignment{{ProjectID: home.ID, Pattern: "*"}, {ProjectID: home.ID, Pattern: "@"}}); err != nil {
			p.t.Fatal(err)
		}
		mine, err := p.st.ProjectDomains(p.ctx, home.ID)
		if err != nil || len(mine) != 1 || mine[0].ID != d.ID {
			p.t.Fatalf("project domains %#v %v", mine, err)
		}
		if none, _ := p.st.ProjectDomains(p.ctx, theirs.ID); len(none) != 0 {
			p.t.Fatalf("rival project domains %#v", none)
		}

		// Daily checks: six failures keep it verified, the seventh lapses it,
		// a success restores it.
		for i := 1; i <= store.DomainLapseAfter; i++ {
			got, lapsed, err := p.st.RecordDomainCheck(p.ctx, d.ID, false, p.at.Add(time.Duration(i)*time.Hour))
			if err != nil || got.Failures != i || lapsed != (i == store.DomainLapseAfter) {
				p.t.Fatalf("check %d %#v %v %v", i, got, lapsed, err)
			}
		}
		if got, _ := p.st.GetOrgDomain(p.ctx, org, d.ID); got.Status != store.DomainLapsed {
			p.t.Fatalf("status %s", got.Status)
		}
		if _, err := p.st.MarkDomainVerified(p.ctx, rivalOrg, rd.ID, p.at); !errors.Is(err, store.ErrDomainTaken) {
			p.t.Fatalf("a lapsed name stays held: %v", err)
		}
		got, lapsed, err := p.st.RecordDomainCheck(p.ctx, d.ID, true, p.at.Add(10*time.Hour))
		if err != nil || lapsed || got.Status != store.DomainVerified || got.Failures != 0 {
			p.t.Fatalf("recovered %#v %v", got, err)
		}
		if _, _, err := p.st.RecordDomainCheck(p.ctx, rd.ID, true, p.at); !errors.Is(err, store.ErrState) {
			p.t.Fatalf("pending check %v", err)
		}
		due, err := p.st.ListDomainsDue(p.ctx, p.at.Add(11*time.Hour), 0)
		if err != nil || !containsDomain(due, d.ID) || containsDomain(due, rd.ID) {
			p.t.Fatalf("due %#v %v", due, err)
		}
		if due, _ = p.st.ListDomainsDue(p.ctx, p.at.Add(9*time.Hour), 0); containsDomain(due, d.ID) {
			p.t.Fatal("checked domain is due")
		}

		// ACME email.
		if err := p.st.SetOrgACMEEmail(p.ctx, org, "ops@example.com"); err != nil {
			p.t.Fatal(err)
		}
		if e, _ := p.st.OrgACMEEmail(p.ctx, org); e != "ops@example.com" {
			p.t.Fatalf("email %q", e)
		}
		if err := p.st.SetOrgACMEEmail(p.ctx, org, ""); err != nil {
			p.t.Fatal(err)
		}
		if e, _ := p.st.OrgACMEEmail(p.ctx, org); e != "" {
			p.t.Fatalf("cleared email %q", e)
		}
	})
}

func containsDomain(list []store.OrgDomain, id uuid.UUID) bool {
	for _, d := range list {
		if d.ID == id {
			return true
		}
	}
	return false
}

func putPlan(p parity, c store.EdgeConfirmation) []byte {
	p.t.Helper()
	_, hash, _ := secret.NewToken()
	c.Body = json.RawMessage(`{}`)
	c.RequestHash = make([]byte, 32)
	c.IdempotencyKey = "k-" + p.tag
	c.CreatedAt, c.ExpiresAt = p.at, p.at.Add(time.Minute)
	if err := p.st.PutEdgeConfirmation(p.ctx, hash, c); err != nil {
		p.t.Fatal(err)
	}
	return hash
}

// TestParityRoutesAndEdges covers routes, plans, live entries and edges.
func TestParityRoutesAndEdges(t *testing.T) {
	runParity(t, func(p parity) {
		owner := p.user("rt")
		org := p.org(owner.ID)
		home := p.project(org, owner.ID, "rhome")
		host := p.enroll(org, &store.TokenGrant{ProjectID: home.ID, Level: "deploy"}, owner.ID, "r")
		spec, hash, display := catalogSpec(p.t, "pnpm run dev")
		web, err := p.st.CreateCatalogEntry(p.ctx, home.ID, store.CatalogWrite{Name: "rweb", Spec: spec, SpecHash: hash, CommandDisplay: display, Actor: owner.ID, At: p.at})
		if err != nil {
			p.t.Fatal(err)
		}
		api, err := p.st.CreateCatalogEntry(p.ctx, home.ID, store.CatalogWrite{Name: "rapi", Spec: spec, SpecHash: hash, CommandDisplay: display, Actor: owner.ID, At: p.at})
		if err != nil {
			p.t.Fatal(err)
		}
		hn := "app.ex" + p.tag + ".com"
		cf := "cloudflare"
		routes := []store.Route{{Hostname: hn, PathPrefix: "/", Port: 3000, ClientIPFrom: &cf, NoStorePaths: []string{"/evidence/"}}, {Hostname: hn, PathPrefix: "/api", Port: 3000}}
		plan := putPlan(p, store.EdgeConfirmation{UserID: owner.ID, Kind: store.EdgePlanRoutes, ProjectID: &home.ID, EntryID: &web.ID})
		// Another user, or another entry, cannot use the plan.
		if err := p.st.ReplaceEntryRoutes(p.ctx, home.ID, web.ID, routes, plan, uuid.New(), p.at); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("foreign actor %v", err)
		}
		if err := p.st.ReplaceEntryRoutes(p.ctx, home.ID, api.ID, routes, plan, owner.ID, p.at); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("other entry %v", err)
		}
		if err := p.st.ReplaceEntryRoutes(p.ctx, home.ID, web.ID, routes, plan, owner.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		if err := p.st.ReplaceEntryRoutes(p.ctx, home.ID, web.ID, routes, plan, owner.ID, p.at); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("used plan %v", err)
		}
		if c, err := p.st.GetEdgeConfirmation(p.ctx, plan); err != nil || c.UsedAt == nil {
			p.t.Fatalf("plan not marked %v", err)
		}
		got, err := p.st.ListProjectRoutes(p.ctx, home.ID)
		if err != nil || len(got) != 2 || got[0].Entry != "rweb" || got[0].PathPrefix != "/" || got[0].ClientIPFrom == nil || len(got[0].NoStorePaths) != 1 || len(got[1].NoStorePaths) != 0 {
			p.t.Fatalf("routes %#v %v", got, err)
		}
		// The same hostname and path for another entry is a conflict and
		// leaves its plan unused.
		plan2 := putPlan(p, store.EdgeConfirmation{UserID: owner.ID, Kind: store.EdgePlanRoutes, ProjectID: &home.ID, EntryID: &api.ID})
		if err := p.st.ReplaceEntryRoutes(p.ctx, home.ID, api.ID, routes[1:], plan2, owner.ID, p.at); !errors.Is(err, store.ErrConflict) {
			p.t.Fatalf("taken %v", err)
		}
		if c, _ := p.st.GetEdgeConfirmation(p.ctx, plan2); c.UsedAt != nil {
			p.t.Fatal("conflict marked the plan")
		}
		if byHost, _ := p.st.ListRoutesByHostname(p.ctx, []string{hn}); len(byHost) != 2 {
			p.t.Fatalf("by hostname %#v", byHost)
		}

		// A live owned program on the host makes the entry deployed there.
		if err := p.st.SetHostPrograms(p.ctx, host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, Projects: []string{home.Slug}}); err != nil {
			p.t.Fatal(err)
		}
		if err := p.st.UpsertDiscovered(p.ctx, host.ID, []store.Discovered{{Name: "x", Kind: "process", Ports: []int{3000}, Owner: &store.DiscoveredOwner{Kind: "program", Project: home.Slug, Entry: "rweb"}}}, p.at, true, true); err != nil {
			p.t.Fatal(err)
		}
		hosts, err := p.st.EntryHosts(p.ctx, home.ID)
		if err != nil || len(hosts["rweb"]) != 1 || hosts["rweb"][0] != host.ID || len(hosts["rapi"]) != 0 {
			p.t.Fatalf("entry hosts %#v %v", hosts, err)
		}
		entries, err := p.st.HostEntries(p.ctx, host.ID)
		if err != nil || len(entries) != 1 || entries[0].Entry != "rweb" || entries[0].ProjectID != home.ID || len(entries[0].Ports) != 1 || entries[0].Ports[0] != 3000 {
			p.t.Fatalf("host entries %#v %v", entries, err)
		}

		// Edges: no row reads off; set, read back, list.
		e, err := p.st.GetHostEdge(p.ctx, host.ID)
		if err != nil || e.Enabled || e.Status != store.EdgeOff {
			p.t.Fatalf("no edge %#v %v", e, err)
		}
		if edges, _ := p.st.ListOrgEdges(p.ctx, org); len(edges) != 0 {
			p.t.Fatalf("edges %#v", edges)
		}
		// A state write alone creates a disabled row and never sets enabled.
		applied := p.at
		want := store.HostEdge{HostID: host.ID, Enabled: true, Status: store.EdgeApplied, ConfigHash: strings.Repeat("a", 64), AppliedAt: &applied, UpdatedAt: p.at}
		if err := p.st.SetHostEdgeState(p.ctx, want); err != nil {
			p.t.Fatal(err)
		}
		if e, _ = p.st.GetHostEdge(p.ctx, host.ID); e.Enabled || e.Status != store.EdgeApplied {
			p.t.Fatalf("state set enabled %#v", e)
		}
		if e, err = p.st.SetHostEdgeEnabled(p.ctx, host.ID, true, p.at); err != nil || !e.Enabled || e.Status != store.EdgePending || e.ConfigHash != want.ConfigHash {
			p.t.Fatalf("enable %#v %v", e, err)
		}
		if err := p.st.SetHostEdgeState(p.ctx, want); err != nil {
			p.t.Fatal(err)
		}
		if e, _ = p.st.GetHostEdge(p.ctx, host.ID); !e.Enabled || e.Status != store.EdgeApplied || e.ConfigHash != want.ConfigHash || e.AppliedAt == nil {
			p.t.Fatalf("edge %#v", e)
		}
		// A disable during an apply survives the apply's state write.
		if e, _ = p.st.SetHostEdgeEnabled(p.ctx, host.ID, false, p.at); e.Enabled || e.Status != store.EdgePending {
			p.t.Fatalf("disable %#v", e)
		}
		if err := p.st.SetHostEdgeState(p.ctx, want); err != nil {
			p.t.Fatal(err)
		}
		if e, _ = p.st.GetHostEdge(p.ctx, host.ID); e.Enabled {
			p.t.Fatal("the state write re-enabled the edge")
		}
		if _, err := p.st.SetHostEdgeEnabled(p.ctx, host.ID, true, p.at); err != nil {
			p.t.Fatal(err)
		}
		if edges, _ := p.st.ListOrgEdges(p.ctx, org); len(edges) != 1 {
			p.t.Fatalf("edges %#v", edges)
		}
		if _, err := p.st.GetHostEdge(p.ctx, uuid.New()); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("unknown host %v", err)
		}

		// A domain delete removes the routes under it and keeps the used plan.
		name := "ex" + p.tag + ".com"
		d, err := p.st.CreateOrgDomain(p.ctx, org, name, strings.Repeat("t", 43), owner.ID, p.at)
		if err != nil {
			p.t.Fatal(err)
		}
		under, _ := p.st.RoutesUnderDomain(p.ctx, org, d.ID)
		if len(under) != 2 || under[0].ID == uuid.Nil {
			p.t.Fatalf("under %#v", under)
		}
		del := putPlan(p, store.EdgeConfirmation{UserID: owner.ID, Kind: store.EdgePlanDomainDelete, OrgID: &org, DomainID: &d.ID})
		// A plan whose routes differ is refused and stays unused.
		if _, err := p.st.DeleteOrgDomain(p.ctx, org, d.ID, store.RouteKeys(under[:1]), del, owner.ID, p.at); !errors.Is(err, store.ErrRoutesChanged) {
			p.t.Fatalf("changed routes %v", err)
		}
		removed, err := p.st.DeleteOrgDomain(p.ctx, org, d.ID, store.RouteKeys(under), del, owner.ID, p.at)
		if err != nil || len(removed) != 2 {
			p.t.Fatalf("delete %#v %v", removed, err)
		}
		if left, _ := p.st.ListProjectRoutes(p.ctx, home.ID); len(left) != 0 {
			p.t.Fatalf("left %#v", left)
		}
		if c, err := p.st.GetEdgeConfirmation(p.ctx, del); err != nil || c.UsedAt == nil {
			p.t.Fatalf("delete plan %#v %v", c, err)
		}

		// Deleting an entry drops its routes.
		plan3 := putPlan(p, store.EdgeConfirmation{UserID: owner.ID, Kind: store.EdgePlanRoutes, ProjectID: &home.ID, EntryID: &api.ID})
		if err := p.st.ReplaceEntryRoutes(p.ctx, home.ID, api.ID, routes[1:], plan3, owner.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.DeleteCatalogEntry(p.ctx, home.ID, "rapi"); err != nil {
			p.t.Fatal(err)
		}
		if left, _ := p.st.ListRoutesByHostname(p.ctx, []string{hn}); len(left) != 0 {
			p.t.Fatalf("entry routes left %#v", left)
		}
	})
}

func TestDomainsEdgeRoutesMigrationText(t *testing.T) {
	if !numberedSQL(edgeMigration) || numberedSQL("0023_domains_edge_routes.down.sql") {
		t.Fatal("0023 numbering")
	}
	body, err := migrations.FS.ReadFile(edgeMigration)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"CREATE TABLE app.org_domains", "CREATE TABLE app.domain_assignments", "CREATE TABLE app.routes",
		"CREATE TABLE app.host_edges", "CREATE TABLE app.edge_confirmations",
		"CONSTRAINT routes_hostname_path_unique UNIQUE (hostname, path_prefix)",
		"WHERE status IN ('verified', 'lapsed')",
		"REFERENCES app.catalog_entries (id) ON DELETE CASCADE",
		"ALTER TABLE app.orgs ADD COLUMN acme_email text NULL",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("0023 missing %s", want)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if trim := strings.TrimSpace(line); strings.HasPrefix(trim, "--") && strings.ContainsAny(trim, ";'") {
			t.Fatalf("comment contains a semicolon or an apostrophe: %s", trim)
		}
	}
	down, err := migrations.FS.ReadFile("0023_domains_edge_routes.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	stmts := splitSQL(string(down))
	if len(stmts) < 3 || !strings.Contains(stmts[1], "routes_none_chk") || !strings.Contains(stmts[2], "org_domains_none_chk") ||
		!strings.Contains(string(down), "DELETE FROM app.schema_migrations WHERE name = '"+edgeMigration+"'") ||
		!strings.HasSuffix(strings.TrimSpace(string(down)), "COMMIT;") {
		t.Fatalf("down script %v", stmts)
	}
}

func edgeTables(t *testing.T, d *DB, ctx context.Context) int {
	t.Helper()
	var n int
	if err := d.pool.QueryRow(ctx, `
SELECT count(*) FROM information_schema.tables
WHERE table_schema = 'app' AND table_name IN ('org_domains', 'domain_assignments', 'routes', 'host_edges', 'edge_confirmations')
`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestDomainsEdgeRoutesMigration is up, a refused down (a domain exists),
// down, and up again, with the deploybot_api grants.
func TestDomainsEdgeRoutesMigration(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
	lockMigrations(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() { restoreEdgeMigration(url) })
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
	if edgeTables(t, d, ctx) != 5 {
		t.Fatal("0023 tables missing")
	}
	assertTablePrivs(t, d, ctx, "org_domains", true, true, false, true, []string{"failures", "last_check_at", "status", "verified_at"})
	assertTablePrivs(t, d, ctx, "routes", true, true, false, true, nil)
	assertTablePrivs(t, d, ctx, "domain_assignments", true, true, false, true, nil)
	assertTablePrivs(t, d, ctx, "host_edges", true, true, false, false, []string{"applied_at", "config_hash", "detail", "enabled", "status", "updated_at"})
	assertTablePrivs(t, d, ctx, "edge_confirmations", true, true, false, true, []string{"used_at"})

	// Clear rows other tests left, then hold one domain so down refuses.
	if _, err := d.pool.Exec(ctx, `DELETE FROM app.routes`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `DELETE FROM app.org_domains`); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	tag := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	owner := catalogUser(t, d, ctx, "edge-mig-"+tag, at)
	org := personalOrg(t, d, ctx, owner.ID)
	dom, err := d.CreateOrgDomain(ctx, org, "mig"+tag+".com", strings.Repeat("t", 43), owner.ID, at)
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
	down, err := migrations.FS.ReadFile("0023_domains_edge_routes.down.sql")
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
		t.Fatal("down ran while a domain exists")
	}
	if edgeTables(t, d, ctx) != 5 {
		t.Fatal("refused down changed the schema")
	}
	if _, err := d.pool.Exec(ctx, `DELETE FROM app.org_domains WHERE id = $1::uuid`, dom.ID.String()); err != nil {
		t.Fatal(err)
	}
	if err := runDown(); err != nil {
		t.Fatalf("down: %v", err)
	}
	if edgeTables(t, d, ctx) != 0 {
		t.Fatal("down left tables")
	}
	if _, err := conn.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name IN ('`+edgeMigration+`', '`+domainNamesMigration+`')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, url); err != nil {
		t.Fatalf("restore migrate: %v", err)
	}
	if edgeTables(t, d, ctx) != 5 {
		t.Fatal("up again")
	}
}

// restoreEdgeMigration re-applies 0023 when a failed run left it rolled back.
func restoreEdgeMigration(url string) {
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
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema = 'app' AND table_name = 'routes'`).Scan(&n); err != nil || n == 1 {
		return
	}
	if _, err := d.pool.Exec(ctx, `DELETE FROM app.schema_migrations WHERE name IN ('`+edgeMigration+`', '`+domainNamesMigration+`')`); err != nil {
		return
	}
	_ = Migrate(ctx, url)
}

// TestDomainsEdgeRoutesAsAPIRole runs the v1.35 writes as deploybot_api.
func TestDomainsEdgeRoutesAsAPIRole(t *testing.T) {
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
	p := parity{t: t, st: api, ctx: ctx, at: time.Now().UTC().Truncate(time.Microsecond), tag: strings.ReplaceAll(uuid.NewString(), "-", "")[:8]}
	owner := p.user("eapi")
	org := p.org(owner.ID)
	home := p.project(org, owner.ID, "eapi")
	host := p.enroll(org, &store.TokenGrant{ProjectID: home.ID, Level: "deploy"}, owner.ID, "e")
	spec, hash, display := catalogSpec(t, "pnpm run dev")
	web, err := api.CreateCatalogEntry(ctx, home.ID, store.CatalogWrite{Name: "eweb", Spec: spec, SpecHash: hash, CommandDisplay: display, Actor: owner.ID, At: p.at})
	if err != nil {
		t.Fatal(err)
	}
	name := "api" + p.tag + ".com"
	dom, err := api.CreateOrgDomain(ctx, org, name, strings.Repeat("t", 43), owner.ID, p.at)
	if err != nil {
		t.Fatalf("domain as deploybot_api: %v", err)
	}
	if _, err := api.MarkDomainVerified(ctx, org, dom.ID, p.at); err != nil {
		t.Fatalf("verify as deploybot_api: %v", err)
	}
	if _, _, err := api.RecordDomainCheck(ctx, dom.ID, false, p.at); err != nil {
		t.Fatalf("check as deploybot_api: %v", err)
	}
	if _, err := api.PutDomainAssignments(ctx, org, dom.ID, []store.DomainAssignment{{ProjectID: home.ID, Pattern: "*"}}); err != nil {
		t.Fatalf("assign as deploybot_api: %v", err)
	}
	plan := putPlan(p, store.EdgeConfirmation{UserID: owner.ID, Kind: store.EdgePlanRoutes, ProjectID: &home.ID, EntryID: &web.ID})
	if err := api.ReplaceEntryRoutes(ctx, home.ID, web.ID, []store.Route{{Hostname: "app." + name, PathPrefix: "/", Port: 3000}}, plan, owner.ID, p.at); err != nil {
		t.Fatalf("routes as deploybot_api: %v", err)
	}
	// Every read the routes, domains, edge sync and re-check job use.
	reads := map[string]func() error{
		"ListProjectRoutes":    func() error { _, err := api.ListProjectRoutes(ctx, home.ID); return err },
		"ListRoutesByHostname": func() error { _, err := api.ListRoutesByHostname(ctx, []string{"app." + name}); return err },
		"ProjectDomains":       func() error { _, err := api.ProjectDomains(ctx, home.ID); return err },
		"ListOrgDomains":       func() error { _, err := api.ListOrgDomains(ctx, org); return err },
		"ListDomainsDue":       func() error { _, err := api.ListDomainsDue(ctx, p.at.Add(48*time.Hour), 10); return err },
		"RoutesUnderDomain":    func() error { _, err := api.RoutesUnderDomain(ctx, org, dom.ID); return err },
		"GetEdgeConfirmation":  func() error { _, err := api.GetEdgeConfirmation(ctx, plan); return err },
		"EntryHosts":           func() error { _, err := api.EntryHosts(ctx, home.ID); return err },
		"HostEntries":          func() error { _, err := api.HostEntries(ctx, host.ID); return err },
		"GetHostEdge":          func() error { _, err := api.GetHostEdge(ctx, host.ID); return err },
		"ListOrgEdges":         func() error { _, err := api.ListOrgEdges(ctx, org); return err },
		"OrgACMEEmail":         func() error { _, err := api.OrgACMEEmail(ctx, org); return err },
	}
	for what, read := range reads {
		if err := read(); err != nil {
			t.Fatalf("%s as deploybot_api: %v", what, err)
		}
	}
	// The re-check job: a success after the failure above.
	if _, _, err := api.RecordDomainCheck(ctx, dom.ID, true, p.at.Add(time.Hour)); err != nil {
		t.Fatalf("check ok as deploybot_api: %v", err)
	}
	// The edge switch (insert, then update) and the sync state writes.
	if _, err := api.SetHostEdgeEnabled(ctx, host.ID, true, p.at); err != nil {
		t.Fatalf("edge insert as deploybot_api: %v", err)
	}
	if err := api.SetHostEdgeState(ctx, store.HostEdge{HostID: host.ID, Status: store.EdgeApplied, ConfigHash: strings.Repeat("b", 64), UpdatedAt: p.at}); err != nil {
		t.Fatalf("edge update as deploybot_api: %v", err)
	}
	if _, err := api.SetHostEdgeEnabled(ctx, host.ID, false, p.at); err != nil {
		t.Fatalf("edge disable as deploybot_api: %v", err)
	}
	if err := api.SetHostEdgeState(ctx, store.HostEdge{HostID: host.ID, Status: store.EdgeOff, UpdatedAt: p.at}); err != nil {
		t.Fatalf("edge stop state as deploybot_api: %v", err)
	}
	if err := api.SetOrgACMEEmail(ctx, org, "ops@example.com"); err != nil {
		t.Fatalf("acme email as deploybot_api: %v", err)
	}
	del := putPlan(p, store.EdgeConfirmation{UserID: owner.ID, Kind: store.EdgePlanDomainDelete, OrgID: &org, DomainID: &dom.ID})
	if _, err := api.DeleteOrgDomain(ctx, org, dom.ID, []string{"app." + name + "/"}, del, owner.ID, p.at); err != nil {
		t.Fatalf("domain delete as deploybot_api: %v", err)
	}
	if _, err := api.DeleteCatalogEntry(ctx, home.ID, "eweb"); err != nil {
		t.Fatalf("entry delete as deploybot_api: %v", err)
	}

	// Project delete drops its routes and assignments; org delete drops
	// its domains.
	other, err := api.CreateOrg(ctx, owner.ID, "Edge API", "edge-api-"+p.tag, p.at)
	if err != nil {
		t.Fatal(err)
	}
	gone := p.project(other.ID, owner.ID, "egone")
	gweb, err := api.CreateCatalogEntry(ctx, gone.ID, store.CatalogWrite{Name: "gweb", Spec: spec, SpecHash: hash, CommandDisplay: display, Actor: owner.ID, At: p.at})
	if err != nil {
		t.Fatal(err)
	}
	gname := "gone" + p.tag + ".com"
	gdom, err := api.CreateOrgDomain(ctx, other.ID, gname, strings.Repeat("t", 43), owner.ID, p.at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.MarkDomainVerified(ctx, other.ID, gdom.ID, p.at); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PutDomainAssignments(ctx, other.ID, gdom.ID, []store.DomainAssignment{{ProjectID: gone.ID, Pattern: "@"}}); err != nil {
		t.Fatal(err)
	}
	gplan := putPlan(p, store.EdgeConfirmation{UserID: owner.ID, Kind: store.EdgePlanRoutes, ProjectID: &gone.ID, EntryID: &gweb.ID})
	if err := api.ReplaceEntryRoutes(ctx, gone.ID, gweb.ID, []store.Route{{Hostname: gname, PathPrefix: "/", Port: 3000}}, gplan, owner.ID, p.at); err != nil {
		t.Fatal(err)
	}
	if err := api.DeleteProject(ctx, gone.ID, p.at); err != nil {
		t.Fatalf("project delete as deploybot_api: %v", err)
	}
	var left int
	if err := d.pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM app.routes WHERE project_id = $1::uuid)
     + (SELECT count(*) FROM app.domain_assignments WHERE project_id = $1::uuid)`, gone.ID.String()).Scan(&left); err != nil || left != 0 {
		t.Fatalf("routes or assignments left %d %v", left, err)
	}
	if _, err := api.DeleteOrg(ctx, other.ID, p.at); err != nil {
		t.Fatalf("org delete as deploybot_api: %v", err)
	}
}

// TestParityDomainScopeAndProjectDelete: a domain delete touches only its
// org's routes and skips hostnames another domain still covers (review 3),
// and a deleted project frees its hostnames (review 5).
func TestParityDomainScopeAndProjectDelete(t *testing.T) {
	runParity(t, func(p parity) {
		a := p.user("sa")
		orgA := p.org(a.ID)
		projA := p.project(orgA, a.ID, "sa")
		b := p.user("sb")
		orgB := p.org(b.ID)
		projB := p.project(orgB, b.ID, "sb")
		spec, hash, display := catalogSpec(p.t, "pnpm run dev")
		entry := func(project store.Project, owner uuid.UUID, name string) store.CatalogEntry {
			e, err := p.st.CreateCatalogEntry(p.ctx, project.ID, store.CatalogWrite{Name: name, Spec: spec, SpecHash: hash, CommandDisplay: display, Actor: owner, At: p.at})
			if err != nil {
				p.t.Fatal(err)
			}
			return e
		}
		route := func(owner uuid.UUID, project store.Project, e store.CatalogEntry, hostnames ...string) error {
			plan := putPlan(p, store.EdgeConfirmation{UserID: owner, Kind: store.EdgePlanRoutes, ProjectID: &project.ID, EntryID: &e.ID})
			var routes []store.Route
			for _, h := range hostnames {
				routes = append(routes, store.Route{Hostname: h, PathPrefix: "/", Port: 3000})
			}
			return p.st.ReplaceEntryRoutes(p.ctx, project.ID, e.ID, routes, plan, owner, p.at)
		}
		base := "ex" + p.tag + ".com"
		sub := "sub." + base
		domA, err := p.st.CreateOrgDomain(p.ctx, orgA, base, strings.Repeat("t", 43), a.ID, p.at)
		if err != nil {
			p.t.Fatal(err)
		}
		// Org A also holds www.<base>, which covers its www route.
		www, err := p.st.CreateOrgDomain(p.ctx, orgA, "www."+base, strings.Repeat("t", 43), a.ID, p.at)
		if err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.MarkDomainVerified(p.ctx, orgA, www.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.PutDomainAssignments(p.ctx, orgA, www.ID, []store.DomainAssignment{{ProjectID: projA.ID, Pattern: "@"}}); err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.CreateOrgDomain(p.ctx, orgB, sub, strings.Repeat("t", 43), b.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		webA := entry(projA, a.ID, "sweb")
		webB := entry(projB, b.ID, "sweb")
		if err := route(a.ID, projA, webA, "app."+base, "www."+base); err != nil {
			p.t.Fatal(err)
		}
		if err := route(b.ID, projB, webB, sub); err != nil {
			p.t.Fatal(err)
		}
		under, err := p.st.RoutesUnderDomain(p.ctx, orgA, domA.ID)
		if err != nil || len(under) != 1 || under[0].Hostname != "app."+base {
			p.t.Fatalf("under %#v %v", under, err)
		}
		// A route a narrower wildcard of another domain covers is left alone
		// as an exact or `*` assignment is (api-v1 v1.52).
		if _, err := p.st.PutDomainAssignments(p.ctx, orgA, www.ID, []store.DomainAssignment{{ProjectID: projA.ID, Pattern: "@"}, {ProjectID: projA.ID, Pattern: "deep"}}); err != nil {
			p.t.Fatal(err)
		}
		if err := route(a.ID, projA, webA, "app."+base, "www."+base, "deep.www."+base); err != nil {
			p.t.Fatal(err)
		}
		if under, err = p.st.RoutesUnderDomain(p.ctx, orgA, domA.ID); err != nil || len(under) != 1 || under[0].Hostname != "app."+base {
			p.t.Fatalf("under with a deep name %#v %v", under, err)
		}
		// A deep route held by a more specific domain of the org was never
		// the parent's, even once that domain's assignment is gone: the
		// parent's delete leaves it alone.
		if _, err := p.st.PutDomainAssignments(p.ctx, orgA, www.ID, []store.DomainAssignment{{ProjectID: projA.ID, Pattern: "@"}}); err != nil {
			p.t.Fatal(err)
		}
		if under, err = p.st.RoutesUnderDomain(p.ctx, orgA, domA.ID); err != nil || len(under) != 1 || under[0].Hostname != "app."+base {
			p.t.Fatalf("under after unassigning the deep name %#v %v", under, err)
		}
		del := putPlan(p, store.EdgeConfirmation{UserID: a.ID, Kind: store.EdgePlanDomainDelete, OrgID: &orgA, DomainID: &domA.ID})
		if _, err := p.st.DeleteOrgDomain(p.ctx, orgA, domA.ID, store.RouteKeys(under), del, a.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		if left, _ := p.st.ListRoutesByHostname(p.ctx, []string{sub, "www." + base}); len(left) != 2 {
			p.t.Fatalf("other routes touched %#v", left)
		}

		// Deleting B's project frees sub.<base> for another project of B.
		if err := p.st.DeleteProject(p.ctx, projB.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		projB2 := p.project(orgB, b.ID, "sb2")
		webB2 := entry(projB2, b.ID, "sweb")
		if err := route(b.ID, projB2, webB2, sub); err != nil {
			p.t.Fatalf("hostname not freed: %v", err)
		}
	})
}
