package mcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// readinessLeak stands for a value or token the API must never send. The
// tools rebuild their results from allowlisted fields, so it never reaches
// the agent even when the fake API sends it.
const readinessLeak = "gho_LEAKEDtokenVALUE123456"

func readinessReply() map[string]any {
	return map[string]any{
		"ready": false,
		"checks": []any{
			map[string]any{"check": "env_sets", "status": "ok", "message": "every env set the entry names exists", "fix": map[string]any{"action": "none"}},
			map[string]any{"check": "env_values", "status": "missing", "message": "env SMTP_PASSWORD in set mail has no value yet; set it in the console",
				"fix": map[string]any{"action": "fill_value", "set": "mail", "key": "SMTP_PASSWORD", "value": readinessLeak}, "value": readinessLeak},
			map[string]any{"check": "files", "status": "missing", "message": "file conf.txt does not exist", "fix": map[string]any{"action": "add_file", "name": "conf.txt"}},
			map[string]any{"check": "registry", "status": "missing", "message": "private or missing; connect GitHub", "fix": map[string]any{"action": "connect_github", "registry": "ghcr.io"}},
			map[string]any{"check": "host_access", "status": "missing", "message": "this project has no deploy access to the host", "fix": map[string]any{"action": "grant_deploy_access", "host_id": hostID}},
			map[string]any{"check": "host_policy", "status": "missing", "message": "this host does not allow image ghcr.io/joe/app", "fix": map[string]any{"action": "host_policy_snippet", "host_id": hostID}},
			map[string]any{"check": "networks", "status": "ok", "message": "token=" + readinessLeak, "fix": map[string]any{"action": "none"}},
		},
	}
}

func TestReadinessTools(t *testing.T) {
	fake := &fakeAPI{token: userToken}
	mcpSrv, _ := newTestServer(t, fake)
	cs, _ := connectMCP(t, mcpSrv, userToken)

	res := mustCall(t, cs, "check_readiness", map[string]any{"project_id": projectID, "entry": imageEntry, "host_id": hostID})
	assertLast(t, fake, http.MethodGet, "/v1/projects/"+projectID+"/catalog/"+imageEntry+"/readiness", "host_id="+hostID, "")
	text := textOf(t, res)
	raw, _ := json.Marshal(res.StructuredContent)
	if res.IsError || strings.Contains(text, readinessLeak) || strings.Contains(string(raw), readinessLeak) {
		t.Fatalf("leak or error: %s %s", text, raw)
	}
	for _, phrase := range []string{
		"not ready: 5 check(s) missing",
		"env SMTP_PASSWORD in set mail has no value yet; set it in the console",
		"call request_env_value with project_id " + projectID + ", set mail and key SMTP_PASSWORD",
		"call request_file with project_id " + projectID + " and name conf.txt; the person adds the file in the browser.",
		"call connect_registry with project_id " + projectID + " and registry ghcr.io",
		"ask an org owner or admin to grant this project deploy access",
		"call host_policy_snippet",
	} {
		if !strings.Contains(text, phrase) {
			t.Errorf("text lacks %q: %s", phrase, text)
		}
	}
	var body struct {
		Ready  bool `json:"ready"`
		Checks []struct {
			Check string         `json:"check"`
			Fix   map[string]any `json:"fix"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Ready || len(body.Checks) != 7 {
		t.Fatalf("structured %s %v", raw, err)
	}
	if fix := body.Checks[1].Fix; len(fix) != 3 || fix["key"] != "SMTP_PASSWORD" || fix["set"] != "mail" {
		t.Fatalf("fill_value fix %v", fix)
	}
	// Without host_id no query is sent.
	res = mustCall(t, cs, "check_readiness", map[string]any{"project_id": projectID, "entry": imageEntry})
	assertLast(t, fake, http.MethodGet, "/v1/projects/"+projectID+"/catalog/"+imageEntry+"/readiness", "", "")
	if res = mustCall(t, cs, "check_readiness", map[string]any{"project_id": "nope", "entry": imageEntry}); !res.IsError {
		t.Fatal("bad project id accepted")
	}

	res = mustCall(t, cs, "image_access", map[string]any{"project_id": projectID, "image": "ghcr.io/joe/app:v1"})
	assertLast(t, fake, http.MethodGet, "/v1/projects/"+projectID+"/image-access", "image=ghcr.io%2Fjoe%2Fapp%3Av1", "")
	raw, _ = json.Marshal(res.StructuredContent)
	if res.IsError || string(raw) != `{"access":"private_no_login","login":"none","registry":"ghcr.io"}` || !strings.Contains(textOf(t, res), "Call connect_registry") {
		t.Fatalf("image_access %s %s", raw, textOf(t, res))
	}

	res = mustCall(t, cs, "host_policy_snippet", map[string]any{"project_id": projectID, "entry": imageEntry, "host_id": hostID})
	assertLast(t, fake, http.MethodGet, "/v1/projects/"+projectID+"/catalog/"+imageEntry+"/host-policy-snippet", "host_id="+hostID, "")
	raw, _ = json.Marshal(res.StructuredContent)
	text = textOf(t, res)
	if res.IsError || strings.Contains(string(raw), readinessLeak) || strings.Contains(string(raw), "rm -rf") ||
		!strings.Contains(string(raw), `sudo ./install.sh install --runner --project lab --image 'ghcr.io/joe/app'`) ||
		!strings.Contains(text, `images = ["ghcr.io/joe/app"]`) {
		t.Fatalf("snippet %s %s", raw, text)
	}
}
