package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

const (
	netReasonOld    = "this host's agent does not support shared networks; upgrade it to 0.2.9"
	netReasonClosed = "shared network dd is not open to this project on this host"
)

func netPrograms(nets []store.SharedNetwork) *store.HostPrograms {
	return &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, Images: true, Docker: true, PolicyHash: "abc", SharedNetworks: nets}
}

func networksOf(t *testing.T, task map[string]any) (any, bool) {
	t.Helper()
	frame, _ := task["spec"].(map[string]any)
	nested, _ := frame["spec"].(map[string]any)
	v, ok := nested["networks"]
	return v, ok
}

// TestNetworksHTTP covers api-v1 v1.25 items 1, 3 and 4 over HTTP.
func TestNetworksHTTP(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()

	for _, bad := range []string{`["DD"]`, `["dd","dd"]`, `["a","b","c","d","e"]`, `"dd"`} {
		status, env, _ := mustCall(t, w, tok, http.MethodPost, "/v1/projects/"+project+"/catalog", `{"name":"bad","kind":"image","spec":{"kind":"service","image":"nginx","networks":`+bad+`}}`)
		mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid networks")
	}
	empty := createImage(t, w, tok, sc.project.ID, "empty", `{"kind":"service","image":"nginx","networks":[]}`)
	if spec, _ := empty["spec"].(map[string]any); spec == nil || spec["networks"] != nil {
		t.Fatalf("[] was stored %#v", empty["spec"])
	}
	web := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","networks":["dd"]}`)
	spec, _ := web["spec"].(map[string]any)
	if got, _ := spec["networks"].([]any); len(got) != 1 || got[0] != "dd" {
		t.Fatalf("stored networks %#v", spec["networks"])
	}
	createImage(t, w, tok, sc.project.ID, "bare", `{"kind":"service","image":"nginx"}`)

	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	log := watchTasks(t, conn, func(msg map[string]any) map[string]any {
		return imageDeployReply(msg, shaDigest(2))
	})

	// An agent without shared_networks: entries without networks deploy
	// and carry no networks key; entries with networks are refused.
	status, env, _ := postImage(t, w, tok, project, "bare", "deploy", host, "bare-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if _, ok := networksOf(t, log.raw(0)); ok {
		t.Fatal("bare task sent networks")
	}
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "web-old", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", netReasonOld)
	status, env, _ = postImage(t, w, tok, project, "web", "rollback", host, "web-old-roll", map[string]any{"digest": shaDigest(2)})
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", netReasonOld)
	if log.count() != 1 {
		t.Fatal("refused deploy sent a task")
	}

	// Defined but not open to this project, then not defined at all.
	setPrograms(t, w, sc.host.ID, netPrograms([]store.SharedNetwork{{Name: "dd", Subnet: "10.200.10.0/24", Projects: []string{"other"}}}))
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "web-closed", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", netReasonClosed)
	setPrograms(t, w, sc.host.ID, netPrograms([]store.SharedNetwork{}))
	status, env, _ = postImage(t, w, tok, project, "web", "rollback", host, "web-none-roll", map[string]any{"digest": shaDigest(2)})
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", netReasonClosed)
	if log.count() != 1 {
		t.Fatal("refused deploy sent a task")
	}

	// Open: the task carries spec.networks. Run, stop and restart are not gated.
	open := netPrograms([]store.SharedNetwork{{Name: "dd", Subnet: "10.200.10.0/24", Projects: []string{"other", sc.project.Slug}}})
	setPrograms(t, w, sc.host.ID, open)
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "web-open", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if got, ok := networksOf(t, log.raw(1)); !ok || len(got.([]any)) != 1 || got.([]any)[0] != "dd" {
		t.Fatalf("task networks %#v", got)
	}
	status, env, _ = postImage(t, w, tok, project, "bare", "deploy", host, "bare-2", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if _, ok := networksOf(t, log.raw(2)); ok {
		t.Fatal("capable host received networks for an entry without it")
	}

	// Delegated: the plan names the network; confirm re-checks the gate.
	delegated := tokenKind(t, w, owner.ID, store.KindDelegated)
	status, env, _ = postImage(t, w, delegated, project, "web", "deploy", host, "web-del", nil)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	payload := objectPayload(t, env)
	if plan, _ := payload["plan"].(string); !strings.HasSuffix(plan, "; also joins shared network(s): dd") {
		t.Fatalf("plan %q", plan)
	}
	id, _ := payload["confirmation_id"].(string)
	setPrograms(t, w, sc.host.ID, netPrograms(nil))
	before := log.count()
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("web-del"))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", netReasonOld)
	setPrograms(t, w, sc.host.ID, netPrograms([]store.SharedNetwork{{Name: "dd", Subnet: "10.200.10.0/24", Projects: []string{}}}))
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("web-del"))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", netReasonClosed)
	if log.count() != before {
		t.Fatal("refused confirm sent a task")
	}
	setPrograms(t, w, sc.host.ID, open)
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("web-del"))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if log.count() != before+1 {
		t.Fatal("confirm did not dispatch")
	}
}

