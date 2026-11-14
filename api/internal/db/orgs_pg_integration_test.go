package db

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// TestOrgsPostgres is the Postgres coverage that the shared-hosts tests had
// before v1.24 (review D22).
func TestOrgsPostgres(t *testing.T) {
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip()
	}
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
	t.Run("privileges", func(t *testing.T) { assertOrgPrivileges(t, d, ctx) })
	t.Run("facts and filters", func(t *testing.T) { assertFactsChain(t, d, ctx) })
	t.Run("host cursors", func(t *testing.T) { assertHostCursors(t, d, ctx) })
	t.Run("processes", func(t *testing.T) { assertProcessesUnfiltered(t, d, ctx) })
	t.Run("project delete", func(t *testing.T) { assertProjectDeleteOrg(t, d, ctx) })
	t.Run("org delete", func(t *testing.T) { assertOrgDelete(t, d, ctx) })
	t.Run("host delete cascades access", func(t *testing.T) { assertHostAccessCascade(t, d, ctx) })
}

type pgFix struct {
	t   *testing.T
	d   *DB
	ctx context.Context
	at  time.Time
	tag string
}

func newPgFix(t *testing.T, d *DB, ctx context.Context) pgFix {
	return pgFix{t: t, d: d, ctx: ctx, at: time.Now().UTC().Truncate(time.Microsecond), tag: strings.ReplaceAll(uuid.NewString(), "-", "")[:8]}
}

func (f pgFix) user(name string) store.User { return dbUser(f.t, f.d, f.ctx, name+"-"+f.tag, f.at) }

func (f pgFix) project(org, owner uuid.UUID, slug string) store.Project {
	return projectIn(f.t, f.d, f.ctx, org, owner, slug, slug+"-"+f.tag, f.at)
}

func (f pgFix) host(org uuid.UUID, grant *store.TokenGrant, by uuid.UUID, name string) store.Host {
	return enrollOrgHost(f.t, f.d, f.ctx, org, grant, by, name+"-"+f.tag, f.at)
}

func (f pgFix) row(hostID uuid.UUID, project *uuid.UUID, name string) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	if _, err := f.d.pool.Exec(f.ctx, `
INSERT INTO app.services (id, host_id, name, source, kind, ports, first_seen_at, last_seen_at, project_id)
VALUES ($1::uuid, $2::uuid, $3, 'declared', 'process', '{}', $4, $4, CAST($5 AS uuid))
`, id.String(), hostID.String(), name, f.at, optID(project)); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func assertOrgPrivileges(t *testing.T, d *DB, ctx context.Context) {
	assertTablePrivs(t, d, ctx, "orgs", true, true, true, true, []string{"id", "name", "slug", "created_by", "created_at", "deleted_at"})
	assertTablePrivs(t, d, ctx, "org_members", true, true, true, true, []string{"org_id", "user_id", "role", "added_by", "added_at"})
	assertTablePrivs(t, d, ctx, "host_access", true, true, true, true, []string{"host_id", "project_id", "level", "granted_by", "granted_at"})
	for _, col := range []string{"projects.org_id", "hosts.org_id", "join_tokens.org_id", "join_tokens.grant_project_id", "join_tokens.grant_level"} {
		table, column, _ := strings.Cut(col, ".")
		var ins, upd bool
		if err := d.pool.QueryRow(ctx, `
SELECT has_column_privilege('deploybot_api', 'app.'||$1, $2, 'INSERT'),
       has_column_privilege('deploybot_api', 'app.'||$1, $2, 'UPDATE')
`, table, column).Scan(&ins, &upd); err != nil || !ins || !upd {
			t.Fatalf("%s insert=%v update=%v %v", col, ins, upd, err)
		}
	}
	var ins bool
	if err := d.pool.QueryRow(ctx, `SELECT has_column_privilege('deploybot_api', 'app.audit_log', 'org_id', 'INSERT')`).Scan(&ins); err != nil || !ins {
		t.Fatalf("audit_log.org_id insert=%v %v", ins, err)
	}
}

