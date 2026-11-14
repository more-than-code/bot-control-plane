package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Actors of spec-organizations §3.2 (api-v1 v1.24 item 2). The host is in
// the owner's org. home has deploy access (the enrolment grant), metrics has
// metrics access and deploy has deploy access; all three are in the org.
const (
	actOwnerViewer   = "home_viewer" // viewer of home (a deploy project)
	actOwnerAdmin    = "org_admin"   // org admin; also admin of home
	actOwnerOwner    = "org_owner"   // org owner; also owner of home
	actMetricsViewer = "metrics_viewer"
	actMetricsAdmin  = "metrics_admin"
	actDeployViewer  = "deploy_viewer"
	actDeployAdmin   = "deploy_admin"
	actOrgMember     = "org_member" // org member with no project role
	actNone          = "none"
	// actDeployOwner owns the deploy project but is not in the org; it is
	// not in accessActors.
	actDeployOwner = "deploy_owner"
)

var accessActors = []string{
	actOwnerViewer, actOwnerAdmin, actOwnerOwner,
	actMetricsViewer, actMetricsAdmin,
	actDeployViewer, actDeployAdmin, actOrgMember, actNone,
}

// cellWant maps the §3.2 columns to actors. ownerViewer is the viewer of a
// deploy project (home) looking at home's service; orgAdmin covers the org
// owner too; none covers org members without a project role.
func cellWant(ownerViewer, orgAdmin, metrics, deployViewer, deployAdmin, none int) map[string]int {
	return map[string]int{
		actOwnerViewer:   ownerViewer,
		actOwnerAdmin:    orgAdmin,
		actOwnerOwner:    orgAdmin,
		actMetricsViewer: metrics,
		actMetricsAdmin:  metrics,
		actDeployViewer:  deployViewer,
		actDeployAdmin:   deployAdmin,
		actOrgMember:     none,
		actNone:          none,
	}
}

type accessFixture struct {
	w                     *world
	sc                    scene
	home, metrics, deploy store.Project
	ownerSvc, shareSvc    store.Service
	orgSvc                store.Service
	tok                   map[string]string
	users                 map[string]store.User
}

