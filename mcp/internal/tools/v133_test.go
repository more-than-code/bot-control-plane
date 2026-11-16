package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// api-v1 v1.33 item 5 and 6 in check_readiness: the env_pairs row passes
// through unredacted, and a private key's fill names the entry.
func TestReadinessPairsAndPrivateFill(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"ready": false,
		"checks": []any{
			map[string]any{"check": "env_values", "status": "missing", "message": "env TOKEN in set @web has no value yet; set it in the console",
				"fix": map[string]any{"action": "fill_value", "set": "@web", "key": "TOKEN"}},
			map[string]any{"check": "env_pairs", "status": "warn", "message": "GOOGLE_CLIENT_ID comes from the entry's env but GOOGLE_CLIENT_SECRET comes from env set google; keep a pair in one place",
				"fix": map[string]any{"action": "none"}},
		},
	})
	out, text, err := projectReadiness(payload, "p1", "web")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"call request_env_value with project_id p1, entry web and key TOKEN",
		"env TOKEN in set @web has no value yet",
		"GOOGLE_CLIENT_ID comes from the entry's env but GOOGLE_CLIENT_SECRET comes from env set google; keep a pair in one place",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	if !strings.Contains(string(out), `"set":"@web"`) {
		t.Fatalf("fix set dropped: %s", out)
	}
}
