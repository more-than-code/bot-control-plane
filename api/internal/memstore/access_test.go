package memstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// TestHostFactsOrgAndAccess: the caller's org role and every access row of
// a project they belong to (api-v1 v1.24 item 2).
func TestHostFactsOrgAndAccess(t *testing.T) {
	ctx := context.Background()
	mem := New()
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	owner := mustUser(t, mem, "owner@ex.com", at)
	guest := mustUser(t, mem, "guest@ex.com", at)
	org := mem.PersonalOrg(owner.ID)
	home := mustProject(t, mem, org, owner.ID, "Home", "home", at)
	metrics := mustProject(t, mem, org, guest.ID, "Metrics", "metrics", at)
	deploy := mustProject(t, mem, org, owner.ID, "Deploy", "deploy", at)
	if _, err := mem.AddMember(ctx, deploy.ID, guest.ID, "admin", owner.ID, at); err != nil {
		t.Fatal(err)
	}
	host := enrollMem(t, mem, home.ID, owner.ID, "pi", at)
	if _, _, err := mem.PutHostAccess(ctx, host.ID, metrics.ID, "metrics", owner.ID, at); err != nil {
		t.Fatal(err)
	}
	if _, created, err := mem.PutHostAccess(ctx, host.ID, deploy.ID, "deploy", owner.ID, at); err != nil || !created {
		t.Fatal(created, err)
	}
	facts, err := mem.HostFacts(ctx, guest.ID, host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if facts.OrgID != org || facts.OrgRole != "" || len(facts.Access) != 2 {
		t.Fatalf("guest facts %+v", facts)
	}
	got := map[uuid.UUID]store.MemberAccess{}
	for _, a := range facts.Access {
		got[a.ProjectID] = a
	}
	if got[metrics.ID].Level != "metrics" || got[metrics.ID].Role != "owner" {
		t.Fatalf("metrics row %+v", got[metrics.ID])
	}
	if got[deploy.ID].Level != "deploy" || got[deploy.ID].Role != "admin" {
		t.Fatalf("deploy row %+v", got[deploy.ID])
	}
	ownerFacts, err := mem.HostFacts(ctx, owner.ID, host.ID)
	if err != nil || ownerFacts.OrgRole != "owner" || len(ownerFacts.Access) != 2 {
		t.Fatalf("owner facts %+v %v", ownerFacts, err)
	}
	if _, err := mem.HostFacts(ctx, owner.ID, uuid.New()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing host %v", err)
	}
}

// TestEnrolmentPerOrg: the token's grant becomes an access row, names are
// unique per org, and the enrolled host names the grant project for the
// host.enroll audit row.
func TestEnrolmentPerOrg(t *testing.T) {
	ctx := context.Background()
	mem := New()
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	owner := mustUser(t, mem, "own@ex.com", at)
	other := mustUser(t, mem, "oth@ex.com", at)
	org := mem.PersonalOrg(owner.ID)
	home := mustProject(t, mem, org, owner.ID, "Home", "home", at)
	first := enrollMem(t, mem, home.ID, owner.ID, "pi", at)
	second := enrollMem(t, mem, home.ID, owner.ID, "pi", at)
	if first.Name != "pi" || second.Name != "pi-2" || second.OrgID != org {
		t.Fatalf("names %q %q", first.Name, second.Name)
	}
	if second.GrantProjectID == nil || *second.GrantProjectID != home.ID {
		t.Fatalf("grant project %v", second.GrantProjectID)
	}
	level, err := mem.HostProjectAccess(ctx, home.ID, second.ID)
	if err != nil || level != "deploy" {
		t.Fatalf("grant %q %v", level, err)
	}
	// Another org may reuse the name.
	away := mustProject(t, mem, mem.PersonalOrg(other.ID), other.ID, "Away", "away", at)
	elsewhere := enrollMem(t, mem, away.ID, other.ID, "pi", at)
	if elsewhere.Name != "pi" {
		t.Fatalf("per-org name %q", elsewhere.Name)
	}
	// A token without a grant: no access row and no grant project.
	bare := enrollToken(t, mem, org, nil, owner.ID, "bare", at)
	if bare.GrantProjectID != nil {
		t.Fatalf("bare grant project %v", bare.GrantProjectID)
	}
	if rows, err := mem.ListHostAccess(ctx, bare.ID); err != nil || len(rows) != 0 {
		t.Fatalf("bare access %+v %v", rows, err)
	}
	// A grant naming another org's project is refused.
	_, hash, _ := secret.NewToken()
	if _, err := mem.CreateJoinToken(ctx, org, hash, nil, &store.TokenGrant{ProjectID: away.ID, Level: "deploy"}, owner.ID, at.Add(time.Hour), at); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org grant %v", err)
	}
	if _, err := mem.CreateJoinToken(ctx, org, hash, nil, &store.TokenGrant{ProjectID: home.ID, Level: "owner"}, owner.ID, at.Add(time.Hour), at); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("bad level %v", err)
	}
	// The project-scoped token list only sees tokens granting that project.
	all, _, err := mem.ListJoinTokens(ctx, org, nil, 50, nil)
	if err != nil || len(all) != 3 {
		t.Fatalf("org tokens %d %v", len(all), err)
	}
	scoped, _, err := mem.ListJoinTokens(ctx, org, &home.ID, 50, nil)
	if err != nil || len(scoped) != 2 {
		t.Fatalf("project tokens %d %v", len(scoped), err)
	}
	var bareToken uuid.UUID
	for _, tok := range all {
		if tok.Grant == nil {
			bareToken = tok.ID
		}
	}
	if err := mem.RevokeJoinToken(ctx, org, &home.ID, bareToken, at); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("project revoke of an org token %v", err)
	}
	if err := mem.RevokeJoinToken(ctx, org, nil, bareToken, at); err != nil {
		t.Fatal(err)
	}
}

