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
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/memstore"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// parityStores is memstore and, with DEPLOYBOT_TEST_DATABASE_URL, Postgres:
// the org and access behaviours run against both (review D23).
func parityStores(t *testing.T) map[string]store.Store {
	t.Helper()
	out := map[string]store.Store{"memstore": memstore.New()}
	url := os.Getenv("DEPLOYBOT_TEST_DATABASE_URL")
	if url == "" {
		return out
	}
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
	out["postgres"] = d
	return out
}

type parity struct {
	t   *testing.T
	st  store.Store
	ctx context.Context
	at  time.Time
	tag string
}

func (p parity) user(name string) store.User {
	p.t.Helper()
	u, err := p.st.UpsertGoogleUser(p.ctx, store.GoogleSignIn{Subject: name + "-" + p.tag, Email: name + "-" + p.tag + "@example.com", Name: name, At: p.at})
	if err != nil {
		p.t.Fatal(err)
	}
	return u
}

func (p parity) org(userID uuid.UUID) uuid.UUID {
	p.t.Helper()
	orgs, err := p.st.ListUserOrgs(p.ctx, userID)
	if err != nil {
		p.t.Fatal(err)
	}
	for _, o := range orgs {
		if o.Role == store.OrgRoleOwner {
			return o.ID
		}
	}
	p.t.Fatal("no owned org")
	return uuid.Nil
}

func (p parity) project(orgID, owner uuid.UUID, slug string) store.Project {
	p.t.Helper()
	pr, err := p.st.CreateProject(p.ctx, orgID, owner, slug, slug+"-"+p.tag, p.at)
	if err != nil {
		p.t.Fatal(err)
	}
	return pr
}

func (p parity) enroll(orgID uuid.UUID, grant *store.TokenGrant, by uuid.UUID, name string) store.Host {
	p.t.Helper()
	_, hash, _ := secret.NewToken()
	if _, err := p.st.CreateJoinToken(p.ctx, orgID, hash, nil, grant, by, p.at.Add(time.Hour), p.at); err != nil {
		p.t.Fatal(err)
	}
	_, cred, _ := secret.NewToken()
	h, err := p.st.ConsumeJoinToken(p.ctx, hash, store.EnrollInput{Name: name + "-" + p.tag, NodeType: "pi", OS: "linux", Arch: "arm64", AlwaysOn: true, AgentVersion: "0.2.8"}, uuid.New(), cred, p.at)
	if err != nil {
		p.t.Fatal(err)
	}
	return h
}

func (p parity) service(hostID uuid.UUID, name string) store.Service {
	p.t.Helper()
	items, err := p.st.ListServices(p.ctx, hostID, nil, p.at)
	if err != nil {
		p.t.Fatal(err)
	}
	for _, s := range items {
		if s.Name == name {
			return s
		}
	}
	p.t.Fatalf("no service %s", name)
	return store.Service{}
}

func runParity(t *testing.T, fn func(p parity)) {
	t.Helper()
	for name, st := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			fn(parity{t: t, st: st, ctx: context.Background(), at: time.Now().UTC().Truncate(time.Microsecond), tag: strings.ReplaceAll(uuid.NewString(), "-", "")[:8]})
		})
	}
}

func TestParityOrgs(t *testing.T) {
	runParity(t, func(p parity) {
		owner, admin, outside := p.user("po"), p.user("pa"), p.user("px")
		first, err := p.st.CreateOrg(p.ctx, owner.ID, "Dup", "dup-"+p.tag, p.at)
		if err != nil {
			p.t.Fatal(err)
		}
		second, err := p.st.CreateOrg(p.ctx, owner.ID, "Dup", "dup-"+p.tag, p.at)
		if err != nil || second.Slug != first.Slug+"-2" {
			p.t.Fatalf("slug suffix %q %v", second.Slug, err)
		}
		org := first.ID
		if _, err := p.st.AddOrgMember(p.ctx, org, outside.ID, "owner", owner.ID, p.at); !errors.Is(err, store.ErrInvalid) {
			p.t.Fatalf("owner role %v", err)
		}
		if _, err := p.st.AddOrgMember(p.ctx, org, admin.ID, "admin", owner.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		if err := p.st.TransferOrgOwnership(p.ctx, org, outside.ID); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("transfer to a non-member %v", err)
		}
		if _, err := p.st.UpdateOrgMemberRole(p.ctx, org, owner.ID, "member"); !errors.Is(err, store.ErrOwner) {
			p.t.Fatalf("demote owner %v", err)
		}
		if err := p.st.TransferOrgOwnership(p.ctx, org, admin.ID); err != nil {
			p.t.Fatal(err)
		}
		if r, _ := p.st.OrgRole(p.ctx, org, owner.ID); r != "admin" {
			p.t.Fatalf("old owner %q", r)
		}
		if err := p.st.RemoveOrgMember(p.ctx, org, owner.ID); err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.OrgRole(p.ctx, org, owner.ID); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("removed %v", err)
		}
		host := p.enroll(org, nil, admin.ID, "audit")
		if err := p.st.InsertAudit(p.ctx, store.AuditRow{At: p.at, Via: "console", HostID: &host.ID, Action: "host.access_grant", Decision: "allowed"}); err != nil {
			p.t.Fatal(err)
		}
		rows, _, err := p.st.ListOrgAudit(p.ctx, org, 50, nil)
		if err != nil || len(rows) == 0 || rows[0].OrgID == nil || *rows[0].OrgID != org {
			p.t.Fatalf("org audit derived from host %+v %v", rows, err)
		}
	})
}