// TestNetworksVisibility covers api-v1 v1.25 item 2 on the three host views.
func TestNetworksVisibility(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	member := w.user("a@ex.com", "A")
	sc := w.scene(owner, member, w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	nets := []store.SharedNetwork{
		{Name: "dd", Subnet: "10.200.10.0/24", Projects: []string{"other", sc.project.Slug}},
		{Name: "solo", Subnet: "10.200.11.0/24", Projects: []string{"other"}},
		{Name: "empty", Subnet: "10.200.12.0/24", Projects: []string{}},
	}
	setPrograms(t, w, sc.host.ID, netPrograms(nets))
	// Keys are sorted: the test re-marshals the decoded map.
	full := `[{"name":"dd","projects":["other","proj"],"subnet":"10.200.10.0/24"},{"name":"solo","projects":["other"],"subnet":"10.200.11.0/24"},{"name":"empty","projects":[],"subnet":"10.200.12.0/24"}]`
	open := `[{"name":"dd","subnet":"10.200.10.0/24"}]`
	listPath := "/v1/projects/" + sc.project.ID.String() + "/hosts"
	getPath := "/v1/hosts/" + sc.host.ID.String()
	orgPath := "/v1/orgs/" + sc.project.OrgID.String() + "/hosts"

	session := w.token(owner.ID)
	delegated := tokenKind(t, w, owner.ID, store.KindDelegated)
	memberTok := w.token(member.ID)
	cases := []struct {
		name, tok, path, want string
	}{
		{"org admin list", session, listPath, full},
		{"org admin get", session, getPath, full},
		{"org admin org view", session, orgPath, full},
		{"delegated list", delegated, listPath, open},
		{"delegated get", delegated, getPath, open},
		{"delegated org view", delegated, orgPath, `[]`},
		{"member list", memberTok, listPath, open},
		{"member get", memberTok, getPath, open},
	}
	for _, tc := range cases {
		if got := sharedNetworksJSON(t, w, tc.tok, tc.path, sc.host.ID); got != tc.want {
			t.Fatalf("%s: %s", tc.name, got)
		}
	}
	// The stored list is not changed by a filtered view.
	if got := sharedNetworksJSON(t, w, session, getPath, sc.host.ID); got != full {
		t.Fatalf("store changed: %s", got)
	}

	setPrograms(t, w, sc.host.ID, netPrograms(nil))
	for _, tc := range cases {
		if got := sharedNetworksJSON(t, w, tc.tok, tc.path, sc.host.ID); got != "null" {
			t.Fatalf("%s old agent: %s", tc.name, got)
		}
	}
}

func sharedNetworksJSON(t *testing.T, w *world, tok, path string, hostID uuid.UUID) string {
	t.Helper()
	status, env, _ := mustCall(t, w, tok, http.MethodGet, path, "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	var hostDoc map[string]any
	if items, ok := objectPayload(t, env)["items"].([]any); ok {
		for _, item := range items {
			if m, _ := item.(map[string]any); m["id"] == hostID.String() {
				hostDoc = m
			}
		}
	} else {
		hostDoc = objectPayload(t, env)
	}
	programs, _ := hostDoc["programs"].(map[string]any)
	raw, err := json.Marshal(programs["shared_networks"])
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestNetworksPlans(t *testing.T) {
	bare := mustPlanImage(t, "web", `{"kind":"service","image":"nginx"}`, nil, nil)
	withNets := mustPlanImage(t, "web", `{"kind":"service","image":"nginx","networks":["dd","edge"]}`, nil, nil)
	if plan := catalogCreatePlan("proj", "web", bare); strings.Contains(plan, "shared networks") {
		t.Fatalf("bare create %q", plan)
	}
	if plan := catalogCreatePlan("proj", "web", withNets); !strings.HasSuffix(plan, "; shared networks: dd, edge") {
		t.Fatalf("create %q", plan)
	}
	if plan := catalogUpdatePlan("web", withNets, bare); plan != "will update image entry web: shared networks: dd, edge → none" {
		t.Fatalf("update remove %q", plan)
	}
	if plan := catalogUpdatePlan("web", bare, withNets); plan != "will update image entry web: shared networks: none → dd, edge" {
		t.Fatalf("update add %q", plan)
	}
	if plan := catalogUpdatePlan("web", bare, bare); plan != "will update image entry web: no field changes" {
		t.Fatalf("update none %q", plan)
	}
	if plan := imagePlan("deploy", "web", "nginx", "pi4", []string{"dd"}); plan != "will deploy web (nginx) on pi4; also joins shared network(s): dd" {
		t.Fatalf("deploy %q", plan)
	}
	if plan := imagePlan("rollback", "web", "sha256:x", "pi4", []string{"dd", "edge"}); plan != "will deploy web (sha256:x) on pi4; also joins shared network(s): dd, edge" {
		t.Fatalf("rollback %q", plan)
	}
	for _, action := range []string{"run", "stop", "restart", "remove"} {
		if plan := imagePlan(action, "web", "nginx", "pi4", []string{"dd"}); strings.Contains(plan, "shared") {
			t.Fatalf("%s %q", action, plan)
		}
	}
	if plan := imagePlan("deploy", "web", "nginx", "pi4", nil); plan != "will deploy web (nginx) on pi4" {
		t.Fatalf("deploy bare %q", plan)
	}
}

// TestNetworksPayloadGolden pins the task spec of an entry without networks
// to the v0.2.31 payload (api-v1 v1.25 item 4; acceptance criterion 1).
func TestNetworksPayloadGolden(t *testing.T) {
	const raw = `{"kind":"service","image":"ghcr.io/acme/web:1.2","command":["serve","--port","8080"],"ports":[{"host":8080,"container":8080,"public":false}],"env":{"A":"1"},"env_sets":["app"],"volumes":[{"name":"data","target":"/data"}],"healthcheck":{"test":["true"]},"logging":{"max_size_mb":10,"max_files":3},"memory_mb":256,"user":"1000"}`
	const want = `{"command":["serve","--port","8080"],"entrypoint":null,"env":{"A":"1"},"env_sets":["app"],"healthcheck":{"interval_sec":30,"retries":3,"start_period_sec":0,"test":["true"],"timeout_sec":5},"healthy_after_sec":30,"image":"ghcr.io/acme/web:1.2","kind":"service","logging":{"max_files":3,"max_size_mb":10},"memory_mb":256,"ports":[{"container":8080,"host":8080,"public":false}],"restart":"unless-stopped","secret_env":{},"timeout_sec":0,"user":"1000","volumes":[{"name":"data","read_only":false,"target":"/data"}]}`
	host := store.Host{Programs: netPrograms([]store.SharedNetwork{})}
	for _, extra := range []string{"", `,"networks":[]`, `,"networks":null`} {
		img, err := catalog.BuildImage("web", []byte(strings.TrimSuffix(raw, "}")+extra+"}"), catalog.SetKeys{"app": {"X"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		spec, err := storedSpec(img, host)
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%q payload %s", extra, got)
		}
	}
}
