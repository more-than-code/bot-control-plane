package mcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// api-v1 v1.33 over MCP: update_env_set and delete_env_set plans, used_by
// and private sets in list_env_sets, request_env_value with entry,
// shared_network_snippet, and the confirm rule for project_id without entry.
// The fake API sends values and previews; no tool result may carry them.

func v133Reply(r *http.Request, plan string) (fakeReply, bool) {
	base := "/v1/projects/" + projectID
	switch {
	case (r.Method == http.MethodPatch || r.Method == http.MethodDelete) && r.URL.Path == base+"/env-sets/app":
		return fakeReply{http.StatusAccepted, "ok", "ok", map[string]any{
			"confirmation_id": confirmationID, "plan": plan, "expires_at": "2026-10-15T00:05:00Z",
		}}, true
	case r.Method == http.MethodGet && r.URL.Path == base+"/env-sets":
		private := envSetBody()
		private["name"] = "@web"
		private["entry"] = "web"
		private["used_by"] = []any{"web"}
		shared := envSetBody()
		shared["name"] = "app"
		shared["used_by"] = []any{"api", "web"}
		return fakeReply{http.StatusOK, "ok", "ok", map[string]any{"items": []any{shared, private}}}, true
	case r.Method == http.MethodPost && r.URL.Path == base+"/catalog/web/value-requests":
		return fakeReply{http.StatusCreated, "ok", "ok", map[string]any{
			"request_id": valueRequestID, "url": "https://console.example/fill/" + valueRequestID, "expires_at": "2026-11-15T10:15:00Z",
		}}, true
	case r.Method == http.MethodGet && r.URL.Path == "/v1/value-requests/"+valueRequestID:
		return fakeReply{http.StatusOK, "ok", "ok", map[string]any{
			"request_id": valueRequestID, "project_id": projectID, "set": "@web", "key": "TOKEN",
			"state": "pending", "expires_at": "2026-11-15T10:15:00Z", "created_at": "2026-11-15T10:00:00Z",
		}}, true
	case r.Method == http.MethodGet && r.URL.Path == base+"/hosts/"+hostID+"/network-snippet":
		return fakeReply{http.StatusOK, "ok", "ok", map[string]any{
			"snippet": "# In /etc/bot-agent/runner.toml, add these items to the lists already there\nshared_networks = [\"dd=10.200.10.0/24\"]\nshared_network_projects = [\"dd:lab\"]\n",
		}}, true
	case r.Method == http.MethodPost && r.URL.Path == base+"/catalog/readiness":
		return fakeReply{http.StatusOK, "ok", "ok", map[string]any{
			"ready": false,
			"checks": []any{
				map[string]any{"check": "env_sets", "status": "ok", "message": "every env set the entry names exists", "fix": map[string]any{"action": "none"}},
				map[string]any{"check": "env_values", "status": "missing", "message": "env TOKEN in the entry's private set has no value yet; after the entry is created, fill it with request_env_value",
					"fix": map[string]any{"action": "fill_value", "key": "TOKEN"}},
				map[string]any{"check": "env_refs", "status": "ok", "message": "every ${KEY} reference resolves", "fix": map[string]any{"action": "none"}},
				map[string]any{"check": "env_pairs", "status": "warn", "message": "G_CLIENT_ID comes from the entry's env but G_CLIENT_SECRET comes from the entry's private set; keep a pair in one place", "fix": map[string]any{"action": "none"}},
			},
		}}, true
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/confirmations/"):
		if strings.HasPrefix(plan, "delete") {
			return fakeReply{http.StatusOK, "ok", "ok", map[string]any{"name": "app", "deleted": true}}, true
		}
		body := envSetBody()
		body["name"] = "app"
		return fakeReply{http.StatusOK, "ok", "ok", body}, true
	}
	return fakeReply{}, false
}