func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	w := newWorld(t)
	ctx := context.Background()
	owner := w.user("owner@ex.com", "Owner")
	admin := w.user("admin@ex.com", "Admin")
	viewer := w.user("viewer@ex.com", "Viewer")
	spare := w.user("spare@ex.com", "Spare")
	extra := w.user("extra@ex.com", "Extra")
	metricsViewer := w.user("metrics-viewer@ex.com", "MetricsViewer")
	metricsAdmin := w.user("metrics-admin@ex.com", "MetricsAdmin")
	deployViewer := w.user("deploy-viewer@ex.com", "DeployViewer")
	deployOwner := w.user("deploy-owner@ex.com", "DeployOwner")
	deployAdmin := w.user("deploy-admin@ex.com", "DeployAdmin")
	orgMember := w.user("org-member@ex.com", "OrgMember")
	outsider := w.user("outsider@ex.com", "Outsider")
	sc := w.scene(owner, admin, viewer, spare, extra)
	org := sc.project.OrgID
	if _, err := w.mem.AddOrgMember(ctx, org, admin.ID, "admin", owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	if _, err := w.mem.AddOrgMember(ctx, org, orgMember.ID, "member", owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	metrics, err := w.mem.CreateProject(ctx, org, metricsViewer.ID, "Metrics", "metrics", w.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.mem.UpdateMemberRole(ctx, metrics.ID, metricsViewer.ID, "viewer"); err == nil {
		t.Fatal("owner demoted")
	}
	if _, err := w.mem.AddMember(ctx, metrics.ID, metricsAdmin.ID, "admin", metricsViewer.ID, w.now); err != nil {
		t.Fatal(err)
	}
	deploy, err := w.mem.CreateProject(ctx, org, deployOwner.ID, "Deploy", "deploy", w.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.mem.AddMember(ctx, deploy.ID, deployAdmin.ID, "admin", deployOwner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	if _, err := w.mem.AddMember(ctx, deploy.ID, deployViewer.ID, "viewer", deployOwner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.mem.PutHostAccess(ctx, sc.host.ID, metrics.ID, "metrics", owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.mem.PutHostAccess(ctx, sc.host.ID, deploy.ID, "deploy", owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	shareSvc := w.mem.PutDeclared(sc.host.ID, "web", "process", []int{8080}, w.now)
	w.mem.SetServiceProject(shareSvc.ID, deploy.ID)
	orgSvc := w.mem.PutDiscovered(sc.host.ID, "sshd", "process", []int{8080}, nil, w.now)
	w.mem.SetServiceProject(orgSvc.ID, uuid.Nil)
	samples := []store.Sample{
		procSample(w.now, 11, "api"),
		procSample(w.now, 22, "web"),
		procSample(w.now, 33, ""),
	}
	if _, err := w.mem.InsertBatch(ctx, sc.host.ID, uuid.New(), samples, w.now); err != nil {
		t.Fatal(err)
	}
	return &accessFixture{
		w: w, sc: sc, home: sc.project, metrics: metrics, deploy: deploy,
		ownerSvc: sc.svc, shareSvc: shareSvc, orgSvc: orgSvc,
		tok: map[string]string{
			actOwnerViewer:   w.token(viewer.ID),
			actOwnerAdmin:    w.token(admin.ID),
			actOwnerOwner:    w.token(owner.ID),
			actMetricsViewer: w.token(metricsViewer.ID),
			actMetricsAdmin:  w.token(metricsAdmin.ID),
			actDeployViewer:  w.token(deployViewer.ID),
			actDeployAdmin:   w.token(deployAdmin.ID),
			actOrgMember:     w.token(orgMember.ID),
			actNone:          w.token(outsider.ID),
			actDeployOwner:   w.token(deployOwner.ID),
		},
		users: map[string]store.User{
			actOwnerViewer: viewer, actOwnerAdmin: admin, actOwnerOwner: owner,
			actMetricsViewer: metricsViewer, actMetricsAdmin: metricsAdmin,
			actDeployViewer: deployViewer, actDeployAdmin: deployAdmin,
			actOrgMember: orgMember, actNone: outsider,
		},
	}
}

func (fx *accessFixture) do(actor, method, path, body string) (int, envelope) {
	return fx.w.do(fx.tok[actor], method, path, body)
}

func (fx *accessFixture) hostPath(suffix string) string {
	return "/v1/hosts/" + fx.sc.host.ID.String() + suffix
}

func (fx *accessFixture) servicePath(svc store.Service, suffix string) string {
	return "/v1/services/" + svc.ID.String() + suffix
}

const checksBody = `{"checks":[{"protocol":"tcp","port":8080,"interval_sec":30,"expect_status_min":200,"expect_status_max":299,"enabled":true}]}`

// TestHostPermissionMatrix is every cell of spec-organizations §3.2 on the
// real mux and memstore (acceptance 2). Revoke 200 is in TestOrgAdminRevokes.
func TestHostPermissionMatrix(t *testing.T) {
	const ok, no, miss = http.StatusOK, http.StatusForbidden, http.StatusNotFound
	cases := []struct {
		name   string
		method string
		path   func(fx *accessFixture) string
		body   string
		want   map[string]int
	}{
		{"see host", http.MethodGet, func(fx *accessFixture) string { return fx.hostPath("") }, "", cellWant(ok, ok, ok, ok, ok, miss)},
		{"metrics", http.MethodGet, func(fx *accessFixture) string { return fx.hostPath("/metrics?m=cpu") }, "", cellWant(ok, ok, ok, ok, ok, miss)},
		{"processes", http.MethodGet, func(fx *accessFixture) string { return fx.hostPath("/processes") }, "", cellWant(ok, ok, ok, ok, ok, miss)},
		{"services list", http.MethodGet, func(fx *accessFixture) string { return fx.hostPath("/services") }, "", cellWant(ok, ok, no, ok, ok, miss)},
		{"home service read", http.MethodGet, func(fx *accessFixture) string { return fx.servicePath(fx.ownerSvc, "") }, "", cellWant(ok, ok, no, no, no, miss)},
		{"deploy service read", http.MethodGet, func(fx *accessFixture) string { return fx.servicePath(fx.shareSvc, "") }, "", cellWant(no, ok, no, ok, ok, miss)},
		{"org-level service read", http.MethodGet, func(fx *accessFixture) string { return fx.servicePath(fx.orgSvc, "") }, "", cellWant(no, ok, no, no, no, miss)},
		{"health history", http.MethodGet, func(fx *accessFixture) string { return fx.servicePath(fx.shareSvc, "/health") }, "", cellWant(no, ok, no, ok, ok, miss)},
		{"home checks edit", http.MethodPut, func(fx *accessFixture) string { return fx.servicePath(fx.ownerSvc, "/health-checks") }, checksBody, cellWant(no, ok, no, no, no, miss)},
		{"deploy checks edit", http.MethodPut, func(fx *accessFixture) string { return fx.servicePath(fx.shareSvc, "/health-checks") }, checksBody, cellWant(no, ok, no, no, ok, miss)},
		{"org-level checks edit", http.MethodPut, func(fx *accessFixture) string { return fx.servicePath(fx.orgSvc, "/health-checks") }, checksBody, cellWant(no, ok, no, no, no, miss)},
		{"access list", http.MethodGet, func(fx *accessFixture) string { return fx.hostPath("/access") }, "", cellWant(no, ok, no, no, no, miss)},
		{"access grant", http.MethodPut, func(fx *accessFixture) string { return fx.hostPath("/access/" + fx.metrics.ID.String()) }, `{"level":"metrics"}`, cellWant(no, ok, no, no, no, miss)},
	}
	for _, tc := range cases {
		fx := newAccessFixture(t)
		for _, actor := range accessActors {
			code, env := fx.do(actor, tc.method, tc.path(fx), tc.body)
			if code != tc.want[actor] {
				t.Fatalf("%s %s: %d %s %s, want %d", tc.name, actor, code, env.Code, env.Msg, tc.want[actor])
			}
		}
	}
	// Revoke is org admin+; the denials leave the credential alone.
	fx := newAccessFixture(t)
	for _, actor := range accessActors {
		want := cellWant(no, ok, no, no, no, miss)[actor]
		if want == ok {
			continue
		}
		code, _ := fx.do(actor, http.MethodDelete, fx.hostPath("/credential"), "")
		if code != want {
			t.Fatalf("revoke %s %d want %d", actor, code, want)
		}
	}
}

// TestHostServiceListsByActor: org admins see every service including the
// org-level row; a deploy project sees its own; the full process list goes
// to everyone who sees the host.
func TestHostServiceListsByActor(t *testing.T) {
	fx := newAccessFixture(t)
	want := map[string][]string{
		actOwnerOwner:   {"api", "sshd", "web"},
		actOwnerAdmin:   {"api", "sshd", "web"},
		actOwnerViewer:  {"api"},
		actDeployViewer: {"web"},
		actDeployAdmin:  {"web"},
	}
	for actor, names := range want {
		code, env := fx.do(actor, http.MethodGet, fx.hostPath("/services"), "")
		if code != http.StatusOK {
			t.Fatalf("%s %d", actor, code)
		}
		got := serviceNameSet(t, env)
		if len(got) != len(names) {
			t.Fatalf("%s services %v", actor, got)
		}
		for _, n := range names {
			if !got[n] {
				t.Fatalf("%s missing %s in %v", actor, n, got)
			}
		}
	}
	for _, actor := range []string{actMetricsViewer, actOwnerViewer, actOwnerOwner} {
		code, env := fx.do(actor, http.MethodGet, fx.hostPath("/processes"), "")
		if code != http.StatusOK || len(listItems(t, env)) != 3 {
			t.Fatalf("%s processes %d", actor, code)
		}
	}
}

// TestHostViewsAllowlistAndAccess: programs.projects only for org admins
// (v1.24 item 1 amendment); access only for org admins; access_level per project.
func TestHostViewsAllowlistAndAccess(t *testing.T) {
	fx := newAccessFixture(t)
	ctx := context.Background()
	if err := fx.w.mem.SetHostPrograms(ctx, fx.sc.host.ID, &store.HostPrograms{
		Enabled: true, RunAs: []string{"joe"}, Projects: []string{"proj", "deploy"},
	}); err != nil {
		t.Fatal(err)
	}
	get := func(actor string) map[string]any {
		t.Helper()
		code, env := fx.do(actor, http.MethodGet, fx.hostPath(""), "")
		if code != http.StatusOK {
			t.Fatalf("%s %d", actor, code)
		}
		return objectPayload(t, env)
	}
	admin := get(actOwnerAdmin)
	programs := admin["programs"].(map[string]any)
	if list, ok := programs["projects"].([]any); !ok || len(list) != 2 {
		t.Fatalf("org admin allowlist %#v", programs["projects"])
	}
	if access, ok := admin["access"].([]any); !ok || len(access) != 3 {
		t.Fatalf("org admin access %#v", admin["access"])
	}
	if admin["org_id"] != fx.home.OrgID.String() {
		t.Fatalf("org_id %#v", admin["org_id"])
	}
	for _, actor := range []string{actMetricsViewer, actDeployAdmin, actOwnerViewer} {
		got := get(actor)
		programs := got["programs"].(map[string]any)
		if programs["projects"] != nil {
			t.Fatalf("%s sees the allowlist %#v", actor, programs["projects"])
		}
		if _, present := got["access"]; present {
			t.Fatalf("%s sees access", actor)
		}
	}
	metrics := get(actMetricsViewer)
	if metrics["access_level"] != "metrics" {
		t.Fatalf("metrics access_level %#v", metrics["access_level"])
	}
	if listed := metrics["programs"].(map[string]any)["project_listed"]; listed != false {
		t.Fatalf("metrics project_listed %#v", listed)
	}
	deploy := get(actDeployAdmin)
	if deploy["access_level"] != "deploy" {
		t.Fatalf("deploy access_level %#v", deploy["access_level"])
	}
	if listed := deploy["programs"].(map[string]any)["project_listed"]; listed != true {
		t.Fatalf("deploy project_listed %#v", listed)
	}
	// Project host list: access_level of that project; allowlist hidden
	// from a project admin who is not an org admin.
	code, env := fx.do(actDeployAdmin, http.MethodGet, "/v1/projects/"+fx.deploy.ID.String()+"/hosts", "")
	if code != http.StatusOK {
		t.Fatal(code)
	}
	item := findItem(t, listItems(t, env), "id", fx.sc.host.ID.String())
	if item["access_level"] != "deploy" || item["programs"].(map[string]any)["projects"] != nil {
		t.Fatalf("project list item %#v", item)
	}
	code, env = fx.do(actOwnerAdmin, http.MethodGet, "/v1/orgs/"+fx.home.OrgID.String()+"/hosts", "")
	if code != http.StatusOK {
		t.Fatal(code)
	}
	item = findItem(t, listItems(t, env), "id", fx.sc.host.ID.String())
	if item["access_level"] != nil || item["access"] == nil {
		t.Fatalf("org list item %#v", item)
	}
}

// TestAccessWritesAndLeave covers acceptance 3 for access: grant, update,
// 409 while services remain, ?reassign=org, a project admin leaving, and
// a project outside the org.
func TestAccessWritesAndLeave(t *testing.T) {
	fx := newAccessFixture(t)
	ctx := context.Background()
	other := fx.w.user("away@ex.com", "Away")
	away, err := fx.w.mem.CreateProject(ctx, fx.w.mem.PersonalOrg(other.ID), other.ID, "Away", "away", fx.w.now)
	if err != nil {
		t.Fatal(err)
	}
	code, env := fx.do(actOwnerAdmin, http.MethodPut, fx.hostPath("/access/"+away.ID.String()), `{"level":"deploy"}`)
	assertHTTP(t, code, env, http.StatusNotFound)
	code, env = fx.do(actOwnerAdmin, http.MethodPut, fx.hostPath("/access/"+fx.metrics.ID.String()), `{"level":"owner"}`)
	assertHTTP(t, code, env, http.StatusBadRequest)
	code, env = fx.do(actOwnerAdmin, http.MethodPut, fx.hostPath("/access/"+fx.metrics.ID.String()), `{"level":"deploy"}`)
	assertHTTP(t, code, env, http.StatusOK)
	if objectPayload(t, env)["level"] != "deploy" {
		t.Fatalf("update %#v", env.Payload)
	}
	// The deploy project still has a service on the host.
	path := fx.hostPath("/access/" + fx.deploy.ID.String())
	code, env = fx.do(actDeployViewer, http.MethodDelete, path, "")
	assertHTTP(t, code, env, http.StatusForbidden)
	code, env = fx.do(actDeployAdmin, http.MethodDelete, path, "")
	assertHTTP(t, code, env, http.StatusConflict)
	code, env = fx.do(actDeployAdmin, http.MethodDelete, path+"?reassign=owner", "")
	assertHTTP(t, code, env, http.StatusBadRequest)
	code, env = fx.do(actDeployAdmin, http.MethodDelete, path+"?reassign=org", "")
	assertHTTP(t, code, env, http.StatusOK)
	svc, err := fx.w.mem.GetService(ctx, fx.shareSvc.ID, fx.w.now)
	if err != nil || svc.ProjectID != nil {
		t.Fatalf("reassigned service %+v %v", svc.ProjectID, err)
	}
	code, _ = fx.do(actDeployAdmin, http.MethodGet, fx.hostPath(""), "")
	if code != http.StatusNotFound {
		t.Fatalf("left project still sees the host %d", code)
	}
	code, env = fx.do(actOwnerAdmin, http.MethodGet, fx.hostPath("/access"), "")
	assertHTTP(t, code, env, http.StatusOK)
	if items := listItems(t, env); len(items) != 2 {
		t.Fatalf("access rows %v", items)
	}
	var actions []string
	for _, row := range fx.w.mem.AllAudits() {
		if row.HostID != nil && *row.HostID == fx.sc.host.ID {
			if row.OrgID == nil || *row.OrgID != fx.home.OrgID {
				t.Fatalf("host audit without org %s", row.Action)
			}
			if row.Decision == audit.DecisionAllowed {
				actions = append(actions, row.Action)
			}
		}
	}
	want := []string{audit.ActionHostAccessUpdate, audit.ActionHostAccessRevoke}
	if len(actions) != len(want) || actions[0] != want[0] || actions[1] != want[1] {
		t.Fatalf("audit actions %v", actions)
	}
}

func TestOrgAdminRevokes(t *testing.T) {
	fx := newAccessFixture(t)
	code, env := fx.do(actOwnerAdmin, http.MethodDelete, fx.hostPath("/credential"), "")
	assertHTTP(t, code, env, http.StatusOK)
	h, err := fx.w.mem.GetHost(context.Background(), fx.sc.host.ID)
	if err != nil || h.RevokedAt == nil {
		t.Fatalf("not revoked %v", err)
	}
}

// TestSharesAndApprovalsGone: the removed routes answer 410 (v1.24 items 3, 4).
func TestSharesAndApprovalsGone(t *testing.T) {
	fx := newAccessFixture(t)
	paths := []struct{ method, path string }{
		{http.MethodGet, fx.hostPath("/shares")},
		{http.MethodGet, fx.hostPath("/shares/" + fx.metrics.ID.String())},
		{http.MethodPut, fx.hostPath("/shares/" + fx.metrics.ID.String())},
		{http.MethodDelete, fx.hostPath("/shares/" + fx.metrics.ID.String())},
		{http.MethodGet, fx.hostPath("/catalog-approvals")},
		{http.MethodPost, fx.hostPath("/catalog-approvals")},
		{http.MethodDelete, fx.hostPath("/catalog-approvals/" + uuid.NewString())},
	}
	for _, p := range paths {
		code, env := fx.do(actOwnerOwner, p.method, p.path, `{}`)
		if code != http.StatusGone || env.Code != "gone" {
			t.Fatalf("%s %s %d %s", p.method, p.path, code, env.Code)
		}
	}
	// A delegated (MCP) session: the reads are delegatedAllow and reach the
	// 410 handler; the writes are delegatedDeny and are refused (403) before it.
	tok := tokenKind(t, fx.w, fx.users[actOwnerOwner].ID, store.KindDelegated)
	for _, p := range paths {
		code, env := fx.w.do(tok, p.method, p.path, `{}`)
		want, wantCode := http.StatusForbidden, "forbidden"
		if p.method == http.MethodGet {
			want, wantCode = http.StatusGone, "gone"
		}
		if code != want || env.Code != wantCode {
			t.Fatalf("delegated %s %s %d %s, want %d %s", p.method, p.path, code, env.Code, want, wantCode)
		}
	}
}

func assertHTTP(t *testing.T, status int, env envelope, want int) {
	t.Helper()
	wantCode := map[int]string{
		http.StatusOK:         "ok",
		http.StatusCreated:    "ok",
		http.StatusAccepted:   "ok",
		http.StatusBadRequest: "bad_request",
		http.StatusForbidden:  "forbidden",
		http.StatusNotFound:   "not_found",
		http.StatusConflict:   "conflict",
	}[want]
	if status != want || env.Code != wantCode {
		t.Fatalf("status %d code %s msg %s, want %d %s", status, env.Code, env.Msg, want, wantCode)
	}
}

func listItems(t *testing.T, env envelope) []map[string]any {
	t.Helper()
	payload := objectPayload(t, env)
	raw, _ := payload["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("item %T", item)
		}
		out = append(out, m)
	}
	return out
}

func objectPayload(t *testing.T, env envelope) map[string]any {
	t.Helper()
	m, ok := env.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload %T", env.Payload)
	}
	return m
}

func findItem(t *testing.T, items []map[string]any, key, value string) map[string]any {
	t.Helper()
	for _, item := range items {
		if item[key] == value {
			return item
		}
	}
	t.Fatalf("no item %s=%s in %d", key, value, len(items))
	return nil
}

func namesOf(items []map[string]any) []string {
	out := make([]string, len(items))
	for i, item := range items {
		name, _ := item["name"].(string)
		id, _ := item["id"].(string)
		out[i] = name + ":" + id
	}
	return out
}

func procSample(at time.Time, pid int, service string) store.Sample {
	tags := map[string]string{
		"pid": strconv.Itoa(pid), "process_name": "p", "user": "root", "cmdline": "p",
	}
	if service != "" {
		tags["service"] = service
	}
	return store.Sample{
		M:      "process",
		T:      at,
		Tags:   tags,
		Floats: map[string]float64{"cpu_usage": 1, "memory_usage": 1},
		Ints:   map[string]int64{"memory_rss": 1, "num_threads": 1},
	}
}

func enrollHost(t *testing.T, w *world, project store.Project, ownerID uuid.UUID, name string) store.Host {
	t.Helper()
	ctx := context.Background()
	_, hash, err := secret.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.mem.CreateJoinToken(ctx, project.OrgID, hash, nil, &store.TokenGrant{ProjectID: project.ID, Level: "deploy"}, ownerID, w.now.Add(time.Hour), w.now); err != nil {
		t.Fatal(err)
	}
	_, credHash, err := secret.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	host, err := w.mem.ConsumeJoinToken(ctx, hash, store.EnrollInput{
		Name: name, NodeType: "pi", OS: "linux", Arch: "arm64", AlwaysOn: true, AgentVersion: "0.1.0",
	}, uuid.New(), credHash, w.now)
	if err != nil {
		t.Fatal(err)
	}
	if host.Name != name {
		t.Fatalf("name %s", host.Name)
	}
	return host
}

func TestHostCursorCodec(t *testing.T) {
	id := uuid.New().String()
	old := encodeCursor(&store.Cursor{At: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC), ID: id})
	if _, err := decodeHostCursor(*old); err == nil {
		t.Fatal("time cursor decoded as a host cursor")
	}
	rank := 0
	next := encodeCursor(&store.Cursor{Rank: &rank, Name: "a-owned", ID: id})
	got, err := decodeHostCursor(*next)
	if err != nil || got.Rank == nil || *got.Rank != 0 || got.Name != "a-owned" || got.ID != id {
		t.Fatalf("%+v %v", got, err)
	}
	partial, err := json.Marshal(map[string]any{"rel_rank": 0, "id": id})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeHostCursor(base64.RawURLEncoding.EncodeToString(partial)); err == nil {
		t.Fatal("partial cursor decoded")
	}
}

func nextCursor(t *testing.T, env envelope) (string, bool) {
	t.Helper()
	raw, ok := objectPayload(t, env)["next_cursor"]
	if !ok || raw == nil {
		return "", false
	}
	s, ok := raw.(string)
	if !ok || s == "" {
		t.Fatalf("cursor %#v", raw)
	}
	return s, true
}

func serviceNameSet(t *testing.T, env envelope) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, item := range listItems(t, env) {
		out[item["name"].(string)] = true
	}
	return out
}
