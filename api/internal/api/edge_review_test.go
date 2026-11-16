package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/more-than-code/bot-control-plane/api/internal/agentws"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Review fixes for api-v1 v1.35 (2026-11-17).

// lastConfig is the config of the last edge_apply, as text.
func lastConfig(t *testing.T, tasks *taskLog) string {
	t.Helper()
	raw, _ := json.Marshal(lastTask(t, tasks, "edge_apply")["spec"].(map[string]any)["config"])
	return string(raw)
}

func entryRouteState(t *testing.T, c edgeCast, entry string) map[string]any {
	t.Helper()
	c.w.srv.waitEdges()
	status, env := c.w.do(c.tok, http.MethodGet, c.entryPath(entry), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	return objectPayload(t, env)["items"].([]any)[0].(map[string]any)
}

// A1: a route reaches only the entry's own listener on the host.
func TestRouteUpstreamBinding(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	ctx := context.Background()
	c.verifiedDomain(t, "example.com")

	// A program without a declared port cannot be routed.
	createProgram(t, w, c.tok, c.sc.project.ID, "prog", "pnpm run dev", false)
	status, env := w.do(c.tok, http.MethodPut, c.entryPath("prog"), `{"routes":[{"hostname":"p.example.com","port":3000}],"idempotency_key":"p1"}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "program prog declares no port; add one to route it")

	// Another project on the same host listens on 5432.
	other, err := w.mem.CreateProject(ctx, c.sc.project.OrgID, c.owner.ID, "Other", "other", w.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.grant(ctx, c.sc.host.ID, other.ID, "deploy", c.owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	createImage(t, w, c.tok, other.ID, "db", `{"kind":"service","image":"postgres","ports":[{"host":5432,"container":5432}]}`)
	createImage(t, w, c.tok, c.sc.project.ID, "web", webSpec)
	c.planAndConfirm(t, c.tok, "web", `[{"hostname":"app.example.com","port":8788}]`, "r1")
	otherDB := map[string]any{
		"name": "deploybot-other-db", "kind": "container", "ports": []int{5432}, "container_name": "deploybot-other-db",
		"owner": map[string]any{"kind": "image", "project": "other", "entry": "db"},
	}
	conn := helloAgent(t, w, c.sc.host, c.sc.credPlain, c.sc.credID, edgePrograms(true), []any{ownedWeb(18788), otherDB})
	tasks := watchTasks(t, conn, edgeAgent)
	status, env = w.do(c.tok, http.MethodPut, "/v1/hosts/"+c.sc.host.ID.String()+"/edge", `{"enabled":true}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if s := edgeState(t, c); s["routes"] != float64(1) || !strings.Contains(lastConfig(t, tasks), `127.0.0.1:18788`) {
		t.Fatalf("edge %#v", s)
	}
	if st := entryRouteState(t, c, "web"); st["state"] != "live" {
		t.Fatalf("route %#v", st)
	}

	// The spec is edited (no redeploy) to publish 8788 on the other
	// project's port: the route must not follow it.
	status, env, _ = mustCall(t, w, c.tok, http.MethodPut, catalogItem(c.sc.project.ID, "web"),
		catalogImagePutBody(`{"kind":"service","image":"nginx","ports":[{"host":5432,"container":8788}]}`, ""))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	writeFrame(t, conn, map[string]any{"v": 1, "type": "services", "services": []any{ownedWeb(18788), otherDB}})
	waitFor(t, func() bool { return countKind(tasks, "edge_apply") == 2 })
	if cfg := lastConfig(t, tasks); strings.Contains(cfg, "127.0.0.1:5432") || strings.Contains(cfg, "app.example.com") {
		t.Fatalf("config reaches a foreign port: %s", cfg)
	}
	if st := entryRouteState(t, c, "web"); st["state"] != "pending" {
		t.Fatalf("route %#v", st)
	}
}

// B2: a disable made while an apply is in flight wins.
func TestEdgeDisableDuringApply(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	conn := helloAgent(t, w, c.sc.host, c.sc.credPlain, c.sc.credID, edgePrograms(true), []any{ownedWeb(18788)})
	release := make(chan struct{})
	tasks := watchTasks(t, conn, func(msg map[string]any) map[string]any {
		if msg["kind"] == "edge_apply" {
			<-release
		}
		return edgeAgent(msg)
	})
	hostPath := "/v1/hosts/" + c.sc.host.ID.String() + "/edge"
	status, env := w.do(c.tok, http.MethodPut, hostPath, `{"enabled":true}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	waitFor(t, func() bool { return countKind(tasks, "edge_apply") == 1 })
	status, env = w.do(c.tok, http.MethodPut, hostPath, `{"enabled":false}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if objectPayload(t, env)["enabled"] != false {
		t.Fatal("disable not stored")
	}
	close(release)
	s := edgeState(t, c)
	if s["enabled"] != false || s["status"] != "off" || countKind(tasks, "edge_stop") != 1 {
		t.Fatalf("edge %#v, stops %d", s, countKind(tasks, "edge_stop"))
	}
}

// C2: the host's runner.toml turns the edge off: the API stops it.
func TestEdgePolicyOffStops(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	conn := helloAgent(t, w, c.sc.host, c.sc.credPlain, c.sc.credID, edgePrograms(true), []any{ownedWeb(18788)})
	watchTasks(t, conn, edgeAgent)
	status, env := w.do(c.tok, http.MethodPut, "/v1/hosts/"+c.sc.host.ID.String()+"/edge", `{"enabled":true}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if s := edgeState(t, c); s["status"] != "applied" {
		t.Fatalf("edge %#v", s)
	}
	conn.Close(websocket.StatusNormalClosure, "")
	waitFor(t, func() bool { return w.srv.hostStatus(c.sc.host) != "online" })
	conn = helloAgent(t, w, c.sc.host, c.sc.credPlain, c.sc.credID, edgePrograms(false), []any{ownedWeb(18788)})
	tasks := watchTasks(t, conn, edgeAgent)
	waitFor(t, func() bool { return countKind(tasks, "edge_stop") == 1 })
	if s := edgeState(t, c); s["status"] != "off" || s["detail"] != msgEdgePolicyOff || countKind(tasks, "edge_apply") != 0 {
		t.Fatalf("edge %#v", s)
	}
}

// C1: agent error codes become sentences with the agent's detail.
func TestEdgeErrDetail(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&agentws.CallError{Agent: "conflict", Payload: json.RawMessage(`{"outcome":"failed","detail":"volume deploybot-edge-data has no owner label"}`)},
			"a container or volume named deploybot-edge exists that Deploybot does not own: volume deploybot-edge-data has no owner label"},
		{&agentws.CallError{Agent: "port_in_use", Payload: json.RawMessage(`{"detail":"port 443 is used by caddy"}`)}, "port 443 is used by caddy"},
		{&agentws.CallError{Agent: "port_in_use"}, "port 80 or 443 is in use on the host"},
		{&agentws.CallError{Agent: "docker_unavailable", Payload: json.RawMessage(`{"detail":"docker_unavailable"}`)}, "Docker is not available on the host"},
		{&agentws.CallError{Agent: "image_unavailable"}, "the edge image could not be pulled"},
		{&agentws.CallError{Agent: "helper_unavailable"}, "the agent's root helper is not available"},
		{&agentws.CallError{Agent: "io_error", Payload: json.RawMessage(`{"detail":"disk full"}`)}, "the agent could not write the edge files: disk full"},
		{&agentws.CallError{Agent: "invalid_config", Payload: json.RawMessage(`{"detail":"loading config: bad dial"}`)}, "Caddy refused the config; the previous config keeps serving: loading config: bad dial"},
		{&agentws.CallError{Agent: "bad_spec"}, "the agent refused the edge task as malformed"},
		{&agentws.CallError{Agent: "edge_disabled"}, "the host's runner.toml does not allow the edge"},
		{&agentws.CallError{Agent: "weird"}, "the agent refused the edge task (weird)"},
		{&agentws.CallError{Code: "timeout", Msg: "timed out"}, "timed out"},
		{errors.New("the host applied a different config"), "the host applied a different config"},
	} {
		if got := edgeErrDetail(tc.err); got != tc.want {
			t.Fatalf("%#v: %q", tc.err, got)
		}
	}
}

// Review 4: a domain delete plan is bound to the routes it names.
func TestDomainDeleteRoutesChanged(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	id := c.verifiedDomain(t, "example.com")
	createImage(t, w, c.tok, c.sc.project.ID, "web", webSpec)
	c.planAndConfirm(t, c.tok, "web", `[{"hostname":"app.example.com","port":8788}]`, "r1")
	status, env := w.do(c.tok, http.MethodDelete, c.org+"/domains/"+id, `{"idempotency_key":"d1"}`)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	plan := objectPayload(t, env)
	c.planAndConfirm(t, c.tok, "web", `[{"hostname":"app.example.com","port":8788},{"hostname":"www.example.com","port":8788}]`, "r2")
	status, env = w.do(c.tok, http.MethodPost, "/v1/confirmations/"+plan["confirmation_id"].(string), `{"idempotency_key":"d1"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "routes changed since the plan; ask for a new one")
	if d, err := w.mem.ListOrgDomains(context.Background(), c.sc.project.OrgID); err != nil || len(d) != 1 {
		t.Fatalf("domain gone %v %v", d, err)
	}
}

// Review 7: a resolver outage is not a failed check.
func TestDomainCheckSkipsDNSOutage(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	id := c.verifiedDomain(t, "example.com")
	c.dns.mu.Lock()
	c.dns.down = true
	c.dns.mu.Unlock()
	status, env := w.do(c.tok, http.MethodPost, c.org+"/domains/"+id+"/verify", "")
	mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", "the DNS lookup failed; try again")
	at := w.now.Add(25 * time.Hour)
	w.srv.Now = func() time.Time { return at }
	if n := w.srv.RecheckDomains(context.Background()); n != 0 {
		t.Fatalf("checked %d", n)
	}
	domains, _ := w.mem.ListOrgDomains(context.Background(), c.sc.project.OrgID)
	if domains[0].Failures != 0 || domains[0].Status != store.DomainVerified {
		t.Fatalf("domain %#v", domains[0])
	}
}

// Review 9 and 12: paths are lowercased; dot segments are refused.
func TestRoutePathsNormalized(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	c.verifiedDomain(t, "example.com")
	createImage(t, w, c.tok, c.sc.project.ID, "web", webSpec)
	status, env := w.do(c.tok, http.MethodPut, c.entryPath("web"), `{"routes":[{"hostname":"app.example.com","path_prefix":"/a/../b","port":8788}],"idempotency_key":"p0"}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid path_prefix: / or /seg[/seg…]")
	status, env = w.do(c.tok, http.MethodPut, c.entryPath("web"), `{"routes":[{"hostname":"app.example.com","port":8788,"no_store_paths":["/./x"]}],"idempotency_key":"p0"}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid no_store_paths: distinct path prefixes such as /evidence/")
	plan, _ := c.planAndConfirm(t, c.tok, "web", `[{"hostname":"app.example.com","path_prefix":"/API","port":8788,"no_store_paths":["/Evidence/"]}]`, "p1")
	if !strings.Contains(plan, "https://app.example.com/api → port 8788 (no-store /evidence/)") {
		t.Fatalf("plan %q", plan)
	}
}

// The edge's own names: project slug edge, and entry edge in project deploybot.
func TestEdgeReservedNames(t *testing.T) {
	c := newEdgeCast(t)
	w := c.w
	status, env := w.do(c.tok, http.MethodPost, "/v1/projects", `{"name":"Edge"}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgReservedProject)
	project, err := w.mem.CreateProject(context.Background(), c.sc.project.OrgID, c.owner.ID, "Deploybot", "deploybot", w.now)
	if err != nil || project.Slug != "deploybot" {
		t.Fatalf("project %#v %v", project, err)
	}
	status, env = w.do(c.tok, http.MethodPost, "/v1/projects/"+project.ID.String()+"/catalog", `{"name":"edge","kind":"image","spec":{"kind":"service","image":"nginx"}}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgReservedEntry)
	// The name is fine in any other project.
	createImage(t, w, c.tok, c.sc.project.ID, "edge", `{"kind":"service","image":"nginx"}`)
}
