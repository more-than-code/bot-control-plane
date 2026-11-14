package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// TestOrgRoutes covers the org object, members, projects and the org-level
// project rule (D-1, D-2).
func TestOrgRoutes(t *testing.T) {
	fx := newAccessFixture(t)
	org := "/v1/orgs/" + fx.home.OrgID.String()
	code, env := fx.do(actOrgMember, http.MethodGet, org, "")
	assertHTTP(t, code, env, http.StatusOK)
	if objectPayload(t, env)["role"] != "member" {
		t.Fatalf("role %#v", env.Payload)
	}
	code, env = fx.do(actNone, http.MethodGet, org, "")
	assertHTTP(t, code, env, http.StatusNotFound)
	code, env = fx.do(actOrgMember, http.MethodPatch, org, `{"name":"X"}`)
	assertHTTP(t, code, env, http.StatusForbidden)
	code, env = fx.do(actOwnerAdmin, http.MethodPatch, org, `{"name":"More Than Code"}`)
	assertHTTP(t, code, env, http.StatusOK)
	// Org member sees no project; org admin reaches every project as admin via org.
	code, env = fx.do(actOrgMember, http.MethodGet, org+"/projects", "")
	assertHTTP(t, code, env, http.StatusOK)
	if len(listItems(t, env)) != 0 {
		t.Fatal("org member reaches projects")
	}
	code, env = fx.do(actOwnerAdmin, http.MethodGet, org+"/projects", "")
	assertHTTP(t, code, env, http.StatusOK)
	viaOrg := findItem(t, listItems(t, env), "id", fx.deploy.ID.String())
	if viaOrg["role"] != "admin" || viaOrg["via"] != "org" {
		t.Fatalf("org reach %#v", viaOrg)
	}
	// /v1/me lists orgs and project via.
	code, env = fx.do(actOwnerAdmin, http.MethodGet, "/v1/me", "")
	assertHTTP(t, code, env, http.StatusOK)
	me := objectPayload(t, env)
	if orgs, _ := me["orgs"].([]any); len(orgs) != 2 {
		t.Fatalf("me orgs %#v", me["orgs"])
	}
	// Org admin acts as project admin: add a member to a project they are not in.
	code, env = fx.do(actOwnerAdmin, http.MethodPost, "/v1/projects/"+fx.deploy.ID.String()+"/members", `{"email":"outsider@ex.com","role":"viewer"}`)
	assertHTTP(t, code, env, http.StatusCreated)
	// The outside collaborator now reaches deploy as a member, without being in the org.
	code, env = fx.do(actNone, http.MethodGet, "/v1/projects/"+fx.deploy.ID.String(), "")
	assertHTTP(t, code, env, http.StatusOK)
	if p := objectPayload(t, env); p["role"] != "viewer" || p["via"] != "member" || p["org_id"] != fx.home.OrgID.String() {
		t.Fatalf("collaborator %#v", p)
	}
	// Only the org owner (or the project owner) renames a project it is not in.
	code, env = fx.do(actOwnerAdmin, http.MethodPatch, "/v1/projects/"+fx.deploy.ID.String(), `{"name":"D"}`)
	assertHTTP(t, code, env, http.StatusForbidden)
	code, env = fx.do(actOwnerOwner, http.MethodPatch, "/v1/projects/"+fx.deploy.ID.String(), `{"name":"D"}`)
	assertHTTP(t, code, env, http.StatusOK)
	// Members: owner cannot be removed; self-removal works.
	ownerID := fx.users[actOwnerOwner].ID.String()
	code, env = fx.do(actOwnerAdmin, http.MethodDelete, org+"/members/"+ownerID, "")
	assertHTTP(t, code, env, http.StatusForbidden)
	code, env = fx.do(actOrgMember, http.MethodDelete, org+"/members/"+fx.users[actOrgMember].ID.String(), "")
	assertHTTP(t, code, env, http.StatusOK)
	code, env = fx.do(actOwnerAdmin, http.MethodPost, org+"/members", `{"email":"metrics-admin@ex.com","role":"owner"}`)
	assertHTTP(t, code, env, http.StatusBadRequest)
	code, env = fx.do(actOwnerAdmin, http.MethodPost, org+"/members", `{"email":"metrics-admin@ex.com","role":"member"}`)
	assertHTTP(t, code, env, http.StatusCreated)
	code, env = fx.do(actOwnerAdmin, http.MethodGet, org+"/members", "")
	assertHTTP(t, code, env, http.StatusOK)
	if len(listItems(t, env)) != 3 {
		t.Fatalf("members %v", listItems(t, env))
	}
	// Transfer: owner only.
	code, env = fx.do(actOwnerAdmin, http.MethodPost, org+"/transfer-ownership", `{"user_id":"`+fx.users[actOwnerAdmin].ID.String()+`"}`)
	assertHTTP(t, code, env, http.StatusForbidden)
	// Create a project in the org; POST /v1/projects needs one admin org.
	code, env = fx.do(actOwnerAdmin, http.MethodPost, org+"/projects", `{"name":"New"}`)
	assertHTTP(t, code, env, http.StatusCreated)
	if p := objectPayload(t, env); p["org_id"] != fx.home.OrgID.String() || p["role"] != "owner" {
		t.Fatalf("created %#v", p)
	}
	code, env = fx.do(actOwnerAdmin, http.MethodPost, "/v1/projects", `{"name":"Which"}`)
	if code != http.StatusBadRequest || env.Msg != "choose an organization" {
		t.Fatalf("two admin orgs %d %s", code, env.Msg)
	}
	code, env = fx.do(actNone, http.MethodPost, "/v1/projects", `{"name":"Mine"}`)
	assertHTTP(t, code, env, http.StatusCreated)
	// Org audit lists the org's rows only for admins.
	code, env = fx.do(actOwnerAdmin, http.MethodGet, org+"/audit", "")
	assertHTTP(t, code, env, http.StatusOK)
	if len(listItems(t, env)) == 0 {
		t.Fatal("no org audit rows")
	}
	code, env = fx.do(actMetricsViewer, http.MethodGet, org+"/audit", "")
	assertHTTP(t, code, env, http.StatusNotFound)
}

