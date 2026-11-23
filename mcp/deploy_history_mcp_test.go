package mcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const (
	failedDeployID = "33333333-3333-4333-8333-333333333333"
	okDeployID     = "44444444-4444-4444-8444-444444444444"
)

// reasonDeployHistory is GET …/deploys with api-v1 v1.47 reasons.
func reasonDeployHistory() map[string]any {
	return map[string]any{"items": []any{
		map[string]any{
			"id": failedDeployID, "digest": digestEF, "outcome": "rolled_back", "via": "mcp",
			"started_at": "2026-11-23T01:00:00Z", "finished_at": "2026-11-23T01:01:00Z",
			"reason":       "health check GET :8080/health answered 503; rolled back; the previous version is running again",
			"final_status": map[string]any{"state": "running", "exit_code": nil, "restart_count": 0},
			"spec_hash":    "not-shown",
		},
		map[string]any{
			"id": okDeployID, "digest": digestAB, "outcome": "succeeded", "via": "console",
			"started_at": "2026-11-22T01:00:00Z", "finished_at": "2026-11-22T01:01:00Z",
			"reason": nil,
		},
	}}
}

// deployDetailReply is GET …/deploys/{id} (api-v1 v1.47 item 3).
func deployDetailReply(mode string) (int, string, string, any) {
	row := reasonDeployHistory()["items"].([]any)[0]
	var lines any = []any{"starting", "panic: password=hunter2hunter2 is wrong"}
	if mode == "detail-no-logs" {
		lines = nil
	}
	return http.StatusOK, "ok", "", map[string]any{"deploy": row, "lines": lines}
}

func TestDeployHistoryTool(t *testing.T) {
	fake := &fakeAPI{token: userToken}
	fake.setMode("history-reason")
	mcpSrv, _ := newTestServer(t, fake)
	cs, _ := connectMCP(t, mcpSrv, userToken)

	before := fake.v1Count()
	for _, bad := range []map[string]any{
		{"project_id": projectID, "entry": imageEntry},
		{"project_id": projectID, "entry": imageEntry, "host_id": hostID, "deploy_id": "x"},
		{"project_id": projectID, "entry": imageEntry, "host_id": hostID, "limit": 21},
	} {
		if res := mustCall(t, cs, "deploy_history", bad); !res.IsError {
			t.Fatalf("accepted %#v", bad)
		}
	}
	if fake.v1Count() != before {
		t.Fatal("rejected input called the api")
	}

	res := mustCall(t, cs, "deploy_history", map[string]any{"project_id": projectID, "entry": imageEntry, "host_id": hostID})
	if res.IsError {
		t.Fatalf("history %s", textOf(t, res))
	}
	// Without deploy_id the newest finished row's tail is read.
	assertLast(t, fake, http.MethodGet, "/v1/projects/"+projectID+"/catalog/"+imageEntry+"/deploys/"+failedDeployID, "host_id="+hostID, "")
	text := textOf(t, res)
	for _, want := range []string{
		"2 recent deploys, newest first:",
		"rolled_back sha256:efefefefefef 2026-11-23T01:01:00Z via mcp container running: health check GET :8080/health answered 503; rolled back",
		"log tail of " + failedDeployID + " (rolled_back):",
		"panic: password=[REDACTED]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text %q lacks %q", text, want)
		}
	}
	raw, _ := json.Marshal(structuredMap(t, res))
	for _, leak := range []string{"hunter2", "not-shown", "spec_hash"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("result contains %s: %s", leak, raw)
		}
	}
	got := structuredMap(t, res)
	detail := got["detail"].(map[string]any)
	if len(detail["lines"].([]any)) != 2 || detail["final_status"].(map[string]any)["state"] != "running" {
		t.Fatalf("detail %#v", detail)
	}

	// A chosen deploy_id and limit; a caller without logs sees no lines.
	fake.setMode("detail-no-logs")
	res = mustCall(t, cs, "deploy_history", map[string]any{"project_id": projectID, "entry": imageEntry, "host_id": hostID, "deploy_id": okDeployID, "limit": 1})
	if res.IsError || !strings.Contains(textOf(t, res), "not shown: needs the logs or deploy permission") {
		t.Fatalf("no logs %q", textOf(t, res))
	}
	assertLast(t, fake, http.MethodGet, "/v1/projects/"+projectID+"/catalog/"+imageEntry+"/deploys/"+okDeployID, "host_id="+hostID, "")
}
