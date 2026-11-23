package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// fakeTXT is a TXTResolver with records set by the test.
type fakeTXT struct {
	mu      sync.Mutex
	records map[string][]string
	// down makes every lookup fail as a resolver outage would.
	down bool
}

func (f *fakeTXT) LookupTXT(_ context.Context, name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("i/o timeout")
	}
	v, ok := f.records[name]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return v, nil
}

func (f *fakeTXT) set(name string, values ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.records == nil {
		f.records = map[string][]string{}
	}
	if values == nil {
		delete(f.records, name)
		return
	}
	f.records[name] = values
}

// edgeCast is a world with a project, an owner token, a host and a
// resolver, and the edge hook wired as run.go wires it.
type edgeCast struct {
	w     *world
	sc    scene
	owner store.User
	tok   string
	dns   *fakeTXT
	org   string
}

func newEdgeCast(t *testing.T) edgeCast {
	t.Helper()
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("admin@ex.com", "Admin"), w.user("viewer@ex.com", "Viewer"), w.user("spare@ex.com", "Spare"), w.user("extra@ex.com", "Extra"))
	dns := &fakeTXT{}
	w.srv.Resolver = dns
	w.hub.SetEdgeHook(w.srv.EdgeHook)
	t.Cleanup(w.srv.waitEdges)
	return edgeCast{w: w, sc: sc, owner: owner, tok: w.token(owner.ID), dns: dns, org: "/v1/orgs/" + sc.project.OrgID.String()}
}

