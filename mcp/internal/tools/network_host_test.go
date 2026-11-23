package tools

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// api-v1 v1.50 item 1: spec.network in the write schema, local checks, and
// pass-through on create, update and get.
func TestNetworkHostSchemaAndShape(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(catalogWriteSchema), &schema); err != nil {
		t.Fatal(err)
	}
	spec := schema["properties"].(map[string]any)["spec"].(map[string]any)
	prop, ok := spec["properties"].(map[string]any)["network"].(map[string]any)
	if !ok || prop["description"] == nil {
		t.Fatal("network missing from spec schema")
	}
	if enum, _ := prop["enum"].([]any); len(enum) != 2 || enum[0] != "host" || enum[1] != nil {
		t.Fatalf("network enum %#v", prop["enum"])
	}
	for _, ok := range []string{
		`{"kind":"service","image":"nginx","network":"host"}`,
		`{"kind":"service","image":"nginx","network":null}`,
		`{"kind":"service","image":"nginx","network":"host","networks":null}`,
	} {
		if err := validateImageSpec(json.RawMessage(ok)); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		`{"kind":"service","image":"nginx","network":"bridge"}`,
		`{"kind":"service","image":"nginx","network":""}`,
		`{"kind":"service","image":"nginx","network":1}`,
		`{"kind":"service","image":"nginx","network":"host","networks":["dd"]}`,
	} {
		if err := validateImageSpec(json.RawMessage(bad)); err == nil || !strings.HasPrefix(err.Error(), "network ") {
			t.Fatalf("%s: %v", bad, err)
		}
	}
}

func TestCatalogWriteToolsForwardNetwork(t *testing.T) {
	const project = "8f1c2d3e-4b5a-4c6d-8e7f-901234567890"
	spec := map[string]any{"kind": "service", "image": "nginx", "network": "host"}
	for _, tc := range []struct{ tool, method string }{
		{"create_catalog_entry", http.MethodPost},
		{"update_catalog_entry", http.MethodPut},
	} {
		res, seen := callWriteTool(t, tc.tool, map[string]any{"project_id": project, "name": "web", "idempotency_key": "net-1", "spec": spec})
		if res.IsError || len(seen) != 1 || seen[0].Method != tc.method {
			t.Fatalf("%s: %v %#v", tc.tool, res.IsError, seen)
		}
		var body struct {
			Spec map[string]json.RawMessage `json:"spec"`
		}
		if err := json.Unmarshal([]byte(seen[0].Body), &body); err != nil || string(body.Spec["network"]) != `"host"` {
			t.Fatalf("%s forwarded %s", tc.tool, seen[0].Body)
		}
		bad := map[string]any{"kind": "service", "image": "nginx", "network": "bridge"}
		res, seen = callWriteTool(t, tc.tool, map[string]any{"project_id": project, "name": "web", "idempotency_key": "net-2", "spec": bad})
		if !res.IsError || len(seen) != 0 {
			t.Fatalf("%s bad network error=%v requests %d", tc.tool, res.IsError, len(seen))
		}
	}
}

func TestGetCatalogEntryKeepsNetwork(t *testing.T) {
	out, _, err := projectFullCatalogEntry(json.RawMessage(`{"name":"web","kind":"image","command_display":"nginx","my_actions":[],
		"spec":{"kind":"service","image":"nginx","network":"host"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Spec map[string]any `json:"spec"`
	}
	if err := json.Unmarshal(out, &body); err != nil || body.Spec["network"] != "host" {
		t.Fatalf("%s %v", out, err)
	}
}

// api-v1 v1.50 item 9: list_catalog shows network ("host" or null).
func TestProjectCatalogNetwork(t *testing.T) {
	out, err := projectCatalog([]byte(`{"items":[
		{"name":"api","kind":"image","command_display":"x:1","spec":{"kind":"service","image":"x:1","network":"host"},"my_actions":[]},
		{"name":"web","kind":"image","command_display":"x:1","spec":{"kind":"service","image":"x:1","network":"bridge"},"my_actions":[]},
		{"name":"bare","kind":"image","command_display":"x:1","spec":{"kind":"service","image":"x:1"},"my_actions":[]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(out, &body); err != nil || len(body.Items) != 3 {
		t.Fatalf("%s %v", out, err)
	}
	for i, want := range []any{"host", nil, nil} {
		if v, ok := body.Items[i]["network"]; !ok || v != want {
			t.Fatalf("item %d network %#v", i, body.Items[i]["network"])
		}
	}
}
