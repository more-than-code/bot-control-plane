package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

const hostNetReason = "this host does not allow host networking (runner.toml host_network_projects)"

func hostNetPrograms(allowed bool) *store.HostPrograms {
	return &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, Images: true, Docker: true, PolicyHash: "abc", HostNetwork: allowed}
}

func networkOf(task map[string]any) (any, bool) {
	frame, _ := task["spec"].(map[string]any)
	nested, _ := frame["spec"].(map[string]any)
	v, ok := nested["network"]
	return v, ok
}

// TestHostNetworkHTTP covers api-v1 v1.50 item 1 over HTTP: write rules,
// the stored and returned field, the unsupported_agent gate on deploy,
// rollback and run (console and confirm), and the task spec.
func TestHostNetworkHTTP(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()

	for bad, msg := range map[string]string{
		`"network":"bridge"`:                 "invalid network",
		`"network":""`:                       "invalid network",
		`"network":"host","networks":["dd"]`: "network host cannot join shared networks",
	} {
		status, env, _ := mustCall(t, w, tok, http.MethodPost, "/v1/projects/"+project+"/catalog", `{"name":"bad","kind":"image","spec":{"kind":"service","image":"nginx",`+bad+`}}`)
		mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msg)
	}
	web := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","network":"host","ports":[{"host":8090,"container":8090}]}`)
	if spec, _ := web["spec"].(map[string]any); spec["network"] != "host" {
		t.Fatalf("stored spec %#v", web["spec"])
	}
	if got := createImageGet(t, w, tok, sc.project.ID, "web"); got["spec"].(map[string]any)["network"] != "host" {
		t.Fatalf("GET spec %#v", got["spec"])
	}
	bare := createImage(t, w, tok, sc.project.ID, "bare", `{"kind":"service","image":"nginx"}`)
	if _, ok := bare["spec"].(map[string]any)["network"]; ok {
		t.Fatal("bare entry stored network")
	}

	// A hello with host_network is accepted (not read as a bad programs
	// object), and the Host answer does not show it.
	programs := allowImages()
	programs["host_network"] = false
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, programs, nil)
	log := watchTasks(t, conn, func(msg map[string]any) map[string]any {
		if msg["kind"] == "image_start" {
			return imageStatusReply(msg)
		}
		return imageDeployReply(msg, shaDigest(2))
	})
	stored, err := w.mem.GetHost(t.Context(), sc.host.ID)
	if err != nil || stored.Programs == nil || !stored.Programs.Images || stored.Programs.HostNetwork {
		t.Fatalf("hello programs %#v %v", stored.Programs, err)
	}

	// Without programs.host_network: deploy, rollback and run are refused
	// 409 before any task; an entry without network still deploys.
	status, env, _ := postImage(t, w, tok, project, "bare", "deploy", host, "bare-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if _, ok := networkOf(log.raw(0)); ok {
		t.Fatal("bare task sent network")
	}
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "web-1", nil)
	mustHTTP(t, status, env, http.StatusConflict, "unsupported_agent", hostNetReason)
	status, env, _ = postImage(t, w, tok, project, "web", "rollback", host, "web-2", map[string]any{"digest": shaDigest(2)})
	mustHTTP(t, status, env, http.StatusConflict, "unsupported_agent", hostNetReason)
	status, env, _ = postImage(t, w, tok, project, "web", "run", host, "web-3", nil)
	mustHTTP(t, status, env, http.StatusConflict, "unsupported_agent", hostNetReason)
	if log.count() != 1 {
		t.Fatal("refused action sent a task")
	}
	if n := auditCount(auditRows(t, w, sc.project.ID), "catalog.deploy", "denied"); n != 1 {
		t.Fatalf("deploy deny audit rows %d", n)
	}
	// Stop and status are not gated.
	status, env, _ = postImage(t, w, tok, project, "web", "status", host, "", nil)
	if status == http.StatusConflict {
		t.Fatalf("status gated %#v", env)
	}

	// Delegated: the plan is issued; confirm re-checks the gate.
	setPrograms(t, w, sc.host.ID, hostNetPrograms(true))
	delegated := tokenKind(t, w, owner.ID, store.KindDelegated)
	status, env, _ = postImage(t, w, delegated, project, "web", "deploy", host, "web-del", nil)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	id, _ := objectPayload(t, env)["confirmation_id"].(string)
	setPrograms(t, w, sc.host.ID, hostNetPrograms(false))
	before := log.count()
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("web-del"))
	mustHTTP(t, status, env, http.StatusConflict, "unsupported_agent", hostNetReason)
	if log.count() != before {
		t.Fatal("refused confirm sent a task")
	}

	// Allowed: the task spec carries network "host"; run passes too.
	setPrograms(t, w, sc.host.ID, hostNetPrograms(true))
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "web-4", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if got, ok := networkOf(log.raw(log.count() - 1)); !ok || got != "host" {
		t.Fatalf("task network %#v", got)
	}
	status, env, _ = postImage(t, w, tok, project, "web", "run", host, "web-5", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")

	// The Host answer never carries host_network.
	_, _, raw := mustCall(t, w, tok, http.MethodGet, "/v1/hosts/"+host, "")
	if strings.Contains(raw, "host_network") {
		t.Fatalf("host answer shows host_network: %s", raw)
	}
}

// The plans name the network.
func TestHostNetworkPlans(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	delegated := tokenKind(t, w, owner.ID, store.KindDelegated)
	project := sc.project.ID.String()
	status, env, _ := mustCall(t, w, delegated, http.MethodPost, "/v1/projects/"+project+"/catalog", `{"name":"web","kind":"image","spec":{"kind":"service","image":"nginx","network":"host"},"idempotency_key":"plan-1"}`)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	if plan, _ := objectPayload(t, env)["plan"].(string); !strings.Contains(plan, "network host") {
		t.Fatalf("create plan %q", plan)
	}
}

// api-v1 v1.50 item 6: an entry with no env at all still gets env_hash (the
// hash of its empty env) on deploy and rollback to an env_hash agent, so a
// changed host env file is never unchanged.
func TestEnvHashWithoutEnvSets(t *testing.T) {
	programs := allowImages()
	programs["env_hash"] = true
	w, sc, tok, log := refsWorld(t, programs)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	createImage(t, w, tok, sc.project.ID, "bare", `{"kind":"service","image":"nginx","network":"host"}`)
	setPrograms(t, w, sc.host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, Images: true, Docker: true, PolicyHash: "abc", EnvHash: true, HostNetwork: true})
	empty, err := w.srv.Secrets.EnvHash(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	status, env, _ := postImage(t, w, tok, project, "bare", "deploy", host, "eh-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if got, _ := lastSpec(t, log)["env_hash"].(string); got != empty {
		t.Fatalf("deploy env_hash %q", got)
	}
	digest, _ := objectPayload(t, env)["deploy"].(map[string]any)["digest"].(string)
	status, env, _ = postImage(t, w, tok, project, "bare", "rollback", host, "eh-2", map[string]any{"digest": digest})
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if got, _ := lastSpec(t, log)["env_hash"].(string); got != empty {
		t.Fatalf("rollback env_hash %q", got)
	}
}