func TestParityTokensAndEnrolment(t *testing.T) {
	runParity(t, func(p parity) {
		owner, admin := p.user("to"), p.user("ta")
		org := p.org(owner.ID)
		home := p.project(org, owner.ID, "thome")
		// other is created later, so home is the org's oldest live project.
		other, err := p.st.CreateProject(p.ctx, org, owner.ID, "tother", "tother-"+p.tag, p.at.Add(time.Second))
		if err != nil {
			p.t.Fatal(err)
		}
		// Mirror (review A6): deploy grant names the project; a metrics
		// grant or no grant takes the org's oldest live project.
		if h := p.enroll(org, &store.TokenGrant{ProjectID: other.ID, Level: "deploy"}, owner.ID, "d"); h.ProjectID == nil || *h.ProjectID != other.ID {
			p.t.Fatalf("deploy mirror %v", h.ProjectID)
		}
		for _, g := range []*store.TokenGrant{{ProjectID: other.ID, Level: "metrics"}, nil} {
			h := p.enroll(org, g, owner.ID, "m")
			if h.ProjectID == nil || *h.ProjectID != home.ID {
				p.t.Fatalf("mirror %+v -> %v", g, h.ProjectID)
			}
		}
		// Project-scoped list and revoke see only tokens that grant it.
		_, hash, _ := secret.NewToken()
		bare, err := p.st.CreateJoinToken(p.ctx, org, hash, nil, nil, owner.ID, p.at.Add(time.Hour), p.at)
		if err != nil {
			p.t.Fatal(err)
		}
		if err := p.st.RevokeJoinToken(p.ctx, org, &home.ID, bare.ID, p.at); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("project revoke of an org token %v", err)
		}
		scoped, _, err := p.st.ListJoinTokens(p.ctx, org, &other.ID, 50, nil)
		if err != nil || len(scoped) != 2 {
			p.t.Fatalf("project tokens %d %v", len(scoped), err)
		}
		// Creator demoted (review A7): the token stops working.
		if _, err := p.st.AddOrgMember(p.ctx, org, admin.ID, "admin", owner.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		_, hash2, _ := secret.NewToken()
		if _, err := p.st.CreateJoinToken(p.ctx, org, hash2, nil, nil, admin.ID, p.at.Add(time.Hour), p.at); err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.UpdateOrgMemberRole(p.ctx, org, admin.ID, "member"); err != nil {
			p.t.Fatal(err)
		}
		_, cred, _ := secret.NewToken()
		if _, err := p.st.ConsumeJoinToken(p.ctx, hash2, store.EnrollInput{Name: "x", NodeType: "pi"}, uuid.New(), cred, p.at); !errors.Is(err, store.ErrUnauthorized) {
			p.t.Fatalf("demoted creator %v", err)
		}
	})
}

