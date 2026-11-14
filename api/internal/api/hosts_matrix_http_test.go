package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// pass marks a cell where the caller gets past authorization. The answer
// may still be another error (the host is offline, so actions are 503), but
// never 403 or 404.
const pass = 0

// matrixCell checks one call against a cellWant value: pass, or an exact
// 403 or 404.
func matrixCell(t *testing.T, name, actor string, code int, env envelope, want int) {
	t.Helper()
	if want == pass {
		if code == http.StatusForbidden || code == http.StatusNotFound {
			t.Fatalf("%s %s: %d %s %q, want past authorization", name, actor, code, env.Code, env.Msg)
		}
		return
	}
	if code != want {
		t.Fatalf("%s %s: %d %s %q, want %d", name, actor, code, env.Code, env.Msg, want)
	}
}

// actionFixture adds catalog entries to the access fixture: a program and an
// image in home, deploy (deploy access) and metrics (metrics access), and a
// permissive v0.2.8 host policy, so authorization is the only gate a test
// sees before the offline host.
func actionFixture(t *testing.T) *accessFixture {
	t.Helper()
	fx := newAccessFixture(t)
	setPrograms(t, fx.w, fx.sc.host.ID, &store.HostPrograms{
		Enabled: true, RunAs: []string{"joe"}, Images: true, Docker: true, Files: true,
		Healthcheck: true, Logging: true, HealthPolicy: true, Secrets: true,
		Projects: []string{fx.home.Slug, fx.deploy.Slug, fx.metrics.Slug},
	})
	owner := fx.tok[actOwnerOwner]
	for _, p := range []store.Project{fx.home, fx.deploy, fx.metrics} {
		createProgram(t, fx.w, owner, p.ID, "job", "pnpm run dev", false)
		createImage(t, fx.w, owner, p.ID, "web", `{"kind":"service","image":"nginx"}`)
	}
	return fx
}

