package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// helloNets is allowImages with shared_networks set to raw JSON (or left out
// when raw is "").
func helloNets(raw string) map[string]any {
	p := allowImages()
	if raw != "" {
		p["shared_networks"] = json.RawMessage(raw)
	}
	return p
}

// TestNetworksHelloEndToEnd sends real hellos through the agent socket and
// reads the org admin view (api-v1 v1.25 item 2, agent-protocol v1.13).
func TestNetworksHelloEndToEnd(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	session := w.token(owner.ID)
	orgPath := "/v1/orgs/" + sc.project.OrgID.String() + "/hosts"
	hello := func(raw string) {
		t.Helper()
		helloAgent(t, w, sc.host, sc.credPlain, sc.credID, helloNets(raw), nil)
	}
	stored := func() *store.HostPrograms {
		t.Helper()
		h, err := w.mem.GetHost(context.Background(), sc.host.ID)
		if err != nil {
			t.Fatal(err)
		}
		return h.Programs
	}
	disabled := func(label string) {
		t.Helper()
		p := stored()
		if p == nil || p.Enabled || p.SharedNetworks != nil {
			t.Fatalf("%s: stored %#v", label, p)
		}
		if got := sharedNetworksJSON(t, w, session, orgPath, sc.host.ID); got != "null" {
			t.Fatalf("%s: org view %s", label, got)
		}
	}

	member := func(i int) string { return fmt.Sprintf(`"p%d"`, i) }
	members := func(n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = member(i)
		}
		return strings.Join(out, ",")
	}
	networks := func(n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf(`{"name":"n%d","subnet":"10.200.%d.0/24","projects":[]}`, i, i)
		}
		return "[" + strings.Join(out, ",") + "]"
	}
	bad := map[string]string{
		"projects string":   `[{"name":"dd","subnet":"10.200.10.0/24","projects":"proj"}]`,
		"non-string member": `[{"name":"dd","subnet":"10.200.10.0/24","projects":["proj",7]}]`,
		"missing name":      `[{"subnet":"10.200.10.0/24","projects":[]}]`,
		"numeric name":      `[{"name":5,"subnet":"10.200.10.0/24","projects":[]}]`,
		"0.0.0.0/0":         `[{"name":"dd","subnet":"0.0.0.0/0","projects":[]}]`,
		"10.0.0.0/8":        `[{"name":"dd","subnet":"10.0.0.0/8","projects":[]}]`,
		"100.64.0.0/16":     `[{"name":"dd","subnet":"100.64.0.0/16","projects":[]}]`,
		"192.168.0.0/29":    `[{"name":"dd","subnet":"192.168.0.0/29","projects":[]}]`,
		"nine networks":     networks(9),
		"33 members":        `[{"name":"dd","subnet":"10.200.10.0/24","projects":[` + members(33) + `]}]`,
	}
	for label, raw := range bad {
		hello(raw)
		disabled(label)
	}

	valid := `[{"name":"dd","subnet":"10.200.10.0/24","projects":["proj","other"]},{"name":"lone","subnet":"192.168.40.16/28","projects":null}]`
	hello(valid)
	if p := stored(); p == nil || !p.Enabled || len(p.SharedNetworks) != 2 {
		t.Fatalf("valid stored %#v", p)
	}
	want := `[{"name":"dd","projects":["proj","other"],"subnet":"10.200.10.0/24"},{"name":"lone","projects":[],"subnet":"192.168.40.16/28"}]`
	if got := sharedNetworksJSON(t, w, session, orgPath, sc.host.ID); got != want {
		t.Fatalf("org view %s", got)
	}

	// Accepted boundaries: exactly 8 networks, exactly 32 members.
	hello(networks(8))
	if p := stored(); p == nil || !p.Enabled || len(p.SharedNetworks) != 8 {
		t.Fatalf("8 networks stored %#v", p)
	}
	hello(`[{"name":"dd","subnet":"10.200.10.0/24","projects":[` + members(32) + `]}]`)
	if p := stored(); p == nil || !p.Enabled || len(p.SharedNetworks) != 1 || len(p.SharedNetworks[0].Projects) != 32 {
		t.Fatalf("32 members stored %#v", p)
	}

	// A later hello without the key (agent < 0.2.9) reads back null.
	hello("")
	if p := stored(); p == nil || !p.Enabled || p.SharedNetworks != nil {
		t.Fatalf("absent stored %#v", p)
	}
	if got := sharedNetworksJSON(t, w, session, orgPath, sc.host.ID); got != "null" {
		t.Fatalf("absent org view %s", got)
	}
}