func TestParityAccess(t *testing.T) {
	runParity(t, func(p parity) {
		owner := p.user("ao")
		org := p.org(owner.ID)
		home := p.project(org, owner.ID, "ahome")
		app := p.project(org, owner.ID, "aapp")
		host := p.enroll(org, &store.TokenGrant{ProjectID: home.ID, Level: "deploy"}, owner.ID, "a")
		if _, _, err := p.st.PutHostAccess(p.ctx, host.ID, app.ID, "deploy", owner.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		// A declared row and an owned row of app; reassign=org keeps declared
		// declared, turns owned into discovered, clears project and entry (C17).
		spec, hash, display := catalogSpec(p.t, "pnpm run dev")
		if _, err := p.st.CreateCatalogEntry(p.ctx, app.ID, store.CatalogWrite{Name: "aweb", Spec: spec, SpecHash: hash, CommandDisplay: display, Actor: owner.ID, At: p.at}); err != nil {
			p.t.Fatal(err)
		}
		if err := p.st.SetHostPrograms(p.ctx, host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, Projects: []string{home.Slug, app.Slug}}); err != nil {
			p.t.Fatal(err)
		}
		if err := p.st.UpsertDiscovered(p.ctx, host.ID, []store.Discovered{{Name: "x", Kind: "process", Owner: &store.DiscoveredOwner{Kind: "program", Project: app.Slug, Entry: "aweb"}}}, p.at, true, false); err != nil {
			p.t.Fatal(err)
		}
		owned := p.service(host.ID, "aweb")
		if owned.ProjectID == nil || *owned.ProjectID != app.ID || owned.Source != "owned" {
			p.t.Fatalf("owned %+v", owned)
		}
		if _, err := p.st.DeleteHostAccess(p.ctx, host.ID, app.ID, false); !errors.Is(err, store.ErrConflict) {
			p.t.Fatalf("conflict %v", err)
		}
		if _, err := p.st.DeleteHostAccess(p.ctx, host.ID, app.ID, true); err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.DeleteHostAccess(p.ctx, host.ID, app.ID, true); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("second delete %v", err)
		}
		got := p.service(host.ID, "aweb")
		if got.ProjectID != nil || got.Source != "discovered" || got.CatalogEntry != nil {
			p.t.Fatalf("reassigned %+v", got)
		}
		// Rediscovery does not claim the org-level row back (review A2).
		if _, _, err := p.st.PutHostAccess(p.ctx, host.ID, app.ID, "deploy", owner.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		if err := p.st.UpsertDiscovered(p.ctx, host.ID, []store.Discovered{{Name: "x", Kind: "process", Owner: &store.DiscoveredOwner{Kind: "program", Project: app.Slug, Entry: "aweb"}}}, p.at, true, false); err != nil {
			p.t.Fatal(err)
		}
		if got := p.service(host.ID, "aweb"); got.ProjectID != nil {
			p.t.Fatalf("org-level row claimed %+v", got)
		}
		conflicts, _, err := p.st.ListOrgAudit(p.ctx, org, 100, nil)
		if err != nil {
			p.t.Fatal(err)
		}
		found := false
		for _, a := range conflicts {
			found = found || a.Action == "service.owner_conflict"
		}
		if !found {
			p.t.Fatal("no owner_conflict audit")
		}
		// Metrics or cross-org owners place nothing (D25).
		stranger := p.user("as")
		away := p.project(p.org(stranger.ID), stranger.ID, "aaway")
		if _, err := p.st.CreateCatalogEntry(p.ctx, away.ID, store.CatalogWrite{Name: "xweb", Spec: spec, SpecHash: hash, CommandDisplay: display, Actor: stranger.ID, At: p.at}); err != nil {
			p.t.Fatal(err)
		}
		if _, _, err := p.st.PutHostAccess(p.ctx, host.ID, app.ID, "metrics", owner.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.CreateCatalogEntry(p.ctx, app.ID, store.CatalogWrite{Name: "mweb", Spec: spec, SpecHash: hash, CommandDisplay: display, Actor: owner.ID, At: p.at}); err != nil {
			p.t.Fatal(err)
		}
		if err := p.st.UpsertDiscovered(p.ctx, host.ID, []store.Discovered{
			{Name: "m1", Kind: "process", Owner: &store.DiscoveredOwner{Kind: "program", Project: app.Slug, Entry: "mweb"}},
			{Name: "x1", Kind: "process", Owner: &store.DiscoveredOwner{Kind: "program", Project: away.Slug, Entry: "xweb"}},
		}, p.at, true, false); err != nil {
			p.t.Fatal(err)
		}
		for _, n := range []string{"m1", "x1"} {
			if got := p.service(host.ID, n); got.ProjectID != nil || got.Source != "discovered" {
				p.t.Fatalf("%s placed %+v", n, got)
			}
		}
		// AutoActionTarget refuses a cross-org slug (D25).
		img, err := catalog.BuildImage("iweb", []byte(`{"kind":"service","image":"redis"}`), nil, nil)
		if err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.CreateCatalogEntry(p.ctx, away.ID, store.CatalogWrite{Name: "iweb", Kind: "image", ImageRaw: []byte(catalog.SpecJSON(img)), SpecHash: catalog.ImageHash(img, nil), CommandDisplay: img.Image, Actor: stranger.ID, At: p.at}); err != nil {
			p.t.Fatal(err)
		}
		if _, err := p.st.AutoActionTarget(p.ctx, host.ID, away.Slug, "iweb"); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("cross-org auto action target %v", err)
		}
		// No grant on a revoked host or in a deleted org (review D22).
		if err := p.st.RevokeHost(p.ctx, host.ID, p.at); err != nil {
			p.t.Fatal(err)
		}
		if _, _, err := p.st.PutHostAccess(p.ctx, host.ID, app.ID, "deploy", owner.ID, p.at); !errors.Is(err, store.ErrNotFound) {
			p.t.Fatalf("grant on a revoked host %v", err)
		}
	})
}

