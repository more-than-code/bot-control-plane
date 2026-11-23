package mcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// imageTagsReply is GET /v1/projects/{id}/image-tags (api-v1 v1.46 item 1).
// Mode "tags-v116" answers like an API before v1.46: no order, no times.
func imageTagsReply(mode string) map[string]any {
	if mode == "tags-v116" {
		return map[string]any{
			"items":     []any{map[string]any{"tag": "1.1"}, map[string]any{"tag": "1.0"}},
			"truncated": false,
		}
	}
	return map[string]any{
		"items": []any{
			map[string]any{"tag": "1.1", "updated_at": "2026-11-22T11:00:00.123Z", "digest": "sha256:not-shown"},
			map[string]any{"tag": "bad tag‮"},
			map[string]any{"tag": "1.0", "updated_at": "2026-11-21T09:00:00Z"},
			map[string]any{"tag": "latest"},
		},
		"truncated": false,
		"order":     "time",
		"token":     "registry-token-do-not-leak",
	}
}

func TestListImageTagsForEntry(t *testing.T) {
	fake := &fakeAPI{token: userToken}
	mcpSrv, _ := newTestServer(t, fake)
	cs, _ := connectMCP(t, mcpSrv, userToken)

	before := fake.v1Count()
	for _, bad := range []map[string]any{
		{"project_id": projectID},
		{"project_id": projectID, "entry": imageEntry, "image": "nginx"},
		{"project_id": projectID, "image": "nginx latest"},
		{"project_id": projectID, "entry": imageEntry, "limit": 0},
		{"project_id": projectID, "entry": imageEntry, "limit": 51},
		{"project_id": "not-a-uuid", "entry": imageEntry},
	} {
		if res := mustCall(t, cs, "list_image_tags", bad); !res.IsError {
			t.Fatalf("accepted %#v", bad)
		}
	}
	if fake.v1Count() != before {
		t.Fatal("rejected input called the api")
	}

	res := mustCall(t, cs, "list_image_tags", map[string]any{"project_id": projectID, "entry": imageEntry})
	if res.IsError {
		t.Fatalf("list %s", textOf(t, res))
	}
	// The entry's stored image is read, then its repository's tags.
	assertLast(t, fake, http.MethodGet, "/v1/projects/"+projectID+"/image-tags", "image=ghcr.io%2Fexample%2Fwebadmin%3A1.0", "")
	if got, want := textOf(t, res), "3 tags of ghcr.io/example/webadmin, newest push first. First: 1.1. Entry stores ghcr.io/example/webadmin:1.0."; got != want {
		t.Fatalf("text %q, want %q", got, want)
	}
	raw, _ := json.Marshal(structuredMap(t, res))
	for _, leak := range []string{"registry-token-do-not-leak", "not-shown", "bad tag"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("result contains %s: %s", leak, raw)
		}
	}
	want := `{"current":"ghcr.io/example/webadmin:1.0","items":[` +
		`{"current":false,"image":"ghcr.io/example/webadmin:1.1","tag":"1.1","updated_at":"2026-11-22T11:00:00Z"},` +
		`{"current":true,"image":"ghcr.io/example/webadmin:1.0","tag":"1.0","updated_at":"2026-11-21T09:00:00Z"},` +
		`{"current":false,"image":"ghcr.io/example/webadmin:latest","tag":"latest"}],` +
		`"order":"time","repository":"ghcr.io/example/webadmin","truncated":false}`
	if string(raw) != want {
		t.Fatalf("result %s\nwant   %s", raw, want)
	}

	// limit cuts the list and says more exist.
	res = mustCall(t, cs, "list_image_tags", map[string]any{"project_id": projectID, "entry": imageEntry, "limit": 1})
	if got := structuredMap(t, res); got["truncated"] != true || len(got["items"].([]any)) != 1 {
		t.Fatalf("limit 1: %#v", got)
	}

	// A program entry has no image tags and the registry is not asked.
	before = fake.v1Count()
	res = mustCall(t, cs, "list_image_tags", map[string]any{"project_id": projectID, "entry": entryName})
	if !res.IsError || !strings.Contains(textOf(t, res), "has no image tags") {
		t.Fatalf("program error=%v text %q", res.IsError, textOf(t, res))
	}
	if fake.v1Count() != before+1 || fake.last().Path != "/v1/projects/"+projectID+"/catalog/"+entryName {
		t.Fatal("program entry reached image-tags")
	}
}

func TestListImageTagsForImage(t *testing.T) {
	fake := &fakeAPI{token: userToken}
	mcpSrv, _ := newTestServer(t, fake)
	cs, _ := connectMCP(t, mcpSrv, userToken)
	fake.setMode("tags-v116")

	res := mustCall(t, cs, "list_image_tags", map[string]any{"project_id": projectID, "image": "ghcr.io/acme/api:v1@sha256:" + strings.Repeat("a", 64)})
	if res.IsError {
		t.Fatalf("list %s", textOf(t, res))
	}
	assertLast(t, fake, http.MethodGet, "/v1/projects/"+projectID+"/image-tags", "image=ghcr.io%2Facme%2Fapi%3Av1%40sha256%3A"+strings.Repeat("a", 64), "")
	if got, want := textOf(t, res), "2 tags of ghcr.io/acme/api, by version name (push times unknown). First: 1.1."; got != want {
		t.Fatalf("text %q, want %q", got, want)
	}
	got := structuredMap(t, res)
	if got["order"] != "name" || got["current"] != nil {
		t.Fatalf("result %#v", got)
	}
	if first := got["items"].([]any)[0].(map[string]any); first["current"] != nil || first["image"] != "ghcr.io/acme/api:1.1" {
		t.Fatalf("first %#v", first)
	}
}