// netWorld is a scene whose agent answers every task as succeeded.
type netWorld struct {
	w       *world
	owner   store.User
	sc      scene
	tok     string
	project string
	host    string
	log     *taskLog
}

func newNetWorld(t *testing.T) netWorld {
	t.Helper()
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	log := watchTasks(t, conn, func(msg map[string]any) map[string]any {
		return imageAuditReply(msg, "succeeded", shaDigest(2))
	})
	return netWorld{w: w, owner: owner, sc: sc, tok: tok, project: sc.project.ID.String(), host: sc.host.ID.String(), log: log}
}

func (n netWorld) open(nets ...store.SharedNetwork) {
	setPrograms(n.w.t, n.w, n.sc.host.ID, netPrograms(nets))
}

func netOpen(name string, slugs ...string) store.SharedNetwork {
	return store.SharedNetwork{Name: name, Subnet: "10.200.10.0/24", Projects: append([]string{}, slugs...)}
}

// TestNetworksShortActionsNotGated: run, stop, restart and remove of an
// entry with networks are not refused for networks (api-v1 v1.25 item 3).
func TestNetworksShortActionsNotGated(t *testing.T) {
	n := newNetWorld(t)
	createImage(t, n.w, n.tok, n.sc.project.ID, "web", `{"kind":"service","image":"nginx","networks":["dd"]}`)
	hosts := map[string]*store.HostPrograms{
		"old agent":   netPrograms(nil),
		"closed host": netPrograms([]store.SharedNetwork{netOpen("dd", "other")}),
	}
	i := 0
	for label, programs := range hosts {
		setPrograms(t, n.w, n.sc.host.ID, programs)
		for _, action := range []string{"run", "stop", "restart", "remove"} {
			i++
			before := n.log.count()
			status, env, raw := postImage(t, n.w, n.tok, n.project, "web", action, n.host, fmt.Sprintf("short-%d", i), nil)
			if env.Msg == netReasonOld || strings.Contains(env.Msg, "is not open to this project") {
				t.Fatalf("%s %s refused for networks: %s", label, action, raw)
			}
			if status != http.StatusOK || n.log.count() != before+1 {
				t.Fatalf("%s %s: status %d tasks %d→%d %s", label, action, status, before, n.log.count(), raw)
			}
		}
	}
}

// TestNetworksDelegatedRollback: the plan names the networks, confirm is
// re-gated, a closed second name is reported, and an open rollback carries
// spec.networks.
func TestNetworksDelegatedRollback(t *testing.T) {
	n := newNetWorld(t)
	createImage(t, n.w, n.tok, n.sc.project.ID, "web", `{"kind":"service","image":"nginx","networks":["dd","edge"]}`)
	both := []store.SharedNetwork{netOpen("dd", "proj"), netOpen("edge", "proj")}
	n.open(both...)
	status, env, _ := postImage(t, n.w, n.tok, n.project, "web", "deploy", n.host, "seed", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")

	delegated := tokenKind(t, n.w, n.owner.ID, store.KindDelegated)
	status, env, _ = postImage(t, n.w, delegated, n.project, "web", "rollback", n.host, "roll-del", map[string]any{"digest": shaDigest(2)})
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	id, plan, _ := healthPlanOf(t, env)
	if want := "will deploy web (" + shaDigest(2) + ") on pi4; also joins shared network(s): dd, edge"; plan != want {
		t.Fatalf("plan %q", plan)
	}

	before := n.log.count()
	// edge (the second name) is closed: its name is the reason.
	n.open(netOpen("dd", "proj"), netOpen("edge", "other"))
	status, env, _ = mustCall(t, n.w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("roll-del"))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "shared network edge is not open to this project on this host")
	setPrograms(t, n.w, n.sc.host.ID, netPrograms(nil))
	status, env, _ = mustCall(t, n.w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("roll-del"))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", netReasonOld)
	if n.log.count() != before {
		t.Fatal("refused confirm sent a task")
	}
	n.open(both...)
	status, env, _ = mustCall(t, n.w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("roll-del"))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if n.log.count() != before+1 {
		t.Fatal("confirm did not dispatch")
	}
	task := n.log.raw(before)
	if task["kind"] != "image_rollback" {
		t.Fatalf("task kind %v", task["kind"])
	}
	if got, ok := networksOf(t, task); !ok || fmt.Sprint(got) != "[dd edge]" {
		t.Fatalf("rollback task networks %#v", got)
	}
}