func TestUpdateEnvSetTool(t *testing.T) {
	fake := &fakeAPI{token: userToken}
	fake.setMode("v133")
	const plan = "update env set app: set the plain value of API_URL; add GEN (generated hex, secret); remove OLD; make secret (one way) MOVE"
	fake.setPlan(plan)
	mcpSrv, _ := newTestServer(t, fake)
	cs, _ := connectMCP(t, mcpSrv, userToken)

	before := fake.v1Count()
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, "bad_request: send at least one of set, add, remove or make_secret"},
		{map[string]any{"set": map[string]any{"A": "x"}, "remove": []any{"A"}}, "bad_request: key A is named more than once"},
		{map[string]any{"set": map[string]any{"A": "a\nb"}}, "bad_request: invalid env value"},
		{map[string]any{"add": []any{map[string]any{"key": "A", "value": envValueLeak}}}, "bad_request: a value can be given only for a plain key"},
	} {
		args := map[string]any{"project_id": projectID, "name": "app", "idempotency_key": "u-1"}
		for k, v := range tc.args {
			args[k] = v
		}
		res := mustCall(t, cs, "update_env_set", args)
		if !res.IsError || textOf(t, res) != tc.want || strings.Contains(textOf(t, res), envValueLeak) {
			t.Fatalf("%v: error=%v %q", tc.args, res.IsError, textOf(t, res))
		}
	}
	if fake.v1Count() != before {
		t.Fatal("rejected input called the api")
	}
	args := map[string]any{
		"project_id": projectID, "name": "app", "idempotency_key": "u-1",
		"set":         map[string]any{"API_URL": "https://new"},
		"add":         []any{map[string]any{"key": "GEN", "generate": map[string]any{"kind": "hex"}}},
		"remove":      []any{"OLD"},
		"make_secret": []any{"MOVE"},
	}
	res := mustCall(t, cs, "update_env_set", args)
	assertCatalogCall(t, fake, http.MethodPatch, "/v1/projects/"+projectID+"/env-sets/app", map[string]any{
		"add":             []any{map[string]any{"key": "GEN", "generate": map[string]any{"kind": "hex"}}},
		"idempotency_key": "u-1",
		"make_secret":     []any{"MOVE"},
		"remove":          []any{"OLD"},
		"set":             map[string]any{"API_URL": "https://new"},
	})
	assertPlan(t, res, plan)
	done := mustCall(t, cs, "confirm", map[string]any{"confirmation_id": confirmationID, "idempotency_key": "u-1", "project_id": projectID})
	if done.IsError || !strings.HasPrefix(textOf(t, done), "updated env set app; it has 2 keys.") {
		t.Fatalf("confirm %v %q", done.IsError, textOf(t, done))
	}
	assertNoEnvLeak(t, done)
}

func TestDeleteEnvSetTool(t *testing.T) {
	fake := &fakeAPI{token: userToken}
	fake.setMode("v133")
	const plan = "delete env set app and its 2 keys (A, B); open value requests for it stop working"
	fake.setPlan(plan)
	mcpSrv, _ := newTestServer(t, fake)
	cs, _ := connectMCP(t, mcpSrv, userToken)
	res := mustCall(t, cs, "delete_env_set", map[string]any{"project_id": projectID, "name": "app", "idempotency_key": "d-1"})
	assertCatalogCall(t, fake, http.MethodDelete, "/v1/projects/"+projectID+"/env-sets/app", map[string]any{"idempotency_key": "d-1"})
	assertPlan(t, res, plan)
	// project_id without entry is fine for an env set confirmation.
	done := mustCall(t, cs, "confirm", map[string]any{"confirmation_id": confirmationID, "idempotency_key": "d-1", "project_id": projectID})
	if done.IsError || textOf(t, done) != "deleted env set app." {
		t.Fatalf("confirm %v %q", done.IsError, textOf(t, done))
	}
	// entry without project_id is refused with a text that fits any plan.
	bad := mustCall(t, cs, "confirm", map[string]any{"confirmation_id": confirmationID, "idempotency_key": "d-1", "entry": "web"})
	if !bad.IsError || textOf(t, bad) != "bad_request: entry needs project_id: pass both for a program run, stop or restart or an image deploy or rollback, or leave entry out" {
		t.Fatalf("entry only %q", textOf(t, bad))
	}
	if strings.Contains(textOf(t, bad), "both required") {
		t.Fatal("old text")
	}
}

