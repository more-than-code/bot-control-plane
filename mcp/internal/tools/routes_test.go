package tools

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// api-v1 v1.35: the routes plan passes through, the confirm result is
// summarized as routes, and readiness accepts the routes check.
func TestRoutesResults(t *testing.T) {
	plan := "routes for serve:\nadd https://defectdrainer.com/ → port 8788 (client IP from Cloudflare; no-store /evidence/) on ip-10-0-30-14\nremove https://old.example.com/"
	payload, _ := json.Marshal(map[string]any{"confirmation_id": strings.Repeat("A", 43), "plan": plan, "expires_at": "2026-11-17T00:05:00Z", "extra": "dropped"})
	res := confirmationRoutes(http.StatusAccepted, payload)
	if res.IsError || !strings.Contains(toolText(t, res), plan) {
		t.Fatalf("plan %q", toolText(t, res))
	}
	if strings.Contains(toolText(t, res), "dropped") {
		t.Fatal("unlisted field shown")
	}
	tricky, _ := json.Marshal(map[string]any{"confirmation_id": strings.Repeat("A", 43), "plan": "a\u0007b\nc"})
	if text := toolText(t, confirmationRoutes(http.StatusAccepted, tricky)); !strings.Contains(text, "a\uFFFDb\nc") {
		t.Fatalf("controls %q", text)
	}
	if res := confirmationRoutes(http.StatusOK, payload); !res.IsError {
		t.Fatal("200 accepted as a plan")
	}
	done, _ := json.Marshal(map[string]any{"entry": "serve", "routes": []any{
		map[string]any{"entry": "serve", "hostname": "defectdrainer.com", "path_prefix": "/", "port": 8788, "state": "pending"},
	}})
	res = actedOrPending(http.StatusOK, done)
	if res.IsError || !strings.Contains(toolText(t, res), "routes for serve set: 1 routes") || !strings.Contains(toolText(t, res), "https://defectdrainer.com/ → serve port 8788 (pending)") {
		t.Fatalf("confirm %q", toolText(t, res))
	}
	ready, _ := json.Marshal(map[string]any{"ready": true, "checks": []any{
		map[string]any{"check": "routes", "status": "warn", "message": "the edge is off on pi3; an org admin turns it on in the console", "fix": map[string]any{"action": "none"}},
	}})
	if _, text, err := projectReadiness(ready, "p1", "serve"); err != nil || !strings.Contains(text, "routes: warn") {
		t.Fatalf("readiness %q %v", text, err)
	}
}

func TestValidateRoutes(t *testing.T) {
	slash, bad := "/", "api"
	cf, other := "cloudflare", "akamai"
	good := []routeArg{{Hostname: "app.example.com", PathPrefix: &slash, Port: 8788, ClientIPFrom: &cf, NoStorePaths: []string{"/evidence/"}}}
	if err := validateRoutes(good); err != nil {
		t.Fatal(err)
	}
	if err := validateRoutes([]routeArg{}); err != nil {
		t.Fatal("[] is valid")
	}
	for _, r := range [][]routeArg{
		nil,
		{{Hostname: "App.example.com", Port: 1}},
		{{Hostname: "localhost", Port: 1}},
		{{Hostname: "a.example.com", Port: 0}},
		{{Hostname: "a.example.com", Port: 1, PathPrefix: &bad}},
		{{Hostname: "a.example.com", Port: 1, ClientIPFrom: &other}},
		{{Hostname: "a.example.com", Port: 1, NoStorePaths: []string{"evidence"}}},
	} {
		if validateRoutes(r) == nil {
			t.Fatalf("accepted %#v", r)
		}
	}
}

func TestHostsSummaryEdge(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"items": []any{
		map[string]any{"name": "a", "status": "online", "access_level": "deploy", "edge": map[string]any{"enabled": true, "status": "applied"}},
		map[string]any{"name": "b", "status": "online", "access_level": "deploy", "edge": map[string]any{"enabled": false, "status": "off"}, "programs": map[string]any{"edge": map[string]any{"enabled": true}}},
		map[string]any{"name": "c", "status": "online", "access_level": "deploy"},
	}})
	text := summarizeHosts(payload, false)
	if !strings.Contains(text, "a online (deploy) (edge on, applied)") || !strings.Contains(text, "b online (deploy) (edge off)") || strings.Contains(text, "c online (deploy) (edge") {
		t.Fatalf("summary %q", text)
	}
}