// TestOrgJoinTokenGrant is acceptance 3: an org token with a grant enrols a
// host with deploy access for that project; the project route needs org admin+.
func TestOrgJoinTokenGrant(t *testing.T) {
	fx := newAccessFixture(t)
	org := "/v1/orgs/" + fx.home.OrgID.String()
	body := `{"grant":{"project_id":"` + fx.metrics.ID.String() + `","level":"deploy"}}`
	code, env := fx.do(actMetricsAdmin, http.MethodPost, org+"/join-tokens", body)
	assertHTTP(t, code, env, http.StatusNotFound)
	code, env = fx.do(actOwnerAdmin, http.MethodPost, org+"/join-tokens", `{"grant":{"project_id":"`+fx.metrics.ID.String()+`","level":"owner"}}`)
	assertHTTP(t, code, env, http.StatusBadRequest)
	code, env = fx.do(actOwnerAdmin, http.MethodPost, org+"/join-tokens", body)
	if code != http.StatusCreated {
		t.Fatalf("create %d %s", code, env.Code)
	}
	tok := objectPayload(t, env)
	grant := tok["grant"].(map[string]any)
	if grant["project_id"] != fx.metrics.ID.String() || grant["level"] != "deploy" {
		t.Fatalf("grant %#v", grant)
	}
	host, err := fx.w.mem.ConsumeJoinToken(context.Background(), secret.Hash(tok["token"].(string)), store.EnrollInput{
		Name: "pi4", NodeType: "pi", OS: "linux", Arch: "arm64", AlwaysOn: true, AgentVersion: "0.2.8",
	}, uuid.New(), secret.Hash("cred"), fx.w.now)
	if err != nil {
		t.Fatal(err)
	}
	if host.Name != "pi4-2" || host.OrgID != fx.home.OrgID {
		t.Fatalf("host %+v", host)
	}
	code, env = fx.do(actMetricsViewer, http.MethodGet, "/v1/hosts/"+host.ID.String(), "")
	assertHTTP(t, code, env, http.StatusOK)
	if objectPayload(t, env)["access_level"] != "deploy" {
		t.Fatalf("enrolled access %#v", env.Payload)
	}
	// Project-scoped shorthand: home admin who is not an org admin is refused.
	homeAdmin := fx.w.user("home-admin@ex.com", "HomeAdmin")
	if _, err := fx.w.mem.AddMember(context.Background(), fx.home.ID, homeAdmin.ID, "admin", fx.users[actOwnerOwner].ID, fx.w.now); err != nil {
		t.Fatal(err)
	}
	code, env = fx.w.do(fx.w.token(homeAdmin.ID), http.MethodPost, "/v1/projects/"+fx.home.ID.String()+"/join-tokens", `{}`)
	assertHTTP(t, code, env, http.StatusForbidden)
	code, env = fx.do(actOwnerAdmin, http.MethodPost, "/v1/projects/"+fx.deploy.ID.String()+"/join-tokens", `{}`)
	if code != http.StatusCreated || objectPayload(t, env)["grant"].(map[string]any)["project_id"] != fx.deploy.ID.String() {
		t.Fatalf("project shorthand %d %#v", code, env.Payload)
	}
	code, env = fx.do(actOwnerAdmin, http.MethodGet, "/v1/projects/"+fx.deploy.ID.String()+"/join-tokens", "")
	assertHTTP(t, code, env, http.StatusOK)
	if len(listItems(t, env)) != 1 {
		t.Fatalf("project tokens %v", listItems(t, env))
	}
	code, env = fx.do(actOwnerAdmin, http.MethodGet, org+"/join-tokens", "")
	assertHTTP(t, code, env, http.StatusOK)
	if len(listItems(t, env)) != 4 {
		t.Fatalf("org tokens %d", len(listItems(t, env)))
	}
}