// TestNetworksPayloadOrder: stored order is kept in the task (api-v1 v1.25
// item 4), not sorted.
func TestNetworksPayloadOrder(t *testing.T) {
	n := newNetWorld(t)
	createImage(t, n.w, n.tok, n.sc.project.ID, "web", `{"kind":"service","image":"nginx","networks":["edge","dd"]}`)
	n.open(netOpen("dd", "proj"), netOpen("edge", "proj"))
	status, env, _ := postImage(t, n.w, n.tok, n.project, "web", "deploy", n.host, "order", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	got, ok := networksOf(t, n.log.raw(0))
	if !ok {
		t.Fatal("no networks in task")
	}
	raw, _ := json.Marshal(got)
	if string(raw) != `["edge","dd"]` {
		t.Fatalf("task networks %s", raw)
	}
}

// TestNetworksGateOrder: earlier gates win (amendment A3), a caller without
// deploy gets 403, and a network refusal is audited.
func TestNetworksGateOrder(t *testing.T) {
	n := newNetWorld(t)
	createImage(t, n.w, n.tok, n.sc.project.ID, "pol", `{"kind":"service","image":"nginx","networks":["dd"],"health_policy":{"on_deploy_failure":"stop"}}`)
	createImage(t, n.w, n.tok, n.sc.project.ID, "web", `{"kind":"service","image":"nginx","networks":["dd"]}`)
	// The host has no health policy support and no shared networks: the
	// health-policy reason comes first.
	setPrograms(t, n.w, n.sc.host.ID, netPrograms(nil))
	status, env, _ := postImage(t, n.w, n.tok, n.project, "pol", "deploy", n.host, "order-1", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", reasonNoPolicy)

	viewer := n.w.token(n.sc.spare.ID)
	status, env, _ = postImage(t, n.w, viewer, n.project, "web", "deploy", n.host, "order-2", nil)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")

	status, env, _ = postImage(t, n.w, n.tok, n.project, "web", "deploy", n.host, "order-3", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", netReasonOld)
	row := latestAudit(t, n.w, n.sc.project.ID, "catalog.deploy")
	if row.Decision != "denied" || row.Result == nil || *row.Result != "bad_request" {
		t.Fatalf("audit %s %s", row.Decision, shownAudit(row.Result))
	}
	var detail map[string]any
	if err := json.Unmarshal(row.Detail, &detail); err != nil || detail["reason"] != netReasonOld || detail["entry"] != "web" {
		t.Fatalf("audit detail %s %v", row.Detail, err)
	}
	if n.log.count() != 0 {
		t.Fatal("a refused deploy sent a task")
	}
}

// TestNetworksPutHTTP covers PUT with networks (api-v1 v1.25 item 1).
func TestNetworksPutHTTP(t *testing.T) {
	c := newCatalogCast(t)
	item := catalogItem(c.sc.project.ID, "web")
	bare := createImage(t, c.w, c.console, c.sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	status, env, _ := mustCall(t, c.w, c.console, http.MethodPut, item, catalogImagePutBody(`{"kind":"service","image":"nginx","networks":["dd","DD"]}`, ""))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid networks")

	with := createImagePut(t, c, "web", `{"kind":"service","image":"nginx","networks":["dd"]}`)
	if with["spec_hash"] == bare["spec_hash"] || with["spec_hash"] == "" {
		t.Fatalf("adding networks kept spec_hash %v", with["spec_hash"])
	}
	cleared := createImagePut(t, c, "web", `{"kind":"service","image":"nginx","networks":[]}`)
	if cleared["spec_hash"] != bare["spec_hash"] {
		t.Fatalf("networks [] hash %v, no-networks hash %v", cleared["spec_hash"], bare["spec_hash"])
	}
	if spec, _ := cleared["spec"].(map[string]any); spec == nil || spec["networks"] != nil {
		t.Fatalf("[] was stored %#v", cleared["spec"])
	}

	status, env, _ = mustCall(t, c.w, c.tok, http.MethodPut, item, catalogImagePutBody(`{"kind":"service","image":"nginx","networks":["dd","edge"]}`, "net-upd"))
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	if _, plan, _ := healthPlanOf(t, env); plan != "will update image entry web: shared networks: none → dd, edge" {
		t.Fatalf("delegated put plan %q", plan)
	}
}

// TestNetworksVisibilityMatrix adds callers to TestNetworksVisibility: an org
// admin (not owner), a plain org member, a caller in two projects with access,
// a metrics-only project, and a project with nothing open.
func TestNetworksVisibilityMatrix(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	orgID := sc.project.OrgID
	second, err := w.mem.CreateProject(ctx, orgID, owner.ID, "Proj Two", "proj-two", w.now)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := w.mem.CreateProject(ctx, orgID, owner.ID, "Proj Metrics", "proj-metrics", w.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.grant(ctx, sc.host.ID, second.ID, "deploy", owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.grant(ctx, sc.host.ID, metrics.ID, "metrics", owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	orgAdmin := w.user("orgadmin@ex.com", "OrgAdmin")
	orgMember := w.user("orgmember@ex.com", "OrgMember")
	twice := w.user("twice@ex.com", "Twice")
	metricsOnly := w.user("metrics@ex.com", "Metrics")
	if _, err := w.mem.AddOrgMember(ctx, orgID, orgAdmin.ID, store.OrgRoleAdmin, owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	if _, err := w.mem.AddOrgMember(ctx, orgID, orgMember.ID, store.OrgRoleMember, owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	for _, p := range []store.Project{sc.project, second} {
		if _, err := w.mem.AddMember(ctx, p.ID, twice.ID, "viewer", owner.ID, w.now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.mem.AddMember(ctx, metrics.ID, metricsOnly.ID, "viewer", owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	setPrograms(t, w, sc.host.ID, netPrograms([]store.SharedNetwork{
		{Name: "dd", Subnet: "10.200.10.0/24", Projects: []string{"proj"}},
		{Name: "mx", Subnet: "10.200.11.0/24", Projects: []string{"proj-metrics"}},
	}))
	full := `[{"name":"dd","projects":["proj"],"subnet":"10.200.10.0/24"},{"name":"mx","projects":["proj-metrics"],"subnet":"10.200.11.0/24"}]`
	getPath := "/v1/hosts/" + sc.host.ID.String()
	orgPath := "/v1/orgs/" + orgID.String() + "/hosts"
	listOf := func(p store.Project) string { return "/v1/projects/" + p.ID.String() + "/hosts" }

	adminTok := w.token(orgAdmin.ID)
	twiceTok := w.token(twice.ID)
	metricsTok := w.token(metricsOnly.ID)
	cases := []struct {
		name, tok, path, want string
	}{
		{"org admin list", adminTok, listOf(sc.project), full},
		{"org admin get", adminTok, getPath, full},
		{"org admin org view", adminTok, orgPath, full},
		{"two projects get", twiceTok, getPath, `[]`},
		{"two projects list proj", twiceTok, listOf(sc.project), `[{"name":"dd","subnet":"10.200.10.0/24"}]`},
		{"nothing open list", twiceTok, listOf(second), `[]`},
		{"metrics-only list", metricsTok, listOf(metrics), `[{"name":"mx","subnet":"10.200.11.0/24"}]`},
		{"metrics-only get", metricsTok, getPath, `[{"name":"mx","subnet":"10.200.11.0/24"}]`},
	}
	for _, tc := range cases {
		if got := sharedNetworksJSON(t, w, tc.tok, tc.path, sc.host.ID); got != tc.want {
			t.Fatalf("%s: %s", tc.name, got)
		}
	}
	// A plain org member may not list the org's hosts at all.
	status, env, _ := mustCall(t, w, w.token(orgMember.ID), http.MethodGet, orgPath, "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
}

// v0231TaskGoldens are the exact task specs (storedSpec) computed on v0.2.31
// (618ed83, exported with git archive) for a host with and without health
// policy support. The current code must reproduce them.
var v0231TaskGoldens = []struct {
	name, raw           string
	sets                catalog.SetKeys
	files               map[string]struct{}
	withPolicy, without string
}{
	{
		name:       "service",
		raw:        `{"kind":"service","image":"ghcr.io/acme/web:1.2","command":["serve","--port","8080"],"ports":[{"host":8080,"container":8080,"public":false}],"env":{"A":"1"},"env_sets":["app"],"volumes":[{"name":"data","target":"/data"}],"healthcheck":{"test":["true"]},"logging":{"max_size_mb":10,"max_files":3},"memory_mb":256,"user":"1000"}`,
		sets:       catalog.SetKeys{"app": {"X"}},
		withPolicy: `{"command":["serve","--port","8080"],"entrypoint":null,"env":{"A":"1"},"env_sets":["app"],"healthcheck":{"interval_sec":30,"retries":3,"start_period_sec":0,"test":["true"],"timeout_sec":5},"healthy_after_sec":30,"image":"ghcr.io/acme/web:1.2","kind":"service","logging":{"max_files":3,"max_size_mb":10},"memory_mb":256,"ports":[{"container":8080,"host":8080,"public":false}],"restart":"unless-stopped","secret_env":{},"timeout_sec":0,"user":"1000","volumes":[{"name":"data","read_only":false,"target":"/data"}]}`,
		without:    `{"command":["serve","--port","8080"],"entrypoint":null,"env":{"A":"1"},"env_sets":["app"],"healthcheck":{"interval_sec":30,"retries":3,"start_period_sec":0,"test":["true"],"timeout_sec":5},"healthy_after_sec":30,"image":"ghcr.io/acme/web:1.2","kind":"service","logging":{"max_files":3,"max_size_mb":10},"memory_mb":256,"ports":[{"container":8080,"host":8080,"public":false}],"restart":"unless-stopped","secret_env":{},"timeout_sec":0,"user":"1000","volumes":[{"name":"data","read_only":false,"target":"/data"}]}`,
	},
	{
		name:       "job with timeout_sec",
		raw:        `{"kind":"job","image":"busybox:1.36","command":["true"],"timeout_sec":120}`,
		withPolicy: `{"command":["true"],"entrypoint":null,"env":{},"env_sets":[],"healthy_after_sec":0,"image":"docker.io/library/busybox:1.36","kind":"job","memory_mb":null,"ports":[],"restart":"no","secret_env":{},"timeout_sec":120,"user":"","volumes":[]}`,
		without:    `{"command":["true"],"entrypoint":null,"env":{},"env_sets":[],"healthy_after_sec":0,"image":"docker.io/library/busybox:1.36","kind":"job","memory_mb":null,"ports":[],"restart":"no","secret_env":{},"timeout_sec":120,"user":"","volumes":[]}`,
	},
	{
		name:       "health_policy and files",
		raw:        `{"kind":"service","image":"nginx:1.27","healthcheck":{"test":["true"]},"health_policy":{"on_deploy_failure":"stop","on_unhealthy":"restart","unhealthy_after_sec":60,"max_actions_per_hour":2},"files":[{"file":"app.conf","target":"/etc/app.conf"}]}`,
		files:      map[string]struct{}{"app.conf": {}},
		withPolicy: `{"command":null,"entrypoint":null,"env":{},"env_sets":[],"files":[{"file":"app.conf","target":"/etc/app.conf"}],"health_policy":{"max_actions_per_hour":2,"on_deploy_failure":"stop","on_unhealthy":"restart","unhealthy_after_sec":60},"healthcheck":{"interval_sec":30,"retries":3,"start_period_sec":0,"test":["true"],"timeout_sec":5},"healthy_after_sec":30,"image":"docker.io/library/nginx:1.27","kind":"service","memory_mb":null,"ports":[],"restart":"unless-stopped","secret_env":{},"timeout_sec":0,"user":"","volumes":[]}`,
		without:    `{"command":null,"entrypoint":null,"env":{},"env_sets":[],"files":[{"file":"app.conf","target":"/etc/app.conf"}],"healthcheck":{"interval_sec":30,"retries":3,"start_period_sec":0,"test":["true"],"timeout_sec":5},"healthy_after_sec":30,"image":"docker.io/library/nginx:1.27","kind":"service","memory_mb":null,"ports":[],"restart":"unless-stopped","secret_env":{},"timeout_sec":0,"user":"","volumes":[]}`,
	},
}

func TestV0231TaskGoldens(t *testing.T) {
	for _, g := range v0231TaskGoldens {
		img, err := catalog.BuildImage("web", []byte(g.raw), g.sets, g.files)
		if err != nil {
			t.Fatalf("%s: %v", g.name, err)
		}
		for _, policy := range []bool{true, false} {
			// A host that reports shared networks must not change the payload.
			for _, nets := range [][]store.SharedNetwork{nil, {}} {
				host := store.Host{Programs: &store.HostPrograms{Enabled: true, RunAs: []string{}, Images: true, Docker: true, Files: true, Healthcheck: true, Logging: true, HealthPolicy: policy, SharedNetworks: nets}}
				spec, err := storedSpec(img, host)
				if err != nil {
					t.Fatal(err)
				}
				got, err := json.Marshal(spec)
				if err != nil {
					t.Fatal(err)
				}
				want := g.without
				if policy {
					want = g.withPolicy
				}
				if string(got) != want {
					t.Fatalf("%s policy=%v nets=%v: %s", g.name, policy, nets != nil, got)
				}
			}
		}
	}
}

// TestNetworksTaskGoldenNew pins the task spec of an entry with networks
// ["dd","edge"]. The value was taken from the new (v1.25) code.
func TestNetworksTaskGoldenNew(t *testing.T) {
	const want = `{"command":["serve","--port","8080"],"entrypoint":null,"env":{"A":"1"},"env_sets":["app"],"healthcheck":{"interval_sec":30,"retries":3,"start_period_sec":0,"test":["true"],"timeout_sec":5},"healthy_after_sec":30,"image":"ghcr.io/acme/web:1.2","kind":"service","logging":{"max_files":3,"max_size_mb":10},"memory_mb":256,"networks":["dd","edge"],"ports":[{"container":8080,"host":8080,"public":false}],"restart":"unless-stopped","secret_env":{},"timeout_sec":0,"user":"1000","volumes":[{"name":"data","read_only":false,"target":"/data"}]}`
	raw := strings.TrimSuffix(v0231TaskGoldens[0].raw, "}") + `,"networks":["dd","edge"]}`
	img, err := catalog.BuildImage("web", []byte(raw), v0231TaskGoldens[0].sets, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := storedSpec(img, store.Host{Programs: netPrograms([]store.SharedNetwork{})})
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("task spec %s", got)
	}
}

// TestNetworksReason is the pure gate (api-v1 v1.25 item 3).
func TestNetworksReason(t *testing.T) {
	img := catalog.Image{Networks: []string{"dd", "edge"}}
	cases := []struct {
		name string
		host store.Host
		img  catalog.Image
		want string
	}{
		{"no networks, no programs", store.Host{}, catalog.Image{}, ""},
		{"no programs", store.Host{}, img, reasonNoNetworks},
		{"old agent", store.Host{Programs: netPrograms(nil)}, img, reasonNoNetworks},
		{"none defined", store.Host{Programs: netPrograms([]store.SharedNetwork{})}, img, reasonNetworkClosed("dd")},
		{"second closed", store.Host{Programs: netPrograms([]store.SharedNetwork{netOpen("dd", "proj"), netOpen("edge")})}, img, reasonNetworkClosed("edge")},
		{"open", store.Host{Programs: netPrograms([]store.SharedNetwork{netOpen("edge", "x", "proj"), netOpen("dd", "proj")})}, img, ""},
	}
	for _, tc := range cases {
		if got := networksReason(tc.host, tc.img, "proj"); got != tc.want {
			t.Fatalf("%s: %q", tc.name, got)
		}
	}
	if reasonNetworkClosed("dd") != netReasonClosed {
		t.Fatalf("closed text %q", reasonNetworkClosed("dd"))
	}
}

// TestNetworksPlanWorstCase checks the 290 runes the shared networks row adds
// to the worst-case update plan (mcp maxCatalogPlanRunes, summary.go): 4
// names of 32 on both sides plus the "; " separator.
func TestNetworksPlanWorstCase(t *testing.T) {
	names := func(c string) []string {
		out := make([]string, 4)
		for i := range out {
			out[i] = strings.Repeat(c, 31) + fmt.Sprint(i)
		}
		return out
	}
	old := mustPlanImage(t, "web", `{"kind":"service","image":"nginx:1"}`, nil, nil)
	next := mustPlanImage(t, "web", `{"kind":"service","image":"nginx:2"}`, nil, nil)
	base := len([]rune(catalogUpdatePlan("web", old, next)))
	old.Networks, next.Networks = names("a"), names("b")
	if got := len([]rune(catalogUpdatePlan("web", old, next))) - base; got != 290 {
		t.Fatalf("networks row adds %d runes, summary.go counts 290", got)
	}
}