// TestHostAccessWrites: the project must be in the host's org; delete is a
// conflict while services remain unless reassigned to the org.
func TestHostAccessWrites(t *testing.T) {
	ctx := context.Background()
	mem := New()
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	owner := mustUser(t, mem, "a@ex.com", at)
	stranger := mustUser(t, mem, "b@ex.com", at)
	org := mem.PersonalOrg(owner.ID)
	home := mustProject(t, mem, org, owner.ID, "Home", "home", at)
	app := mustProject(t, mem, org, owner.ID, "App", "app", at)
	away := mustProject(t, mem, mem.PersonalOrg(stranger.ID), stranger.ID, "Away", "away", at)
	host := enrollMem(t, mem, home.ID, owner.ID, "pi", at)
	if _, _, err := mem.PutHostAccess(ctx, host.ID, away.ID, "deploy", owner.ID, at); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-org grant %v", err)
	}
	if _, _, err := mem.PutHostAccess(ctx, host.ID, app.ID, "nope", owner.ID, at); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("bad level %v", err)
	}
	row, created, err := mem.PutHostAccess(ctx, host.ID, app.ID, "metrics", owner.ID, at)
	if err != nil || !created || row.ProjectName != "App" {
		t.Fatalf("grant %+v %v %v", row, created, err)
	}
	later := at.Add(time.Hour)
	row, created, err = mem.PutHostAccess(ctx, host.ID, app.ID, "deploy", stranger.ID, later)
	if err != nil || created || row.Level != "deploy" || !row.GrantedAt.Equal(at) || *row.GrantedBy != owner.ID {
		t.Fatalf("update %+v %v %v", row, created, err)
	}
	svc := mem.PutDiscovered(host.ID, "web", "process", nil, nil, at)
	mem.SetServiceProject(svc.ID, app.ID)
	if _, err := mem.DeleteHostAccess(ctx, host.ID, app.ID, false); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("conflict %v", err)
	}
	removed, err := mem.DeleteHostAccess(ctx, host.ID, app.ID, true)
	if err != nil || removed.ProjectID != app.ID || removed.ProjectName != "App" {
		t.Fatalf("remove %+v %v", removed, err)
	}
	got := mustService(t, mem, svc.ID, at)
	if got.ProjectID != nil {
		t.Fatalf("reassign=org kept project %v", got.ProjectID)
	}
	if _, err := mem.DeleteHostAccess(ctx, host.ID, app.ID, false); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second remove %v", err)
	}
	if level, err := mem.HostProjectAccess(ctx, app.ID, host.ID); err != nil || level != "" {
		t.Fatalf("level after remove %q %v", level, err)
	}
	// The org-level row is listed with a nil filter only.
	all, err := mem.ListServices(ctx, host.ID, nil, at)
	if err != nil || len(all) != 1 {
		t.Fatalf("all %+v %v", all, err)
	}
	none, err := mem.ListServices(ctx, host.ID, []uuid.UUID{home.ID, app.ID}, at)
	if err != nil || len(none) != 0 {
		t.Fatalf("project filter shows org-level row %+v %v", none, err)
	}
}