// assertFactsChain feeds HostFacts through authz.Relate, ServiceProjectFilter,
// ListServices and AuthorizeHost for mixed roles.
func assertFactsChain(t *testing.T, d *DB, ctx context.Context) {
	f := newPgFix(t, d, ctx)
	owner, admin, member, mixed := f.user("fo"), f.user("fa"), f.user("fm"), f.user("fx")
	org := personalOrg(t, d, ctx, owner.ID)
	for _, m := range []struct {
		u    store.User
		role string
	}{{admin, "admin"}, {member, "member"}} {
		if _, err := d.AddOrgMember(ctx, org, m.u.ID, m.role, owner.ID, f.at); err != nil {
			t.Fatal(err)
		}
	}
	p1 := f.project(org, owner.ID, "fp1")
	p2 := f.project(org, owner.ID, "fp2")
	p3 := f.project(org, owner.ID, "fp3")
	p4 := f.project(org, owner.ID, "fp4")
	host := f.host(org, &store.TokenGrant{ProjectID: p1.ID, Level: "deploy"}, owner.ID, "facts")
	for _, g := range []struct {
		p     store.Project
		level string
	}{{p2, "metrics"}, {p3, "deploy"}, {p4, "deploy"}} {
		if _, _, err := d.PutHostAccess(ctx, host.ID, g.p.ID, g.level, owner.ID, f.at); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []struct {
		p    store.Project
		u    store.User
		role string
	}{{p1, admin, "viewer"}, {p2, mixed, "admin"}, {p3, mixed, "admin"}, {p4, mixed, "admin"}} {
		if _, err := d.AddMember(ctx, m.p.ID, m.u.ID, m.role, owner.ID, f.at); err != nil {
			t.Fatal(err)
		}
	}
	s1 := f.row(host.ID, &p1.ID, "s1")
	s2 := f.row(host.ID, &p2.ID, "s2")
	s3 := f.row(host.ID, &p3.ID, "s3")
	f.row(host.ID, nil, "sorg")
	if err := d.DeleteProject(ctx, p4.ID, f.at); err != nil {
		t.Fatal(err)
	}
	relation := func(u store.User) authz.Relation {
		t.Helper()
		facts, err := d.HostFacts(ctx, u.ID, host.ID)
		if err != nil {
			t.Fatal(err)
		}
		access := make([]authz.Access, len(facts.Access))
		for i, a := range facts.Access {
			access[i] = authz.Access{ProjectID: a.ProjectID, Level: a.Level, Role: a.Role}
		}
		return authz.Relate(facts.OrgID, facts.OrgRole, access)
	}
	names := func(rel authz.Relation) map[string]bool {
		t.Helper()
		items, err := d.ListServices(ctx, host.ID, authz.ServiceProjectFilter(rel), f.at)
		if err != nil {
			t.Fatal(err)
		}
		return svcSet(items)
	}
	// An org admin who is also a viewer in p1: every row, org-level included.
	rel := relation(admin)
	if got := names(rel); len(got) != 4 || !got["sorg"] {
		t.Fatalf("org admin rows %v", got)
	}
	if authz.AuthorizeHost(rel, authz.CapServiceWrite, &p2.ID) != nil {
		t.Fatal("org admin cannot edit p2's service")
	}
	// An org member without a project sees no host.
	if err := authz.AuthorizeHost(relation(member), authz.CapHostView, nil); deniedCode(err) != "not_found" {
		t.Fatalf("org member %v", err)
	}
	// Metrics in p2, deploy in p3; the deleted p4 dropped out.
	rel = relation(mixed)
	if len(rel.Access) != 2 {
		t.Fatalf("mixed access %+v", rel.Access)
	}
	if got := names(rel); len(got) != 1 || !got["s3"] {
		t.Fatalf("mixed rows %v", got)
	}
	if deniedCode(authz.AuthorizeHost(rel, authz.CapServiceRead, &p2.ID)) != "forbidden" || authz.AuthorizeHost(rel, authz.CapServiceWrite, &p3.ID) != nil {
		t.Fatal("mixed service checks")
	}
	// An empty non-nil filter matches nothing; a project filter is exact.
	if items, err := d.ListServices(ctx, host.ID, []uuid.UUID{}, f.at); err != nil || len(items) != 0 {
		t.Fatalf("empty filter %v %v", namesOfSvc(items), err)
	}
	if items, err := d.ListServices(ctx, host.ID, []uuid.UUID{p1.ID}, f.at); err != nil || len(items) != 1 || items[0].ID != s1 {
		t.Fatalf("p1 filter %v %v", namesOfSvc(items), err)
	}
	_, _ = s2, s3
	// A deleted org drops the org role.
	if _, err := d.DeleteOrg(ctx, org, f.at); err != nil {
		t.Fatal(err)
	}
	if rel := relation(admin); rel.OrgRole != "" || rel.Sees() {
		t.Fatalf("deleted org relation %+v", rel)
	}
}

func assertHostCursors(t *testing.T, d *DB, ctx context.Context) {
	f := newPgFix(t, d, ctx)
	owner := f.user("co")
	org := personalOrg(t, d, ctx, owner.ID)
	p := f.project(org, owner.ID, "cp")
	var hosts []store.Host
	for _, n := range []string{"a", "b", "c"} {
		hosts = append(hosts, f.host(org, &store.TokenGrant{ProjectID: p.ID, Level: "deploy"}, owner.ID, "cur-"+n))
	}
	page, next, err := d.ListHosts(ctx, p.ID, 2, nil)
	if err != nil || len(page) != 2 || next == nil || page[0].ID != hosts[0].ID || page[1].ID != hosts[1].ID {
		t.Fatalf("page 1 %v %v", page, err)
	}
	// The cursor host loses its access mid-pagination: the next page still resumes.
	if _, err := d.DeleteHostAccess(ctx, hosts[1].ID, p.ID, true); err != nil {
		t.Fatal(err)
	}
	page, next, err = d.ListHosts(ctx, p.ID, 2, next)
	if err != nil || len(page) != 1 || page[0].ID != hosts[2].ID || next != nil {
		t.Fatalf("page 2 %v %+v %v", page, next, err)
	}
	all := []uuid.UUID{}
	var cur *store.Cursor
	for i := 0; i < 5; i++ {
		page, next, err := d.ListOrgHosts(ctx, org, 1, cur)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range page {
			all = append(all, h.ID)
			if h.Access == nil {
				t.Fatal("org host without access")
			}
		}
		if next == nil {
			break
		}
		cur = next
	}
	if len(all) != 3 {
		t.Fatalf("org host pages %v", all)
	}
}

func assertProcessesUnfiltered(t *testing.T, d *DB, ctx context.Context) {
	f := newPgFix(t, d, ctx)
	owner := f.user("po")
	org := personalOrg(t, d, ctx, owner.ID)
	p := f.project(org, owner.ID, "pp")
	host := f.host(org, &store.TokenGrant{ProjectID: p.ID, Level: "deploy"}, owner.ID, "proc")
	svc := f.row(host.ID, nil, "orgsvc")
	// InsertBatch resolves service_id from the "service" tag, as the agent sends it.
	sample := func(pid int, service string) store.Sample {
		tags := map[string]string{"pid": strconv.Itoa(pid), "process_name": "p", "user": "root", "cmdline": "p"}
		if service != "" {
			tags["service"] = service
		}
		return store.Sample{
			M: "process", T: f.at,
			Tags:   tags,
			Floats: map[string]float64{"cpu_usage": 1, "memory_usage": 1},
			Ints:   map[string]int64{"memory_rss": 1, "num_threads": 1},
		}
	}
	if _, err := d.InsertBatch(ctx, host.ID, uuid.New(), []store.Sample{sample(11, "orgsvc"), sample(22, "")}, f.at); err != nil {
		t.Fatal(err)
	}
	_, items, err := d.LatestProcesses(ctx, host.ID, f.at.Add(-time.Minute), "cpu", 50)
	if err != nil || len(items) != 2 {
		t.Fatalf("processes %+v %v", items, err)
	}
	for _, it := range items {
		if it.PID == 11 && (it.ServiceID == nil || *it.ServiceID != svc) {
			t.Fatalf("org-level service id %+v", it)
		}
		if it.PID == 22 && it.ServiceID != nil {
			t.Fatalf("null service id %+v", it)
		}
	}
}

func assertProjectDeleteOrg(t *testing.T, d *DB, ctx context.Context) {
	f := newPgFix(t, d, ctx)
	owner := f.user("do")
	org := personalOrg(t, d, ctx, owner.ID)
	p := f.project(org, owner.ID, "dp")
	host := f.host(org, &store.TokenGrant{ProjectID: p.ID, Level: "deploy"}, owner.ID, "del")
	svc := f.row(host.ID, &p.ID, "dsvc")
	_, hash, _ := secret.NewToken()
	tok, err := d.CreateJoinToken(ctx, org, hash, nil, &store.TokenGrant{ProjectID: p.ID, Level: "metrics"}, owner.ID, f.at.Add(time.Hour), f.at)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteProject(ctx, p.ID, f.at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetService(ctx, svc, f.at)
	if err != nil || got.GoneAt == nil {
		t.Fatalf("service %+v %v", got, err)
	}
	toks, _, err := d.ListJoinTokens(ctx, org, nil, 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range toks {
		if x.ID == tok.ID && x.RevokedAt == nil {
			t.Fatal("granting token not revoked")
		}
	}
	if h, _ := d.GetHost(ctx, host.ID); h.RevokedAt != nil {
		t.Fatal("project delete revoked the host")
	}
}

func assertOrgDelete(t *testing.T, d *DB, ctx context.Context) {
	f := newPgFix(t, d, ctx)
	owner := f.user("oo")
	org, err := d.CreateOrg(ctx, owner.ID, "Gone", "gone-"+f.tag, f.at)
	if err != nil {
		t.Fatal(err)
	}
	p := f.project(org.ID, owner.ID, "op")
	_, hash, _ := secret.NewToken()
	if _, err := d.CreateJoinToken(ctx, org.ID, hash, nil, nil, owner.ID, f.at.Add(time.Hour), f.at); err != nil {
		t.Fatal(err)
	}
	_, credHash, _ := secret.NewToken()
	_, tokHash, _ := secret.NewToken()
	if _, err := d.CreateJoinToken(ctx, org.ID, tokHash, nil, &store.TokenGrant{ProjectID: p.ID, Level: "deploy"}, owner.ID, f.at.Add(time.Hour), f.at); err != nil {
		t.Fatal(err)
	}
	host, err := d.ConsumeJoinToken(ctx, tokHash, store.EnrollInput{Name: "gone-" + f.tag, NodeType: "pi"}, uuid.New(), credHash, f.at)
	if err != nil {
		t.Fatal(err)
	}
	svc := f.row(host.ID, nil, "gsvc")
	ids, err := d.DeleteOrg(ctx, org.ID, f.at.Add(time.Minute))
	if err != nil || len(ids) != 1 || ids[0] != host.ID {
		t.Fatalf("delete %v %v", ids, err)
	}
	cred, _, err := d.LookupCredential(ctx, credHash)
	if err != nil || cred.RevokedAt == nil {
		t.Fatalf("credential %+v %v", cred, err)
	}
	if got, _ := d.GetService(ctx, svc, f.at); got.GoneAt == nil {
		t.Fatal("service not gone")
	}
	var live int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM app.join_tokens WHERE org_id = $1::uuid AND revoked_at IS NULL AND used_at IS NULL`, org.ID.String()).Scan(&live); err != nil || live != 0 {
		t.Fatalf("live tokens %d %v", live, err)
	}
}

func assertHostAccessCascade(t *testing.T, d *DB, ctx context.Context) {
	f := newPgFix(t, d, ctx)
	owner := f.user("ho")
	org := personalOrg(t, d, ctx, owner.ID)
	p := f.project(org, owner.ID, "hp")
	hostID := uuid.New()
	if _, err := d.pool.Exec(ctx, `
INSERT INTO app.hosts (id, org_id, project_id, name, node_type, os, arch, always_on, agent_version, enrolled_at)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'pi', 'linux', 'arm64', true, '0.2.8', now())
`, hostID.String(), org.String(), p.ID.String(), "casc-"+f.tag); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.PutHostAccess(ctx, hostID, p.ID, "deploy", owner.ID, f.at); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `DELETE FROM app.hosts WHERE id = $1::uuid`, hostID.String()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM app.host_access WHERE host_id = $1::uuid`, hostID.String()).Scan(&n); err != nil || n != 0 {
		t.Fatalf("access rows left %d %v", n, err)
	}
	if _, err := d.HostProjectAccess(ctx, p.ID, hostID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted host %v", err)
	}
}