// verifiedDomain adds name, publishes its TXT record, verifies it and
// assigns it to the project with both patterns.
func (c edgeCast) verifiedDomain(t *testing.T, name string) string {
	t.Helper()
	status, env := c.w.do(c.tok, http.MethodPost, c.org+"/domains", `{"name":"`+name+`"}`)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	d := objectPayload(t, env)
	c.dns.set(d["txt_name"].(string), d["txt_value"].(string))
	id := d["id"].(string)
	status, env = c.w.do(c.tok, http.MethodPost, c.org+"/domains/"+id+"/verify", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env = c.w.do(c.tok, http.MethodPut, c.org+"/domains/"+id+"/assignments",
		`{"assignments":[{"project_id":"`+c.sc.project.ID.String()+`","pattern":"@"},{"project_id":"`+c.sc.project.ID.String()+`","pattern":"*"}]}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	return id
}

func (c edgeCast) entryPath(name string) string {
	return "/v1/projects/" + c.sc.project.ID.String() + "/catalog/" + name + "/routes"
}

// planAndConfirm PUTs routes and confirms the plan; it returns the plan text.
func (c edgeCast) planAndConfirm(t *testing.T, tok, entry, routes, key string) (string, map[string]any) {
	t.Helper()
	status, env := c.w.do(tok, http.MethodPut, c.entryPath(entry), `{"routes":`+routes+`,"idempotency_key":"`+key+`"}`)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	plan := objectPayload(t, env)
	status, env = c.w.do(tok, http.MethodPost, "/v1/confirmations/"+plan["confirmation_id"].(string), `{"idempotency_key":"`+key+`"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	return plan["plan"].(string), objectPayload(t, env)
}

const webSpec = `{"kind":"service","image":"nginx","ports":[{"host":18788,"container":8788}]}`

func edgePrograms(enabled bool) map[string]any {
	p := allowPrograms(false)
	p["images"] = true
	p["edge"] = map[string]any{"enabled": enabled, "image": "docker.io/library/caddy@sha256:" + strings.Repeat("a", 64)}
	return p
}

func ownedWeb(ports ...int) map[string]any {
	return map[string]any{
		"name": "deploybot-proj-web", "kind": "container", "ports": ports, "container_name": "deploybot-proj-web",
		"owner": map[string]any{"kind": "image", "project": "proj", "entry": "web"},
	}
}

// edgeAgent answers edge tasks: edge_apply with applied and the hash it got.
func edgeAgent(msg map[string]any) map[string]any {
	spec, _ := msg["spec"].(map[string]any)
	payload := map[string]any{"outcome": "stopped"}
	if msg["kind"] == "edge_apply" {
		payload = map[string]any{"outcome": "applied", "config_hash": spec["config_hash"]}
	}
	return map[string]any{"v": 1, "type": "task_result", "task_id": msg["task_id"], "state": "succeeded", "payload": payload}
}

func edgeState(t *testing.T, c edgeCast) map[string]any {
	t.Helper()
	c.w.srv.waitEdges()
	status, env := c.w.do(c.tok, http.MethodGet, "/v1/hosts/"+c.sc.host.ID.String()+"/edge", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	return objectPayload(t, env)
}

func TestDomainsFlow(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	status, env := w.do(c.tok, http.MethodPost, c.org+"/domains", `{"name":"Example.com"}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgDomainName)
	status, env = w.do(c.tok, http.MethodPost, c.org+"/domains", `{"name":"10.0.0.1"}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgDomainName)
	status, env = w.do(c.tok, http.MethodPost, c.org+"/domains", `{"name":"example.com"}`)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	d := objectPayload(t, env)
	if d["status"] != "pending" || d["txt_name"] != "_deploybot.example.com" || !strings.HasPrefix(d["txt_value"].(string), "deploybot-verify=") {
		t.Fatalf("domain %#v", d)
	}
	id := d["id"].(string)
	status, env = w.do(c.tok, http.MethodPost, c.org+"/domains", `{"name":"example.com"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", msgDomainAdded)

	status, env = w.do(c.tok, http.MethodPost, c.org+"/domains/"+id+"/verify", "")
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "TXT record not found yet: add _deploybot.example.com = "+d["txt_value"].(string))
	c.dns.set("_deploybot.example.com", "unrelated", d["txt_value"].(string))
	status, env = w.do(c.tok, http.MethodPost, c.org+"/domains/"+id+"/verify", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if v := objectPayload(t, env); v["status"] != "verified" || v["verified_at"] == nil {
		t.Fatalf("verified %#v", v)
	}

	// Another org cannot add (or verify) the name.
	other := w.user("other@ex.com", "Other")
	otherTok := w.token(other.ID)
	otherOrg := "/v1/orgs/" + w.mem.PersonalOrg(other.ID).String()
	status, env = w.do(otherTok, http.MethodPost, otherOrg+"/domains", `{"name":"example.com"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", msgDomainTaken)
	// ... nor see this org's domains.
	status, env = w.do(otherTok, http.MethodGet, c.org+"/domains", "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")

	// Assignments: projects of the org only, pattern @ or *.
	otherProject, err := w.mem.CreateProject(context.Background(), w.mem.PersonalOrg(other.ID), other.ID, "Theirs", "theirs", w.now)
	if err != nil {
		t.Fatal(err)
	}
	status, env = w.do(c.tok, http.MethodPut, c.org+"/domains/"+id+"/assignments", `{"assignments":[{"project_id":"`+otherProject.ID.String()+`","pattern":"@"}]}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgAssignShape)
	status, env = w.do(c.tok, http.MethodPut, c.org+"/domains/"+id+"/assignments", `{"assignments":[{"project_id":"`+c.sc.project.ID.String()+`","pattern":"**"}]}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgAssignShape)
	status, env = w.do(c.tok, http.MethodPut, c.org+"/domains/"+id+"/assignments", `{"assignments":[{"project_id":"`+c.sc.project.ID.String()+`","pattern":"*"}]}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	a := objectPayload(t, env)["assignments"].([]any)[0].(map[string]any)
	if a["project_name"] != "Proj" || a["pattern"] != "*" {
		t.Fatalf("assignment %#v", a)
	}
	status, env = w.do(c.tok, http.MethodGet, c.org+"/domains", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if items := objectPayload(t, env)["items"].([]any); len(items) != 1 {
		t.Fatalf("items %#v", items)
	}
	// Delegated sessions never read or write domains.
	del := tokenKind(t, w, c.owner.ID, store.KindDelegated)
	status, env = w.do(del, http.MethodGet, c.org+"/domains", "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	for _, action := range []string{audit.ActionDomainAdd, audit.ActionDomainVerify, audit.ActionDomainAssign} {
		assertAuditAction(t, w, action)
	}
}

func assertAuditAction(t *testing.T, w *world, action string) {
	t.Helper()
	for _, row := range w.mem.AllAudits() {
		if row.Action == action && row.Decision == audit.DecisionAllowed {
			return
		}
	}
	t.Fatalf("no allowed %s audit row", action)
}

func TestRoutesPlanAndConfirm(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	c.verifiedDomain(t, "example.com")
	entry := createImage(t, w, c.tok, c.sc.project.ID, "web", webSpec)
	createImage(t, w, c.tok, c.sc.project.ID, "api", webSpec)

	put := func(tok, entry, routes, key string) (int, envelope) {
		return w.do(tok, http.MethodPut, c.entryPath(entry), `{"routes":`+routes+`,"idempotency_key":"`+key+`"}`)
	}
	status, env := put(c.tok, "web", `[{"hostname":"app.example.com","port":9999}]`, "r0")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "port 9999 is not published; add a ports line for it (127.0.0.1 is enough)")
	status, env = put(c.tok, "web", `[{"hostname":"a.b.example.com","port":8788}]`, "r0")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "a.b.example.com is not under a verified domain assigned to this project")
	status, env = put(c.tok, "web", `[{"hostname":"app.example.com","port":8788,"client_ip_from":"akamai"}]`, "r0")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "client_ip_from must be cloudflare or absent")
	status, env = put(c.tok, "web", `[{"hostname":"app.example.com","port":8788},{"hostname":"app.example.com","path_prefix":"/","port":8788}]`, "r0")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "https://app.example.com/ is listed twice")

	plan, done := c.planAndConfirm(t, c.tok, "web",
		`[{"hostname":"app.example.com","port":8788,"client_ip_from":"cloudflare","no_store_paths":["/evidence/"]},{"hostname":"example.com","path_prefix":"/api","port":8788}]`, "r1")
	want := "routes for web:\nadd https://app.example.com/ → port 8788 (client IP from Cloudflare; no-store /evidence/) (pending: no host running web has the edge on)\nadd https://example.com/api → port 8788 (pending: no host running web has the edge on)"
	if plan != want {
		t.Fatalf("plan %q", plan)
	}
	if routes := done["routes"].([]any); len(routes) != 2 || routes[0].(map[string]any)["state"] != "pending" {
		t.Fatalf("confirm %#v", done)
	}
	// Routes never touch spec_hash.
	if got := createImageGet(t, w, c.tok, c.sc.project.ID, "web"); got["spec_hash"] != entry["spec_hash"] {
		t.Fatal("spec_hash changed")
	}
	// A second entry cannot take the hostname.
	status, env = put(c.tok, "api", `[{"hostname":"app.example.com","port":8788}]`, "r2")
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "https://app.example.com/ is already routed")
	// Delegated sessions get a plan too; it names what stops.
	del := tokenKind(t, w, c.owner.ID, store.KindDelegated)
	plan, _ = c.planAndConfirm(t, del, "web", `[{"hostname":"app.example.com","port":8788}]`, "r3")
	if plan != "routes for web:\nchange https://app.example.com/ → port 8788 (pending: no host running web has the edge on)\nremove https://example.com/api" {
		t.Fatalf("delegated plan %q", plan)
	}
	// A viewer cannot change routes.
	viewer, _ := w.mem.UserByEmail(context.Background(), "viewer@ex.com")
	status, env = put(w.token(viewer.ID), "web", `[]`, "r4")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")

	status, env = w.do(c.tok, http.MethodGet, "/v1/projects/"+c.sc.project.ID.String()+"/routes", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	items := objectPayload(t, env)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["hostname"] != "app.example.com" {
		t.Fatalf("routes %#v", items)
	}
	assertAuditAction(t, w, audit.ActionRoutesUpdate)
	assertAuditAction(t, w, audit.ActionRoutesConfirmation)
}

// spec-release-v0239 F3: a Deploybot container on 443 is named with its
// project, and the refusal says what to do next.
func TestEdgeSwitchRefusedByProjectContainer(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	createImage(t, w, c.tok, c.sc.project.ID, "web", webSpec)
	conn := helloAgent(t, w, c.sc.host, c.sc.credPlain, c.sc.credID, edgePrograms(true), []any{ownedWeb(443)})
	watchTasks(t, conn, edgeAgent)
	status, env := w.do(c.tok, http.MethodPut, "/v1/hosts/"+c.sc.host.ID.String()+"/edge", `{"enabled":true}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict",
		"port 443 is used by web (project "+c.sc.project.Name+"); stop it, wait until the host no longer reports that port (up to a minute), then try again")
}

func TestEdgeSwitchApplyAndReconnect(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	hostPath := "/v1/hosts/" + c.sc.host.ID.String() + "/edge"
	// No hello edge field: the edge is not available.
	status, env := w.do(c.tok, http.MethodPut, hostPath, `{"enabled":true}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgEdgeUnavailable)

	c.verifiedDomain(t, "example.com")
	createImage(t, w, c.tok, c.sc.project.ID, "web", webSpec)
	c.planAndConfirm(t, c.tok, "web", `[{"hostname":"app.example.com","port":8788}]`, "r1")

	occupant := map[string]any{"name": "caddy", "kind": "process", "ports": []int{80, 443}, "process_name": "caddy"}
	conn := helloAgent(t, w, c.sc.host, c.sc.credPlain, c.sc.credID, edgePrograms(true), []any{ownedWeb(18788), occupant})
	tasks := watchTasks(t, conn, edgeAgent)
	status, env = w.do(c.tok, http.MethodPut, hostPath, `{"enabled":true}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "port 443 is used by a process outside Deploybot; stop it, wait until the host no longer reports that port (up to a minute), then try again")

	// The old proxy stops; the next report no longer lists it.
	writeFrame(t, conn, map[string]any{"v": 1, "type": "services", "services": []any{ownedWeb(18788)}})
	waitFor(t, func() bool {
		svcs, _ := w.mem.ListServices(context.Background(), c.sc.host.ID, nil, w.now)
		for _, s := range svcs {
			if s.Name == "caddy" && s.GoneAt == nil {
				return false
			}
		}
		return true
	})
	status, env = w.do(c.tok, http.MethodPut, hostPath, `{"enabled":true}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	state := edgeState(t, c)
	if state["status"] != "applied" || state["available"] != true || state["routes"] != float64(1) {
		t.Fatalf("edge %#v", state)
	}
	apply := lastTask(t, tasks, "edge_apply")
	spec := apply["spec"].(map[string]any)
	raw, _ := json.Marshal(spec["config"])
	sum := sha256.Sum256(raw)
	if spec["config_hash"] != hex.EncodeToString(sum[:]) || state["config_hash"] != spec["config_hash"] {
		t.Fatalf("config hash %v %v", spec["config_hash"], state["config_hash"])
	}
	if !strings.Contains(string(raw), `"dial":"127.0.0.1:18788"`) || !strings.Contains(string(raw), `"host":["app.example.com"]`) {
		t.Fatalf("config %s", raw)
	}
	status, env = w.do(c.tok, http.MethodGet, c.entryPath("web"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	item := objectPayload(t, env)["items"].([]any)[0].(map[string]any)
	if item["state"] != "live" || item["host_id"] != c.sc.host.ID.String() {
		t.Fatalf("route %#v", item)
	}
	// Host reads carry the edge summary and the hello edge field.
	status, env = w.do(c.tok, http.MethodGet, "/v1/hosts/"+c.sc.host.ID.String(), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	host := objectPayload(t, env)
	if e := host["edge"].(map[string]any); e["enabled"] != true || e["status"] != "applied" {
		t.Fatalf("host edge %#v", host["edge"])
	}
	if pe := host["programs"].(map[string]any)["edge"].(map[string]any); pe["enabled"] != true {
		t.Fatalf("programs edge %#v", pe)
	}

	// A services report with nothing changed sends nothing new.
	before := countKind(tasks, "edge_apply")
	writeFrame(t, conn, map[string]any{"v": 1, "type": "services", "services": []any{ownedWeb(18788)}})
	time.Sleep(100 * time.Millisecond)
	w.srv.waitEdges()
	if countKind(tasks, "edge_apply") != before {
		t.Fatal("an unchanged config was sent again")
	}

	// Offline: the change waits; the next hello sends the full config.
	conn.Close(websocket.StatusNormalClosure, "")
	waitFor(t, func() bool { return w.srv.hostStatus(c.sc.host) != "online" })
	c.planAndConfirm(t, c.tok, "web", `[{"hostname":"app.example.com","port":8788},{"hostname":"www.example.com","port":8788}]`, "r2")
	if s := edgeState(t, c); s["status"] != "pending" || s["detail"] != msgEdgeOffline {
		t.Fatalf("offline edge %#v", s)
	}
	conn = helloAgent(t, w, c.sc.host, c.sc.credPlain, c.sc.credID, edgePrograms(true), []any{ownedWeb(18788)})
	tasks = watchTasks(t, conn, edgeAgent)
	waitFor(t, func() bool { return countKind(tasks, "edge_apply") == 1 })
	if s := edgeState(t, c); s["status"] != "applied" || s["routes"] != float64(2) {
		t.Fatalf("reconnected edge %#v", s)
	}
	cfg, _ := json.Marshal(lastTask(t, tasks, "edge_apply")["spec"].(map[string]any)["config"])
	if !strings.Contains(string(cfg), "www.example.com") {
		t.Fatalf("full config %s", cfg)
	}

	// Disable sends edge_stop.
	status, env = w.do(c.tok, http.MethodPut, hostPath, `{"enabled":false}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if s := edgeState(t, c); s["status"] != "off" || s["enabled"] != false {
		t.Fatalf("disabled edge %#v", s)
	}
	if countKind(tasks, "edge_stop") != 1 {
		t.Fatal("no edge_stop")
	}
	assertAuditAction(t, w, audit.ActionEdgeEnable)
	assertAuditAction(t, w, audit.ActionEdgeDisable)
	// Delegated sessions cannot read the edge.
	del := tokenKind(t, w, c.owner.ID, store.KindDelegated)
	status, env = w.do(del, http.MethodGet, hostPath, "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
}

// An agent without the hello flag is never sent an edge task.
func TestEdgeNotSentWithoutHelloFlag(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	// The switch was stored on (as if the policy was on before).
	if _, err := w.mem.SetHostEdgeEnabled(context.Background(), c.sc.host.ID, true, w.now); err != nil {
		t.Fatal(err)
	}
	// An agent before 0.2.11 sends no programs.edge at all.
	conn := helloAgent(t, w, c.sc.host, c.sc.credPlain, c.sc.credID, allowPrograms(false), nil)
	tasks := watchTasks(t, conn, edgeAgent)
	time.Sleep(100 * time.Millisecond)
	if s := edgeState(t, c); s["status"] != "pending" || s["detail"] != msgEdgeUnavailable {
		t.Fatalf("edge %#v", s)
	}
	if tasks.count() != 0 {
		t.Fatalf("%d tasks sent", tasks.count())
	}
}

func TestDomainLapseAndDelete(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	id := c.verifiedDomain(t, "example.com")
	createImage(t, w, c.tok, c.sc.project.ID, "web", webSpec)
	c.planAndConfirm(t, c.tok, "web", `[{"hostname":"app.example.com","port":8788}]`, "r1")

	// The record goes away; seven daily checks lapse the domain.
	c.dns.set("_deploybot.example.com")
	start := w.now
	for day := 1; day <= 7; day++ {
		at := start.Add(time.Duration(day) * 25 * time.Hour)
		w.srv.Now = func() time.Time { return at }
		if n := w.srv.RecheckDomains(context.Background()); n != 1 {
			t.Fatalf("day %d checked %d", day, n)
		}
		// A second pass the same day checks nothing.
		if n := w.srv.RecheckDomains(context.Background()); n != 0 {
			t.Fatalf("day %d second pass checked %d", day, n)
		}
	}
	status, env := w.do(c.tok, http.MethodGet, c.org+"/domains", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if d := objectPayload(t, env)["items"].([]any)[0].(map[string]any); d["status"] != "lapsed" {
		t.Fatalf("domain %#v", d)
	}
	assertAuditAction(t, w, audit.ActionDomainLapsed)
	// Existing routes stay; a new one is refused.
	status, env = w.do(c.tok, http.MethodPut, c.entryPath("web"), `{"routes":[{"hostname":"app.example.com","port":8788},{"hostname":"new.example.com","port":8788}],"idempotency_key":"r2"}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "domain example.com has lapsed; verify it again before adding routes under it")
	status, env = w.do(c.tok, http.MethodPut, c.entryPath("web"), `{"routes":[{"hostname":"app.example.com","port":8788}],"idempotency_key":"r3"}`)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")

	// Delete: the plan names the route that stops; the confirm removes it.
	status, env = w.do(c.tok, http.MethodDelete, c.org+"/domains/"+id, `{"idempotency_key":"d1"}`)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	plan := objectPayload(t, env)
	if plan["plan"] != "remove domain example.com; routes that stop:\nhttps://app.example.com/ (project Proj, entry web)" {
		t.Fatalf("plan %q", plan["plan"])
	}
	confirm := "/v1/confirmations/" + plan["confirmation_id"].(string)
	status, env = w.do(c.tok, http.MethodPost, confirm, `{"idempotency_key":"d1"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if out := objectPayload(t, env); out["deleted"] != true || out["routes_removed"] != float64(1) {
		t.Fatalf("delete %#v", out)
	}
	status, env = w.do(c.tok, http.MethodPost, confirm, `{"idempotency_key":"d1"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this confirmation was already used")
	routes, _ := w.mem.ListProjectRoutes(context.Background(), c.sc.project.ID)
	if len(routes) != 0 {
		t.Fatalf("routes left %v", routes)
	}
	assertAuditAction(t, w, audit.ActionDomainDelete)
}

func TestReadinessRoutesCheck(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	c.verifiedDomain(t, "example.com")
	createImage(t, w, c.tok, c.sc.project.ID, "web", webSpec)
	path := "/v1/projects/" + c.sc.project.ID.String() + "/catalog/web/readiness"
	routesRow := func() map[string]any {
		status, env := w.do(c.tok, http.MethodGet, path+"?host_id="+c.sc.host.ID.String(), "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		for _, row := range objectPayload(t, env)["checks"].([]any) {
			if m := row.(map[string]any); m["check"] == "routes" {
				return m
			}
		}
		return nil
	}
	if routesRow() != nil {
		t.Fatal("routes check without routes")
	}
	c.planAndConfirm(t, c.tok, "web", `[{"hostname":"app.example.com","port":8788}]`, "r1")
	row := routesRow()
	hostID := c.sc.host.ID.String()
	fixOf := func(row map[string]any) map[string]any { return row["fix"].(map[string]any) }
	if row["status"] != "warn" || !strings.Contains(row["message"].(string), "the edge is not available on pi4") ||
		fixOf(row)["action"] != "allow_edge" || fixOf(row)["host_id"] != hostID {
		t.Fatalf("routes %#v", row)
	}
	// api-v1 v1.49: a console-managed host names its Policy tab.
	setPrograms(t, w, c.sc.host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{}, ConsolePolicy: true, ImagePatterns: []string{}})
	if row = routesRow(); row["message"] != "the edge is not allowed on pi4; an org admin turns it on on the host's Policy tab" || fixOf(row)["action"] != "allow_edge" {
		t.Fatalf("routes %#v", row)
	}
	setPrograms(t, w, c.sc.host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{}, Edge: &store.HostEdgePolicy{Enabled: true}})
	if row = routesRow(); row["message"] != "the edge is off on pi4; an org admin turns it on in the console" || fixOf(row)["action"] != "turn_on_edge" || fixOf(row)["host_id"] != hostID {
		t.Fatalf("routes %#v", row)
	}
	if _, err := w.mem.SetHostEdgeEnabled(context.Background(), c.sc.host.ID, true, w.now); err != nil {
		t.Fatal(err)
	}
	if row = routesRow(); row["status"] != "ok" {
		t.Fatalf("routes %#v", row)
	}
}

func TestOrgACMEEmail(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	status, env := w.do(c.tok, http.MethodPatch, c.org, `{"acme_email":"not-an-email"}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid acme_email")
	status, env = w.do(c.tok, http.MethodPatch, c.org, `{"acme_email":"ops@example.com"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if org := objectPayload(t, env); org["acme_email"] != "ops@example.com" || org["name"] == "" {
		t.Fatalf("org %#v", org)
	}
	status, env = w.do(c.tok, http.MethodGet, c.org, "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if objectPayload(t, env)["acme_email"] != "ops@example.com" {
		t.Fatal("acme_email not read back")
	}
	assertAuditAction(t, w, audit.ActionOrgACMEEmail)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func countKind(l *taskLog, kind string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, task := range l.tasks {
		if task["kind"] == kind {
			n++
		}
	}
	return n
}

func lastTask(t *testing.T, l *taskLog, kind string) map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := len(l.tasks) - 1; i >= 0; i-- {
		if l.tasks[i]["kind"] == kind {
			return l.tasks[i]
		}
	}
	t.Fatalf("no %s task", kind)
	return nil
}

var _ = uuid.Nil
