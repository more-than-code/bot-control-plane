package mcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// api-v1 v1.27 items 4 and 5 over MCP: create_env_set sends key names only
// and returns the plan; list_env_sets and the confirm result keep names and
// flags only, even when the API (here a fake) sends a value or a preview.

const envValueLeak = "env-value-do-not-leak"

type fakeReply struct {
	status  int
	code    string
	msg     string
	payload any
}

func envSetBody() map[string]any {
	return map[string]any{
		"name": "daikou-db",
		"keys": []any{
			map[string]any{"key": "POSTGRES_PASSWORD", "is_secret": true, "value": nil, "preview": "••••", "reveal_pending": true, "needs_value": false},
			map[string]any{"key": "SMTP_HOST", "is_secret": false, "value": envValueLeak, "preview": nil, "reveal_pending": false, "needs_value": true},
		},
		"updated_at": "2026-11-15T00:00:00Z",
	}
}

func envSetsReply(r *http.Request, plan string) (fakeReply, bool) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/projects/"+projectID+"/env-sets":
		return fakeReply{http.StatusAccepted, "ok", "ok", map[string]any{
			"confirmation_id": confirmationID, "plan": plan, "expires_at": "2026-10-15T00:05:00Z",
		}}, true
	case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/"+projectID+"/env-sets":
		return fakeReply{http.StatusOK, "ok", "ok", map[string]any{"items": []any{envSetBody()}}}, true
	case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/"+projectID+"/env-sets/daikou-db":
		return fakeReply{http.StatusOK, "ok", "ok", envSetBody()}, true
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/confirmations/"):
		return fakeReply{http.StatusCreated, "ok", "ok", envSetBody()}, true
	}
	return fakeReply{}, false
}

func TestEnvSetTools(t *testing.T) {
	fake := &fakeAPI{token: userToken}
	fake.setMode("env-sets")
	const plan = "create env set daikou-db with POSTGRES_PASSWORD (generated password, 32 characters, secret), SMTP_HOST (empty; fill it in the console)"
	fake.setPlan(plan)
	mcpSrv, _ := newTestServer(t, fake)
	cs, _ := connectMCP(t, mcpSrv, userToken)

	before := fake.v1Count()
	for _, tc := range []struct {
		keys []any
		want string
	}{
		{[]any{}, "bad_request: keys must list 1 to 64 keys"},
		{[]any{map[string]any{"key": "A"}, map[string]any{"key": "A"}}, "bad_request: duplicate key A"},
		{[]any{map[string]any{"key": "A", "generate": map[string]any{"kind": "hex"}, "is_secret": false}}, "bad_request: a generated value is secret"},
		{[]any{map[string]any{"key": "A", "generate": map[string]any{"kind": "hex", "length": 20}}}, "bad_request: generate.length is only for kind password"},
	} {
		res := mustCall(t, cs, "create_env_set", map[string]any{
			"project_id": projectID, "name": "daikou-db", "keys": tc.keys, "idempotency_key": "es-1",
		})
		if !res.IsError || !strings.HasPrefix(textOf(t, res), "bad_request") {
			t.Fatalf("%v: error=%v %q", tc.keys, res.IsError, textOf(t, res))
		}
	}
	// A value field is refused by the schema before the API is called.
	res := mustCall(t, cs, "create_env_set", map[string]any{
		"project_id": projectID, "name": "daikou-db", "idempotency_key": "es-1",
		"keys": []any{map[string]any{"key": "A", "value": envValueLeak}},
	})
	if !res.IsError {
		t.Fatal("a value field was accepted")
	}
	if fake.v1Count() != before {
		t.Fatal("rejected input called the api")
	}

	keys := []any{
		map[string]any{"key": "POSTGRES_PASSWORD", "generate": map[string]any{"kind": "password"}},
		map[string]any{"key": "SMTP_HOST", "is_secret": false},
	}
	res = mustCall(t, cs, "create_env_set", map[string]any{
		"project_id": projectID, "name": "daikou-db", "keys": keys, "idempotency_key": "es-1",
	})
	assertCatalogCall(t, fake, http.MethodPost, "/v1/projects/"+projectID+"/env-sets", map[string]any{
		"name": "daikou-db", "keys": keys, "idempotency_key": "es-1",
	})
	assertPlan(t, res, plan)

	created := mustCall(t, cs, "confirm", map[string]any{"confirmation_id": confirmationID, "idempotency_key": "es-1"})
	if created.IsError || !strings.HasPrefix(textOf(t, created), "created env set daikou-db with 2 keys.") {
		t.Fatalf("confirm %v %q", created.IsError, textOf(t, created))
	}
	assertNoEnvLeak(t, created)

	for _, args := range []map[string]any{{"project_id": projectID}, {"project_id": projectID, "name": "daikou-db"}} {
		listed := mustCall(t, cs, "list_env_sets", args)
		if listed.IsError {
			t.Fatalf("list %q", textOf(t, listed))
		}
		assertNoEnvLeak(t, listed)
		raw, _ := json.Marshal(listed.StructuredContent)
		if !strings.Contains(string(raw), `"needs_value":true`) || !strings.Contains(string(raw), `"reveal_pending":true`) {
			t.Fatalf("flags missing %s", raw)
		}
		if !strings.Contains(textOf(t, listed), "1 key still needs a value in the console.") {
			t.Fatalf("summary %q", textOf(t, listed))
		}
	}
}

func assertNoEnvLeak(t *testing.T, res any) {
	t.Helper()
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), envValueLeak) || strings.Contains(string(raw), "preview") || strings.Contains(string(raw), `"value"`) {
		t.Fatalf("value or preview leaked: %s", raw)
	}
}

const needsValueRefusal = "env SMTP_PASSWORD in set mail has no value yet; set it in the console"

// api-v1 v1.27 item 6: the refusal reaches deploy and run tool results
// unchanged (it names a key and a set, never a value).
func TestNeedsValueRefusalPassesThrough(t *testing.T) {
	fake := &fakeAPI{token: userToken}
	fake.setMode("needs-value")
	mcpSrv, _ := newTestServer(t, fake)
	cs, _ := connectMCP(t, mcpSrv, userToken)
	for _, tool := range []string{"deploy_entry", "run_entry"} {
		res := mustCall(t, cs, tool, map[string]any{
			"project_id": projectID, "entry": imageEntry, "host_id": hostID, "idempotency_key": tool + "-1",
		})
		if !res.IsError || textOf(t, res) != "bad_request: "+needsValueRefusal {
			t.Fatalf("%s: error=%v %q", tool, res.IsError, textOf(t, res))
		}
	}
}