// TestProjectDeleteKeepsHosts is v1.24 item 7 over HTTP.
func TestProjectDeleteKeepsHosts(t *testing.T) {
	fx := newAccessFixture(t)
	code, env := fx.do(actOwnerOwner, http.MethodDelete, "/v1/projects/"+fx.deploy.ID.String(), "")
	assertHTTP(t, code, env, http.StatusOK)
	h, err := fx.w.mem.GetHost(context.Background(), fx.sc.host.ID)
	if err != nil || h.RevokedAt != nil {
		t.Fatalf("host revoked %v", err)
	}
	svc, err := fx.w.mem.GetService(context.Background(), fx.shareSvc.ID, fx.w.now)
	if err != nil || svc.GoneAt == nil {
		t.Fatalf("service not gone %v", err)
	}
	code, env = fx.do(actOwnerAdmin, http.MethodGet, fx.hostPath("/access"), "")
	assertHTTP(t, code, env, http.StatusOK)
	for _, item := range listItems(t, env) {
		if item["project_id"] == fx.deploy.ID.String() {
			t.Fatal("deleted project keeps access")
		}
	}
}

// TestDelegatedOrgRoutes is v1.24 item 10: delegated sessions are refused on
// every org write and the console-only reads; four reads are allowed.
func TestDelegatedOrgRoutes(t *testing.T) {
	fx := newAccessFixture(t)
	tok := tokenKind(t, fx.w, fx.users[actOwnerOwner].ID, store.KindDelegated)
	org := "/v1/orgs/" + fx.home.OrgID.String()
	allowed := []string{"/v1/me", org, org + "/projects", org + "/hosts"}
	for _, path := range allowed {
		code, env := fx.w.do(tok, http.MethodGet, path, "")
		if code != http.StatusOK {
			t.Fatalf("delegated GET %s %d %s", path, code, env.Code)
		}
	}
	refused := []struct{ method, path, body string }{
		{http.MethodGet, org + "/members", ""},
		{http.MethodGet, org + "/join-tokens", ""},
		{http.MethodGet, org + "/audit", ""},
		{http.MethodGet, fx.hostPath("/access"), ""},
		{http.MethodGet, "/v1/projects/" + fx.home.ID.String() + "/join-tokens", ""},
		{http.MethodPost, "/v1/orgs", `{"name":"X"}`},
		{http.MethodPatch, org, `{"name":"X"}`},
		{http.MethodDelete, org, ""},
		{http.MethodPost, org + "/members", `{"email":"outsider@ex.com","role":"member"}`},
		{http.MethodPost, org + "/transfer-ownership", `{}`},
		{http.MethodPost, org + "/projects", `{"name":"X"}`},
		{http.MethodPost, org + "/join-tokens", `{}`},
		{http.MethodPut, fx.hostPath("/access/" + fx.metrics.ID.String()), `{"level":"deploy"}`},
		{http.MethodDelete, fx.hostPath("/access/" + fx.metrics.ID.String()), ""},
	}
	for _, c := range refused {
		code, env := fx.w.do(tok, c.method, c.path, c.body)
		if code != http.StatusForbidden || env.Code != "forbidden" {
			t.Fatalf("delegated %s %s %d %s", c.method, c.path, code, env.Code)
		}
	}
}