func TestListEnvSetsUsedByAndPrivate(t *testing.T) {
	fake := &fakeAPI{token: userToken}
	fake.setMode("v133")
	mcpSrv, _ := newTestServer(t, fake)
	cs, _ := connectMCP(t, mcpSrv, userToken)
	res := mustCall(t, cs, "list_env_sets", map[string]any{"project_id": projectID})
	if res.IsError {
		t.Fatalf("list %q", textOf(t, res))
	}
	call := fake.last()
	if call.RawQuery != "include=private" {
		t.Fatalf("query %q", call.RawQuery)
	}
	assertNoEnvLeak(t, res)
	raw, _ := json.Marshal(res.StructuredContent)
	for _, want := range []string{`"used_by":["api","web"]`, `"name":"@web"`, `"entry":"web"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
	text := textOf(t, res)
	if !strings.Contains(text, "app is used by api, web.") || !strings.Contains(text, "@web is private to entry web.") {
		t.Fatalf("summary %q", text)
	}
}

func TestRequestEnvValueForEntry(t *testing.T) {
	fake := &fakeAPI{token: userToken}
	fake.setMode("v133")
	mcpSrv, _ := newTestServer(t, fake)
	cs, _ := connectMCP(t, mcpSrv, userToken)
	before := fake.v1Count()
	for _, args := range []map[string]any{
		{"project_id": projectID, "key": "TOKEN"},
		{"project_id": projectID, "set": "svc", "entry": "web", "key": "TOKEN"},
		{"project_id": projectID, "entry": "Bad", "key": "TOKEN"},
	} {
		if res := mustCall(t, cs, "request_env_value", args); !res.IsError {
			t.Fatalf("%v accepted", args)
		}
	}
	if fake.v1Count() != before {
		t.Fatal("rejected input called the api")
	}
	res := mustCall(t, cs, "request_env_value", map[string]any{"project_id": projectID, "entry": "web", "key": "TOKEN"})
	if res.IsError || !strings.Contains(textOf(t, res), "https://console.example/fill/"+valueRequestID) || !strings.Contains(textOf(t, res), "@web.TOKEN") {
		t.Fatalf("request %v %q", res.IsError, textOf(t, res))
	}
	assertLast(t, fake, http.MethodPost, "/v1/projects/"+projectID+"/catalog/web/value-requests", "", `{"key":"TOKEN"}`)
	state := mustCall(t, cs, "get_env_value_request", map[string]any{"request_id": valueRequestID})
	if state.IsError || !strings.HasPrefix(textOf(t, state), "@web.TOKEN: pending.") {
		t.Fatalf("state %v %q", state.IsError, textOf(t, state))
	}
}

func TestSharedNetworkSnippetTool(t *testing.T) {
	fake := &fakeAPI{token: userToken}
	fake.setMode("v133")
	mcpSrv, _ := newTestServer(t, fake)
	cs, _ := connectMCP(t, mcpSrv, userToken)
	if res := mustCall(t, cs, "shared_network_snippet", map[string]any{"project_id": projectID, "host_id": hostID, "network": "Bad Name"}); !res.IsError {
		t.Fatal("bad network accepted")
	}
	res := mustCall(t, cs, "shared_network_snippet", map[string]any{"project_id": projectID, "host_id": hostID, "network": "dd", "subnet": "10.200.10.0/24"})
	if res.IsError || !strings.Contains(textOf(t, res), "shared_network_projects = [\"dd:lab\"]") {
		t.Fatalf("snippet %v %q", res.IsError, textOf(t, res))
	}
	call := fake.last()
	if call.Method != http.MethodGet || call.Path != "/v1/projects/"+projectID+"/hosts/"+hostID+"/network-snippet" || call.RawQuery != "network=dd&subnet=10.200.10.0%2F24" {
		t.Fatalf("call %s %s %s", call.Method, call.Path, call.RawQuery)
	}
}

// TestCheckReadinessSpecPrivateForms sends a spec with both private key
// forms unchanged and shows the fill key as needing a value, with the next
// step after the entry exists.
func TestCheckReadinessSpecPrivateForms(t *testing.T) {
	fake := &fakeAPI{token: userToken}
	fake.setMode("v133")
	mcpSrv, _ := newTestServer(t, fake)
	cs, _ := connectMCP(t, mcpSrv, userToken)
	spec := map[string]any{
		"kind": "service", "image": "nginx",
		"env": map[string]any{"G_CLIENT_ID": "id"},
		"secret_env": map[string]any{
			"TOKEN":           map[string]any{"fill": true},
			"G_CLIENT_SECRET": map[string]any{"fill": true},
			"KEY":             map[string]any{"generate": map[string]any{"kind": "hex"}},
		},
	}
	res := mustCall(t, cs, "check_readiness", map[string]any{"project_id": projectID, "spec": spec})
	if res.IsError {
		t.Fatalf("error %q", textOf(t, res))
	}
	call := fake.last()
	if call.Method != http.MethodPost || call.Path != "/v1/projects/"+projectID+"/catalog/readiness" {
		t.Fatalf("call %s %s", call.Method, call.Path)
	}
	var sent struct {
		Spec map[string]any `json:"spec"`
	}
	if err := json.Unmarshal([]byte(call.Body), &sent); err != nil {
		t.Fatal(err)
	}
	gotSecret, _ := json.Marshal(sent.Spec["secret_env"])
	wantSecret, _ := json.Marshal(spec["secret_env"])
	if string(gotSecret) != string(wantSecret) {
		t.Fatalf("secret_env sent %s", gotSecret)
	}
	text := textOf(t, res)
	for _, want := range []string{
		"env TOKEN in the entry's private set has no value yet; after the entry is created, fill it with request_env_value",
		"after create_catalog_entry and confirm, call request_env_value with project_id " + projectID + ", entry (the new entry's name) and key TOKEN",
		"G_CLIENT_ID comes from the entry's env but G_CLIENT_SECRET comes from the entry's private set; keep a pair in one place",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	// The API judges the object forms (invalid secret_env and the generate
	// texts); the tool forwards them unchanged.
}