func actionBody(host, action, key string) string {
	body := map[string]any{"host_id": host}
	switch action {
	case "logs", "status":
	case "rollback":
		body["idempotency_key"] = key
		body["digest"] = "sha256:" + strings.Repeat("a", 64)
	default:
		body["idempotency_key"] = key
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

// TestHostActionMatrix is spec-organizations §3.2 for the catalog actions,
// snapshot and probe, for every actor (review D21). Deploy access is the
// gate for run and deploy; there is no approval step.
func TestHostActionMatrix(t *testing.T) {
	const no, miss = http.StatusForbidden, http.StatusNotFound
	fx := actionFixture(t)
	host := fx.sc.host.ID.String()
	type cell struct {
		project store.Project
		entry   string
		action  string
		want    map[string]int
	}
	// cellWant(home viewer, org admin+, metrics, deploy viewer, deploy admin, none)
	cells := []cell{
		// home's own entries: home has deploy access.
		{fx.home, "job", "run", cellWant(no, pass, miss, miss, miss, miss)},
		{fx.home, "job", "stop", cellWant(no, pass, miss, miss, miss, miss)},
		{fx.home, "job", "restart", cellWant(no, pass, miss, miss, miss, miss)},
		{fx.home, "job", "logs", cellWant(pass, pass, miss, miss, miss, miss)},
		{fx.home, "web", "deploy", cellWant(no, pass, miss, miss, miss, miss)},
		{fx.home, "web", "rollback", cellWant(no, pass, miss, miss, miss, miss)},
		{fx.home, "web", "remove", cellWant(no, pass, miss, miss, miss, miss)},
		{fx.home, "web", "logs", cellWant(pass, pass, miss, miss, miss, miss)},
		// deploy's entries: its admin runs them, its viewer only reads logs.
		{fx.deploy, "job", "run", cellWant(miss, pass, miss, no, pass, miss)},
		{fx.deploy, "web", "deploy", cellWant(miss, pass, miss, no, pass, miss)},
		{fx.deploy, "web", "logs", cellWant(miss, pass, miss, pass, pass, miss)},
		// metrics has metrics access only: nobody runs or deploys its entries
		// there, org admins included (they see the host, so 403).
		{fx.metrics, "job", "run", cellWant(miss, no, no, miss, miss, miss)},
		{fx.metrics, "web", "deploy", cellWant(miss, no, no, miss, miss, miss)},
	}
	for i, c := range cells {
		for _, actor := range accessActors {
			path := "/v1/projects/" + c.project.ID.String() + "/catalog/" + c.entry + "/" + c.action
			code, env := fx.do(actor, http.MethodPost, path, actionBody(host, c.action, uuid.NewString()))
			matrixCell(t, c.project.Slug+"/"+c.entry+"/"+c.action, actor, code, env, c.want[actor])
			_ = i
		}
	}
	for _, actor := range accessActors {
		code, env := fx.do(actor, http.MethodPost, fx.hostPath("/snapshot"), `{}`)
		matrixCell(t, "snapshot", actor, code, env, cellWant(pass, pass, pass, pass, pass, miss)[actor])
	}
	probe := `{"protocol":"tcp","port":8080}`
	for _, svc := range []struct {
		name string
		s    store.Service
		want map[string]int
	}{
		{"probe home service", fx.ownerSvc, cellWant(pass, pass, no, no, no, miss)},
		{"probe deploy service", fx.shareSvc, cellWant(no, pass, no, pass, pass, miss)},
		{"probe org-level service", fx.orgSvc, cellWant(no, pass, no, no, no, miss)},
	} {
		for _, actor := range accessActors {
			code, env := fx.do(actor, http.MethodPost, fx.servicePath(svc.s, "/probe"), probe)
			matrixCell(t, svc.name, actor, code, env, svc.want[actor])
		}
	}
}

// TestAccessRemovalByActor: org admin+, or an admin+ of the project leaving.
func TestAccessRemovalByActor(t *testing.T) {
	const ok, no, miss = http.StatusOK, http.StatusForbidden, http.StatusNotFound
	// In the fixture metrics_viewer created the metrics project, so it is
	// that project's owner and may leave too.
	want := map[string]int{
		actOwnerOwner: ok, actOwnerAdmin: ok, actMetricsAdmin: ok, actMetricsViewer: ok,
		actOwnerViewer: no, actDeployViewer: no, actDeployAdmin: no,
		actOrgMember: miss, actNone: miss,
	}
	for _, actor := range accessActors {
		fx := newAccessFixture(t)
		code, env := fx.do(actor, http.MethodDelete, fx.hostPath("/access/"+fx.metrics.ID.String()), "")
		if code != want[actor] {
			t.Fatalf("%s %d %s", actor, code, env.Code)
		}
	}
}

// TestOrgJoinTokensByActor: org admin+ only; an org member is 403; anyone
// outside the org is 404, a deploy-project owner too.
func TestOrgJoinTokensByActor(t *testing.T) {
	const no, miss = http.StatusForbidden, http.StatusNotFound
	fx := newAccessFixture(t)
	org := "/v1/orgs/" + fx.home.OrgID.String()
	code, env := fx.do(actOwnerOwner, http.MethodPost, org+"/join-tokens", `{}`)
	if code != http.StatusCreated {
		t.Fatalf("owner create %d", code)
	}
	tokenID := objectPayload(t, env)["id"].(string)
	want := func(actor string, admin int) int {
		switch actor {
		case actOwnerOwner, actOwnerAdmin:
			return admin
		case actOrgMember:
			return no
		default:
			return miss
		}
	}
	for _, actor := range append(append([]string{}, accessActors...), actDeployOwner) {
		code, _ := fx.do(actor, http.MethodPost, org+"/join-tokens", `{}`)
		if code != want(actor, http.StatusCreated) {
			t.Fatalf("create %s %d", actor, code)
		}
		code, _ = fx.do(actor, http.MethodGet, org+"/join-tokens", "")
		if code != want(actor, http.StatusOK) {
			t.Fatalf("list %s %d", actor, code)
		}
		if actor == actOwnerOwner || actor == actOwnerAdmin {
			continue
		}
		code, _ = fx.do(actor, http.MethodDelete, org+"/join-tokens/"+tokenID, "")
		if code != want(actor, http.StatusOK) {
			t.Fatalf("revoke %s %d", actor, code)
		}
	}
	code, env = fx.do(actOwnerAdmin, http.MethodDelete, org+"/join-tokens/"+tokenID, "")
	assertHTTP(t, code, env, http.StatusOK)
	// The deploy-project owner (not in the org) reaches its project but not
	// the org: project tokens are 403, org routes 404.
	code, env = fx.do(actDeployOwner, http.MethodPost, "/v1/projects/"+fx.deploy.ID.String()+"/join-tokens", `{}`)
	assertHTTP(t, code, env, http.StatusForbidden)
	for _, path := range []string{org, org + "/hosts", org + "/members"} {
		code, env = fx.do(actDeployOwner, http.MethodGet, path, "")
		assertHTTP(t, code, env, http.StatusNotFound)
	}
	// An org member may not list the org's hosts.
	code, env = fx.do(actOrgMember, http.MethodGet, org+"/hosts", "")
	assertHTTP(t, code, env, http.StatusForbidden)
	// A grant naming another org's project is 404.
	other := fx.w.user("other-org@ex.com", "Other")
	away, err := fx.w.mem.CreateProject(context.Background(), fx.w.mem.PersonalOrg(other.ID), other.ID, "Away", "away", fx.w.now)
	if err != nil {
		t.Fatal(err)
	}
	code, env = fx.do(actOwnerOwner, http.MethodPost, org+"/join-tokens", `{"grant":{"project_id":"`+away.ID.String()+`","level":"deploy"}}`)
	assertHTTP(t, code, env, http.StatusNotFound)
}

// TestOrgOwnerActions: member role change, owner demote refused, transfer,
// org delete (review D21).
func TestOrgOwnerActions(t *testing.T) {
	fx := newAccessFixture(t)
	org := "/v1/orgs/" + fx.home.OrgID.String()
	member := fx.users[actOrgMember].ID.String()
	code, env := fx.do(actOwnerAdmin, http.MethodPatch, org+"/members/"+member, `{"role":"admin"}`)
	assertHTTP(t, code, env, http.StatusOK)
	if objectPayload(t, env)["role"] != "admin" {
		t.Fatalf("role %#v", env.Payload)
	}
	code, env = fx.do(actOwnerAdmin, http.MethodPatch, org+"/members/"+fx.users[actOwnerOwner].ID.String(), `{"role":"member"}`)
	assertHTTP(t, code, env, http.StatusForbidden)
	code, env = fx.do(actOwnerOwner, http.MethodPost, org+"/transfer-ownership", `{"user_id":"`+fx.users[actOwnerAdmin].ID.String()+`"}`)
	assertHTTP(t, code, env, http.StatusOK)
	code, env = fx.do(actOwnerAdmin, http.MethodGet, org, "")
	if code != http.StatusOK || objectPayload(t, env)["role"] != "owner" {
		t.Fatalf("new owner %d %#v", code, env.Payload)
	}
	code, env = fx.do(actOwnerOwner, http.MethodDelete, org, "")
	assertHTTP(t, code, env, http.StatusForbidden)
	code, env = fx.do(actOwnerAdmin, http.MethodDelete, org, "")
	assertHTTP(t, code, env, http.StatusOK)
	h, err := fx.w.mem.GetHost(context.Background(), fx.sc.host.ID)
	if err != nil || h.RevokedAt == nil {
		t.Fatalf("org delete left the host live %v", err)
	}
	code, env = fx.do(actOwnerAdmin, http.MethodGet, org, "")
	assertHTTP(t, code, env, http.StatusNotFound)
}

// TestOrgAuditCoversEvents is review A3: rows written with only a project or
// a host get the org, so image deploy, program run, a member change and an
// invite show in GET /v1/orgs/{id}/audit.
func TestOrgAuditCoversEvents(t *testing.T) {
	fx := actionFixture(t)
	fx.w.srv.ConsoleOrigin = "http://127.0.0.1:5180"
	host := fx.sc.host.ID.String()
	owner := fx.tok[actOwnerOwner]
	home := "/v1/projects/" + fx.home.ID.String()
	fx.do(actOwnerOwner, http.MethodPost, home+"/catalog/web/deploy", actionBody(host, "deploy", "audit-deploy"))
	fx.do(actOwnerOwner, http.MethodPost, home+"/catalog/job/run", actionBody(host, "run", "audit-run"))
	code, env := fx.w.do(owner, http.MethodPost, home+"/members", `{"email":"outsider@ex.com","role":"viewer"}`)
	assertHTTP(t, code, env, http.StatusCreated)
	code, env = fx.w.do(owner, http.MethodPost, home+"/invites", `{"role":"viewer"}`)
	if code != http.StatusCreated {
		t.Fatalf("invite %d %s", code, env.Code)
	}
	code, env = fx.do(actOwnerOwner, http.MethodGet, "/v1/orgs/"+fx.home.OrgID.String()+"/audit?limit=200", "")
	assertHTTP(t, code, env, http.StatusOK)
	seen := map[string]bool{}
	for _, row := range listItems(t, env) {
		seen[row["action"].(string)] = true
	}
	for _, action := range []string{audit.ActionCatalogDeploy, audit.ActionProgramRun, audit.ActionMemberAdd, audit.ActionInviteCreate} {
		if !seen[action] {
			t.Fatalf("org audit lacks %s: %v", action, seen)
		}
	}
}

// TestDelegatedHostViews is review A5: a delegated (MCP) session never gets
// access rows or the projects allowlist, even as an org admin.
func TestDelegatedHostViews(t *testing.T) {
	fx := actionFixture(t)
	tok := tokenKind(t, fx.w, fx.users[actOwnerOwner].ID, store.KindDelegated)
	code, env := fx.w.do(tok, http.MethodGet, fx.hostPath(""), "")
	assertHTTP(t, code, env, http.StatusOK)
	got := objectPayload(t, env)
	if _, present := got["access"]; present {
		t.Fatal("delegated host view has access")
	}
	if got["programs"].(map[string]any)["projects"] != nil {
		t.Fatal("delegated host view has the allowlist")
	}
	code, env = fx.w.do(tok, http.MethodGet, "/v1/orgs/"+fx.home.OrgID.String()+"/hosts", "")
	assertHTTP(t, code, env, http.StatusOK)
	item := findItem(t, listItems(t, env), "id", fx.sc.host.ID.String())
	if _, present := item["access"]; present || item["programs"].(map[string]any)["projects"] != nil {
		t.Fatalf("delegated org host item %#v", item)
	}
	// The removed write routes are refused for delegated sessions (403).
	code, env = fx.w.do(tok, http.MethodPut, fx.hostPath("/shares/"+fx.metrics.ID.String()), `{"level":"deploy"}`)
	assertHTTP(t, code, env, http.StatusForbidden)
}

// TestHostProjectMirror is review A6: the deprecated project_id goes to org
// admins and members of that project only.
func TestHostProjectMirror(t *testing.T) {
	fx := newAccessFixture(t)
	for actor, want := range map[string]any{
		actOwnerOwner:    fx.home.ID.String(),
		actOwnerAdmin:    fx.home.ID.String(),
		actOwnerViewer:   fx.home.ID.String(),
		actMetricsViewer: nil,
		actDeployAdmin:   nil,
	} {
		code, env := fx.do(actor, http.MethodGet, fx.hostPath(""), "")
		assertHTTP(t, code, env, http.StatusOK)
		if got := objectPayload(t, env)["project_id"]; got != want {
			t.Fatalf("%s project_id %#v want %#v", actor, got, want)
		}
	}
	code, env := fx.do(actDeployAdmin, http.MethodGet, "/v1/projects/"+fx.deploy.ID.String()+"/hosts", "")
	assertHTTP(t, code, env, http.StatusOK)
	if item := findItem(t, listItems(t, env), "id", fx.sc.host.ID.String()); item["project_id"] != nil {
		t.Fatalf("project list mirror %#v", item["project_id"])
	}
}

// TestProgramsNeedAllowlistAgent is D-10 (review A1): a host that reports
// no programs.projects (agents before 0.2.8) runs programs only for its
// single deploy project; images are unaffected.
func TestProgramsNeedAllowlistAgent(t *testing.T) {
	const msg = "this host's agent does not enforce a projects allowlist; upgrade it to 0.2.8"
	fx := actionFixture(t)
	host := fx.sc.host.ID.String()
	old := &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, Images: true, Docker: true}
	setPrograms(t, fx.w, fx.sc.host.ID, old)
	run := func(p store.Project) (int, envelope) {
		return fx.do(actOwnerOwner, http.MethodPost, "/v1/projects/"+p.ID.String()+"/catalog/job/run", actionBody(host, "run", uuid.NewString()))
	}
	// Two deploy projects (home and deploy): both refused.
	for _, p := range []store.Project{fx.home, fx.deploy} {
		code, env := run(p)
		if code != http.StatusForbidden || env.Msg != msg {
			t.Fatalf("%s with two deploy projects: %d %q", p.Slug, code, env.Msg)
		}
	}
	// An image deploy is unaffected (offline host: past authorization).
	code, env := fx.do(actOwnerOwner, http.MethodPost, "/v1/projects/"+fx.home.ID.String()+"/catalog/web/deploy", actionBody(host, "deploy", uuid.NewString()))
	if code == http.StatusForbidden && env.Msg == msg {
		t.Fatalf("image refused %d %q", code, env.Msg)
	}
	// One deploy project: it runs, and another is refused (it has metrics now).
	if _, _, err := fx.w.grant(context.Background(), fx.sc.host.ID, fx.deploy.ID, "metrics", fx.users[actOwnerOwner].ID, fx.w.now); err != nil {
		t.Fatal(err)
	}
	if code, env := run(fx.home); code == http.StatusForbidden {
		t.Fatalf("single deploy project refused %d %q", code, env.Msg)
	}
	if code, _ := run(fx.deploy); code != http.StatusForbidden {
		t.Fatalf("metrics project ran %d", code)
	}
	// A v0.2.8 host that reports the list is unaffected.
	old.Projects = []string{fx.home.Slug, fx.deploy.Slug}
	setPrograms(t, fx.w, fx.sc.host.ID, old)
	if _, _, err := fx.w.grant(context.Background(), fx.sc.host.ID, fx.deploy.ID, "deploy", fx.users[actOwnerOwner].ID, fx.w.now); err != nil {
		t.Fatal(err)
	}
	for _, p := range []store.Project{fx.home, fx.deploy} {
		if code, env := run(p); code == http.StatusForbidden {
			t.Fatalf("%s on a v0.2.8 host refused %d %q", p.Slug, code, env.Msg)
		}
	}
}

// TestReassignKeepsDeclared is review C17 over HTTP: ?reassign=org turns an
// owned row into a discovered org-level row and leaves a declared row
// declared; both lose their project and catalog entry.
func TestReassignKeepsDeclared(t *testing.T) {
	fx := newAccessFixture(t)
	declared := fx.shareSvc // PutDeclared in the fixture, deploy's
	owned := fx.w.mem.PutDiscovered(fx.sc.host.ID, "owned-web", "process", nil, nil, fx.w.now)
	fx.w.mem.SetServiceProject(owned.ID, fx.deploy.ID)
	code, env := fx.do(actOwnerOwner, http.MethodDelete, fx.hostPath("/access/"+fx.deploy.ID.String()+"?reassign=org"), "")
	assertHTTP(t, code, env, http.StatusOK)
	for _, id := range []uuid.UUID{declared.ID, owned.ID} {
		svc, err := fx.w.mem.GetService(context.Background(), id, fx.w.now)
		if err != nil || svc.ProjectID != nil || svc.CatalogEntry != nil {
			t.Fatalf("reassigned %+v %v", svc, err)
		}
		if id == declared.ID && svc.Source != "declared" {
			t.Fatalf("declared became %s", svc.Source)
		}
	}
}