func TestParitySignInOrg(t *testing.T) {
	runParity(t, func(p parity) {
		u := p.user("si")
		if _, err := p.st.DeleteOrg(p.ctx, p.org(u.ID), p.at); err != nil {
			p.t.Fatal(err)
		}
		if orgs, _ := p.st.ListUserOrgs(p.ctx, u.ID); len(orgs) != 0 {
			p.t.Fatalf("orgs after delete %+v", orgs)
		}
		p.user("si") // signs in again (B16)
		orgs, err := p.st.ListUserOrgs(p.ctx, u.ID)
		if err != nil || len(orgs) != 1 || orgs[0].Role != "owner" {
			p.t.Fatalf("sign-in org %+v %v", orgs, err)
		}
		p.user("si")
		if orgs, _ := p.st.ListUserOrgs(p.ctx, u.ID); len(orgs) != 1 {
			p.t.Fatalf("second sign-in made another org %+v", orgs)
		}
	})
}

// TestParityOwnerConflictDedupe is review batch 2, item 30: an agent resends
// the same refused owned report every 300 s; it is audited once per
// audit.OwnerConflictWindow, and a different reported project is a new row.
func TestParityOwnerConflictDedupe(t *testing.T) {
	runParity(t, func(p parity) {
		owner := p.user("co")
		org := p.org(owner.ID)
		home := p.project(org, owner.ID, "chome")
		app := p.project(org, owner.ID, "capp")
		other := p.project(org, owner.ID, "coth")
		host := p.enroll(org, &store.TokenGrant{ProjectID: home.ID, Level: "deploy"}, owner.ID, "c")
		for _, pr := range []store.Project{app, other} {
			if _, _, err := p.st.PutHostAccess(p.ctx, host.ID, pr.ID, "deploy", owner.ID, p.at); err != nil {
				p.t.Fatal(err)
			}
		}
		if err := p.st.SetHostPrograms(p.ctx, host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, Projects: []string{home.Slug, app.Slug, other.Slug}}); err != nil {
			p.t.Fatal(err)
		}
		spec, hash, display := catalogSpec(p.t, "pnpm run dev")
		for _, pr := range []store.Project{app, other} {
			if _, err := p.st.CreateCatalogEntry(p.ctx, pr.ID, store.CatalogWrite{Name: "cweb", Spec: spec, SpecHash: hash, CommandDisplay: display, Actor: owner.ID, At: p.at}); err != nil {
				p.t.Fatal(err)
			}
		}
		// An org-level row named like the entry.
		if err := p.st.UpsertDiscovered(p.ctx, host.ID, []store.Discovered{{Name: "cweb", Kind: "process", Ports: []int{80}}}, p.at, true, false); err != nil {
			p.t.Fatal(err)
		}
		report := func(pr store.Project, at time.Time) {
			if err := p.st.UpsertDiscovered(p.ctx, host.ID, []store.Discovered{{Name: "x", Kind: "process", Owner: &store.DiscoveredOwner{Kind: "program", Project: pr.Slug, Entry: "cweb"}}}, at, true, false); err != nil {
				p.t.Fatal(err)
			}
		}
		conflicts := func() []string {
			rows, _, err := p.st.ListOrgAudit(p.ctx, org, 200, nil)
			if err != nil {
				p.t.Fatal(err)
			}
			var out []string
			for _, a := range rows {
				if a.Action == "service.owner_conflict" {
					var d struct{ Project string }
					_ = json.Unmarshal(a.Detail, &d)
					out = append(out, d.Project)
				}
			}
			return out
		}
		for i := 1; i <= 3; i++ {
			report(app, p.at.Add(time.Duration(i)*5*time.Minute))
		}
		if got := conflicts(); len(got) != 1 || got[0] != app.Slug {
			p.t.Fatalf("3 identical reports wrote %v", got)
		}
		report(other, p.at.Add(20*time.Minute))
		if got := conflicts(); len(got) != 2 {
			p.t.Fatalf("a different project wrote %v", got)
		}
		// After the window the same conflict is audited again.
		report(app, p.at.Add(25*time.Hour))
		if got := conflicts(); len(got) != 3 {
			p.t.Fatalf("after the window %v", got)
		}
		if got := p.service(host.ID, "cweb"); got.ProjectID != nil {
			p.t.Fatalf("org-level row claimed %+v", got)
		}
	})
}