// TestDeleteProjectKeepsHosts is api-v1 v1.24 item 7.
func TestDeleteProjectKeepsHosts(t *testing.T) {
	ctx := context.Background()
	mem := New()
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	owner := mustUser(t, mem, "d@ex.com", at)
	org := mem.PersonalOrg(owner.ID)
	home := mustProject(t, mem, org, owner.ID, "Home", "home", at)
	host := enrollMem(t, mem, home.ID, owner.ID, "pi", at)
	svc := mem.PutDiscovered(host.ID, "web", "process", nil, nil, at)
	mem.SetServiceProject(svc.ID, home.ID)
	_, hash, _ := secret.NewToken()
	pending, err := mem.CreateJoinToken(ctx, org, hash, nil, &store.TokenGrant{ProjectID: home.ID, Level: "deploy"}, owner.ID, at.Add(time.Hour), at)
	if err != nil {
		t.Fatal(err)
	}
	del := at.Add(time.Minute)
	if err := mem.DeleteProject(ctx, home.ID, del); err != nil {
		t.Fatal(err)
	}
	h, err := mem.GetHost(ctx, host.ID)
	if err != nil || h.RevokedAt != nil {
		t.Fatalf("host revoked %+v %v", h, err)
	}
	if rows, err := mem.ListHostAccess(ctx, host.ID); err != nil || len(rows) != 0 {
		t.Fatalf("access left %+v %v", rows, err)
	}
	got := mustService(t, mem, svc.ID, del)
	if got.GoneAt == nil || !got.GoneAt.Equal(del) {
		t.Fatalf("service not gone %+v", got)
	}
	toks, _, err := mem.ListJoinTokens(ctx, org, nil, 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range toks {
		if tok.ID == pending.ID && tok.RevokedAt == nil {
			t.Fatal("token granting the deleted project still usable")
		}
	}
	hosts, _, err := mem.ListOrgHosts(ctx, org, 50, nil)
	if err != nil || len(hosts) != 1 || hosts[0].Access == nil || len(*hosts[0].Access) != 0 {
		t.Fatalf("org hosts %+v %v", hosts, err)
	}
}

// TestDeleteOrgRevokesHosts: org delete revokes every host and deletes every project.
func TestDeleteOrgRevokesHosts(t *testing.T) {
	ctx := context.Background()
	mem := New()
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	owner := mustUser(t, mem, "o@ex.com", at)
	org := mem.PersonalOrg(owner.ID)
	home := mustProject(t, mem, org, owner.ID, "Home", "home", at)
	host := enrollMem(t, mem, home.ID, owner.ID, "pi", at)
	ids, err := mem.DeleteOrg(ctx, org, at)
	if err != nil || len(ids) != 1 || ids[0] != host.ID {
		t.Fatalf("delete %v %v", ids, err)
	}
	if h, _ := mem.GetHost(ctx, host.ID); h.RevokedAt == nil {
		t.Fatal("host not revoked")
	}
	if _, err := mem.GetProject(ctx, home.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("project kept %v", err)
	}
	if orgs, _ := mem.ListUserOrgs(ctx, owner.ID); len(orgs) != 0 {
		t.Fatalf("org listed %+v", orgs)
	}
}

// TestOrgMembersAndReach: org admins reach every project as admin (via org);
// org members reach none; a project member outside the org keeps access.
func TestOrgMembersAndReach(t *testing.T) {
	ctx := context.Background()
	mem := New()
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	owner := mustUser(t, mem, "own@ex.com", at)
	admin := mustUser(t, mem, "adm@ex.com", at)
	member := mustUser(t, mem, "mem@ex.com", at)
	outside := mustUser(t, mem, "out@ex.com", at)
	org, err := mem.CreateOrg(ctx, owner.ID, "More Than Code", "more-than-code", at)
	if err != nil || org.Role != "owner" || org.Slug != "more-than-code" {
		t.Fatalf("org %+v %v", org, err)
	}
	if again, err := mem.CreateOrg(ctx, owner.ID, "Again", "more-than-code", at); err != nil || again.Slug != "more-than-code-2" {
		t.Fatalf("slug suffix %+v %v", again, err)
	}
	p := mustProject(t, mem, org.ID, owner.ID, "App", "app", at)
	if _, err := mem.AddMember(ctx, p.ID, outside.ID, "viewer", owner.ID, at); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, admin.ID, "admin", owner.ID, at); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, member.ID, "member", owner.ID, at); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, member.ID, "admin", owner.ID, at); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate %v", err)
	}
	if _, err := mem.AddOrgMember(ctx, org.ID, outside.ID, "owner", owner.ID, at); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("owner role %v", err)
	}
	list, err := mem.ListOrgProjects(ctx, org.ID, admin.ID)
	if err != nil || len(list) != 1 || list[0].Role != "admin" || list[0].Via != "org" {
		t.Fatalf("admin reach %+v %v", list, err)
	}
	if list, _ := mem.ListOrgProjects(ctx, org.ID, member.ID); len(list) != 0 {
		t.Fatalf("member reach %+v", list)
	}
	all, err := mem.ListAllProjects(ctx, outside.ID)
	if err != nil || len(all) != 1 || all[0].Role != "viewer" || all[0].Via != "member" {
		t.Fatalf("outside collaborator %+v %v", all, err)
	}
	pa, err := mem.ProjectAccess(ctx, p.ID, admin.ID)
	if err != nil || pa.MemberRole != "" || pa.OrgRole != "admin" || pa.OrgID != org.ID {
		t.Fatalf("project access %+v %v", pa, err)
	}
	if _, err := mem.UpdateOrgMemberRole(ctx, org.ID, owner.ID, "member"); !errors.Is(err, store.ErrOwner) {
		t.Fatalf("demote owner %v", err)
	}
	if err := mem.RemoveOrgMember(ctx, org.ID, owner.ID); !errors.Is(err, store.ErrOwner) {
		t.Fatalf("remove owner %v", err)
	}
	if err := mem.TransferOrgOwnership(ctx, org.ID, outside.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("transfer to non-member %v", err)
	}
	if err := mem.TransferOrgOwnership(ctx, org.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	if r, _ := mem.OrgRole(ctx, org.ID, owner.ID); r != "admin" {
		t.Fatalf("old owner %q", r)
	}
	if r, _ := mem.OrgRole(ctx, org.ID, admin.ID); r != "owner" {
		t.Fatalf("new owner %q", r)
	}
	if err := mem.RemoveOrgMember(ctx, org.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.OrgRole(ctx, org.ID, member.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removed member %v", err)
	}
}

func TestHostListCursorByName(t *testing.T) {
	ctx := context.Background()
	mem := New()
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	owner := mustUser(t, mem, "c@ex.com", at)
	org := mem.PersonalOrg(owner.ID)
	home := mustProject(t, mem, org, owner.ID, "Home", "home", at)
	for _, name := range []string{"c", "a", "b"} {
		enrollMem(t, mem, home.ID, owner.ID, name, at)
	}
	page, next, err := mem.ListHosts(ctx, home.ID, 2, nil)
	if err != nil || len(page) != 2 || page[0].Name != "a" || page[1].Name != "b" || next == nil || *next.Rank != 0 {
		t.Fatalf("page 1 %+v %+v %v", page, next, err)
	}
	if page[0].AccessLevel == nil || *page[0].AccessLevel != "deploy" {
		t.Fatalf("access level %v", page[0].AccessLevel)
	}
	page, next, err = mem.ListHosts(ctx, home.ID, 2, next)
	if err != nil || len(page) != 1 || page[0].Name != "c" || next != nil {
		t.Fatalf("page 2 %+v %+v %v", page, next, err)
	}
	one := 1
	if page, _, _ := mem.ListHosts(ctx, home.ID, 2, &store.Cursor{Rank: &one, Name: "a"}); len(page) != 0 {
		t.Fatalf("old shared cursor %+v", page)
	}
}

func mustUser(t *testing.T, mem *Mem, email string, at time.Time) store.User {
	t.Helper()
	u, err := mem.UpsertGoogleUser(context.Background(), store.GoogleSignIn{
		Subject: email, Email: email, Name: email, At: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func mustProject(t *testing.T, mem *Mem, orgID, owner uuid.UUID, name, slug string, at time.Time) store.Project {
	t.Helper()
	p, err := mem.CreateProject(context.Background(), orgID, owner, name, slug, at)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// enrollMem enrols a host in the project's org with deploy access for the project.
func enrollMem(t *testing.T, mem *Mem, projectID, ownerID uuid.UUID, name string, at time.Time) store.Host {
	t.Helper()
	p, err := mem.GetProject(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	return enrollToken(t, mem, p.OrgID, &store.TokenGrant{ProjectID: projectID, Level: "deploy"}, ownerID, name, at)
}

func enrollToken(t *testing.T, mem *Mem, orgID uuid.UUID, grant *store.TokenGrant, ownerID uuid.UUID, name string, at time.Time) store.Host {
	t.Helper()
	ctx := context.Background()
	_, hash, err := secret.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.CreateJoinToken(ctx, orgID, hash, nil, grant, ownerID, at.Add(time.Hour), at); err != nil {
		t.Fatal(err)
	}
	_, credHash, err := secret.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	host, err := mem.ConsumeJoinToken(ctx, hash, store.EnrollInput{
		Name: name, NodeType: "pi", OS: "linux", Arch: "arm64", AlwaysOn: true, AgentVersion: "0.1.0",
	}, uuid.New(), credHash, at)
	if err != nil {
		t.Fatal(err)
	}
	return host
}

// reportAllowlist stores a v0.2.8 hello: programs with a projects allowlist,
// so programs may be placed for several deploy projects (D-10).
func reportAllowlist(t *testing.T, mem *Mem, hostID uuid.UUID) {
	t.Helper()
	if err := mem.SetHostPrograms(context.Background(), hostID, &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, Projects: []string{}}); err != nil {
		t.Fatal(err)
	}
}

// TestOwnedReportNeverMovesRows is review A2: rows are keyed by (host, name)
// and an owned row is named after its entry, so two projects' entries share a
// name. A report never moves another project's row or an org-level row.
func TestOwnedReportNeverMovesRows(t *testing.T) {
	ctx := context.Background()
	mem := New()
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	owner := catalogUser(t, mem, "a2-owner")
	org := mem.PersonalOrg(owner.ID)
	one := mustProject(t, mem, org, owner.ID, "One", "a2one", at)
	two := mustProject(t, mem, org, owner.ID, "Two", "a2two", at)
	host := enrollMem(t, mem, one.ID, owner.ID, "a2-host", at)
	reportAllowlist(t, mem, host.ID)
	if _, _, err := mem.PutHostAccess(ctx, host.ID, two.ID, "deploy", owner.ID, at); err != nil {
		t.Fatal(err)
	}
	spec, hash, display := catalogSpec(t, "pnpm run dev")
	for _, p := range []store.Project{one, two} {
		if _, err := mem.CreateCatalogEntry(ctx, p.ID, store.CatalogWrite{Name: "web", Spec: spec, SpecHash: hash, CommandDisplay: display, Actor: owner.ID, At: at}); err != nil {
			t.Fatal(err)
		}
	}
	report := func(slug string, extra ...store.Discovered) {
		t.Helper()
		items := append([]store.Discovered{{Name: "x", Kind: "process", Owner: &store.DiscoveredOwner{Kind: "program", Project: slug, Entry: "web"}}}, extra...)
		if err := mem.UpsertDiscovered(ctx, host.ID, items, at, true, false); err != nil {
			t.Fatal(err)
		}
	}
	report("a2one")
	items, _ := mem.ListServices(ctx, host.ID, nil, at)
	web := findService(t, items, "web")
	if !sameID(web.ProjectID, one.ID) {
		t.Fatalf("first owner %+v", web)
	}
	// The same name owned by the other project: the row stays one's.
	report("a2two")
	items, _ = mem.ListServices(ctx, host.ID, nil, at)
	if got := findService(t, items, "web"); !sameID(got.ProjectID, one.ID) || got.Source != "owned" {
		t.Fatalf("takeover %+v", got)
	}
	// An org-level row with an entry's name is not claimed either.
	mem.PutDiscovered(host.ID, "sshd", "process", nil, nil, at)
	sshd := findService(t, mustList(t, mem, host.ID, at), "sshd")
	mem.SetServiceProject(sshd.ID, uuid.Nil)
	if _, err := mem.CreateCatalogEntry(ctx, one.ID, store.CatalogWrite{Name: "sshd", Spec: spec, SpecHash: hash, CommandDisplay: display, Actor: owner.ID, At: at}); err != nil {
		t.Fatal(err)
	}
	if err := mem.UpsertDiscovered(ctx, host.ID, []store.Discovered{{Name: "y", Kind: "process", Owner: &store.DiscoveredOwner{Kind: "program", Project: "a2one", Entry: "sshd"}}}, at, true, false); err != nil {
		t.Fatal(err)
	}
	if got := mustService(t, mem, sshd.ID, at); got.ProjectID != nil || got.Source != "discovered" {
		t.Fatalf("org-level row claimed %+v", got)
	}
	conflicts := 0
	for _, a := range mem.AllAudits() {
		if a.Action == "service.owner_conflict" {
			conflicts++
			if a.OrgID == nil || *a.OrgID != org || a.HostID == nil {
				t.Fatalf("conflict audit %+v", a)
			}
		}
	}
	if conflicts != 2 {
		t.Fatalf("conflict audits %d", conflicts)
	}
}

func mustList(t *testing.T, mem *Mem, hostID uuid.UUID, at time.Time) []store.Service {
	t.Helper()
	items, err := mem.ListServices(context.Background(), hostID, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// TestTokenCreatorMustStayAdmin is review A7.
func TestTokenCreatorMustStayAdmin(t *testing.T) {
	ctx := context.Background()
	mem := New()
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	owner := mustUser(t, mem, "a7-owner@ex.com", at)
	admin := mustUser(t, mem, "a7-admin@ex.com", at)
	org := mem.PersonalOrg(owner.ID)
	if _, err := mem.AddOrgMember(ctx, org, admin.ID, "admin", owner.ID, at); err != nil {
		t.Fatal(err)
	}
	_, hash, _ := secret.NewToken()
	if _, err := mem.CreateJoinToken(ctx, org, hash, nil, nil, admin.ID, at.Add(time.Hour), at); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.UpdateOrgMemberRole(ctx, org, admin.ID, "member"); err != nil {
		t.Fatal(err)
	}
	_, cred, _ := secret.NewToken()
	if _, err := mem.ConsumeJoinToken(ctx, hash, store.EnrollInput{Name: "pi", NodeType: "pi"}, uuid.New(), cred, at); !errors.Is(err, store.ErrUnauthorized) {
		t.Fatalf("demoted creator's token %v", err)
	}
	if _, err := mem.UpdateOrgMemberRole(ctx, org, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.ConsumeJoinToken(ctx, hash, store.EnrollInput{Name: "pi", NodeType: "pi"}, uuid.New(), cred, at); err != nil {
		t.Fatalf("restored creator's token %v", err)
	}
}

// TestSignInGivesOrglessUserAnOrg is review B16 (D-8 for users from before orgs).
func TestSignInGivesOrglessUserAnOrg(t *testing.T) {
	ctx := context.Background()
	mem := New()
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	u := mustUser(t, mem, "b16@ex.com", at)
	if _, err := mem.DeleteOrg(ctx, mem.PersonalOrg(u.ID), at); err != nil {
		t.Fatal(err)
	}
	if orgs, _ := mem.ListUserOrgs(ctx, u.ID); len(orgs) != 0 {
		t.Fatalf("orgs %+v", orgs)
	}
	mustUser(t, mem, "b16@ex.com", at.Add(time.Hour))
	orgs, err := mem.ListUserOrgs(ctx, u.ID)
	if err != nil || len(orgs) != 1 || orgs[0].Role != "owner" {
		t.Fatalf("sign-in org %+v %v", orgs, err)
	}
	// A user who is in an org gets no second one.
	mustUser(t, mem, "b16@ex.com", at.Add(2*time.Hour))
	if orgs, _ := mem.ListUserOrgs(ctx, u.ID); len(orgs) != 1 {
		t.Fatalf("second org %+v", orgs)
	}
}

// TestAccessRefusedOnDeadHosts is the review D22 decision: no grant on a
// revoked host or in a deleted org.
func TestAccessRefusedOnDeadHosts(t *testing.T) {
	ctx := context.Background()
	mem := New()
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	owner := mustUser(t, mem, "d22@ex.com", at)
	org := mem.PersonalOrg(owner.ID)
	p := mustProject(t, mem, org, owner.ID, "P", "d22p", at)
	q := mustProject(t, mem, org, owner.ID, "Q", "d22q", at)
	host := enrollMem(t, mem, p.ID, owner.ID, "d22", at)
	if err := mem.RevokeHost(ctx, host.ID, at); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mem.PutHostAccess(ctx, host.ID, q.ID, "metrics", owner.ID, at); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked host grant %v", err)
	}
}

// TestReassignOrgKeepsDeclared is review C17 in memstore.
func TestReassignOrgKeepsDeclared(t *testing.T) {
	ctx := context.Background()
	mem := New()
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	owner := mustUser(t, mem, "c17@ex.com", at)
	org := mem.PersonalOrg(owner.ID)
	p := mustProject(t, mem, org, owner.ID, "P", "c17p", at)
	q := mustProject(t, mem, org, owner.ID, "Q", "c17q", at)
	host := enrollMem(t, mem, p.ID, owner.ID, "c17", at)
	if _, _, err := mem.PutHostAccess(ctx, host.ID, q.ID, "deploy", owner.ID, at); err != nil {
		t.Fatal(err)
	}
	declared := mem.PutDeclared(host.ID, "decl", "process", nil, at)
	mem.SetServiceProject(declared.ID, q.ID)
	if _, err := mem.DeleteHostAccess(ctx, host.ID, q.ID, true); err != nil {
		t.Fatal(err)
	}
	got := mustService(t, mem, declared.ID, at)
	if got.Source != "declared" || got.ProjectID != nil || got.CatalogEntry != nil {
		t.Fatalf("declared after reassign %+v", got)
	}
}
