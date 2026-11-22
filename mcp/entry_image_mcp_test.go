package mcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const newImageRef = "ghcr.io/example/webadmin@sha256:7ae70445f971af10ef94d07f7f3693d2bd6917c88e60b430a829c731a6cfdba5"

// fullImageEntry is GET /v1/projects/{id}/catalog/{name} for an image entry
// with every setting an image-only change must keep.
func fullImageEntry() map[string]any {
	return map[string]any{
		"id": "99999999-9999-4999-8999-999999999999", "project_id": projectID,
		"name": imageEntry, "kind": "image",
		"command_display":  "ghcr.io/example/webadmin:1.0",
		"spec_hash":        "hash-not-shown",
		"last_deployed_at": "2026-11-22T10:00:00Z",
		"private_keys":     []any{map[string]any{"key": "SESSION_KEY", "is_secret": true}},
		"spec": map[string]any{
			"kind":              "service",
			"image":             "ghcr.io/example/webadmin:1.0",
			"command":           []any{"serve", "--port", "3000"},
			"entrypoint":        nil,
			"user":              "1000:1000",
			"healthy_after_sec": 30,
			"timeout_sec":       0,
			"memory_mb":         256,
			"restart":           "unless-stopped",
			"ports":             []any{map[string]any{"host": 3031, "container": 3000, "public": false, "extra": "x"}},
			"env":               map[string]any{"NODE_ENV": "production", "DATA_DIR": "/data"},
			"secret_env":        map[string]any{"DB_URL": "base.DB_URL", "SESSION_KEY": "@webadmin.SESSION_KEY"},
			"env_sets":          []any{"base"},
			"volumes":           []any{map[string]any{"name": "webdata", "target": "/data", "read_only": false}},
			"files": []any{
				map[string]any{"file": "app.toml", "target": "/etc/app/app.toml", "content": "file-content-do-not-leak"},
				map[string]any{"file": "ca.pem", "target": "/etc/ssl/ca.pem"},
			},
			"healthcheck": map[string]any{"test": []any{"wget", "-qO-", "http://127.0.0.1:3000/health"}, "interval_sec": 10, "timeout_sec": 3, "retries": 3, "start_period_sec": 5},
			"logging":     map[string]any{"max_size_mb": 10, "max_files": 3},
			"health_policy": map[string]any{
				"on_deploy_failure": "rollback", "on_unhealthy": "restart", "unhealthy_after_sec": 45, "max_actions_per_hour": 3,
			},
			"networks":       []any{"dd"},
			"internal_field": "internal-do-not-leak",
		},
		"my_actions": []any{"deploy", "logs", "status", "edit"},
	}
}

func TestGetCatalogEntryFullSpec(t *testing.T) {
	fake := &fakeAPI{token: userToken}
	mcpSrv, _ := newTestServer(t, fake)
	cs, _ := connectMCP(t, mcpSrv, userToken)

	res := mustCall(t, cs, "get_catalog_entry", map[string]any{"project_id": projectID, "name": imageEntry})
	if res.IsError {
		t.Fatalf("get %s", textOf(t, res))
	}
	assertLast(t, fake, http.MethodGet, "/v1/projects/"+projectID+"/catalog/"+imageEntry, "", "")
	if got := textOf(t, res); got != "catalog entry webadmin (image service): ghcr.io/example/webadmin:1.0" {
		t.Fatalf("text %q", got)
	}
	got := structuredMap(t, res)
	raw, _ := json.Marshal(got)
	for _, leak := range []string{"file-content-do-not-leak", "internal-do-not-leak", "hash-not-shown", "private_keys", `"extra"`} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("result contains %s: %s", leak, raw)
		}
	}
	spec, _ := got["spec"].(map[string]any)
	if spec == nil {
		t.Fatalf("no spec: %s", raw)
	}
	want := map[string]string{
		"env":         `{"DATA_DIR":"/data","NODE_ENV":"production"}`,
		"secret_env":  `{"DB_URL":"base.DB_URL","SESSION_KEY":"@webadmin.SESSION_KEY"}`,
		"volumes":     `[{"name":"webdata","read_only":false,"target":"/data"}]`,
		"files":       `[{"file":"app.toml","target":"/etc/app/app.toml"},{"file":"ca.pem","target":"/etc/ssl/ca.pem"}]`,
		"healthcheck": `{"interval_sec":10,"retries":3,"start_period_sec":5,"test":["wget","-qO-","http://127.0.0.1:3000/health"],"timeout_sec":3}`,
		"ports":       `[{"container":3000,"host":3031,"public":false}]`,
		"command":     `["serve","--port","3000"]`,
		"networks":    `["dd"]`,
		"image":       `"ghcr.io/example/webadmin:1.0"`,
		"timeout_sec": `0`,
		"user":        `"1000:1000"`,
	}
	for key, w := range want {
		b, _ := json.Marshal(spec[key])
		if string(b) != w {
			t.Errorf("spec.%s = %s want %s", key, b, w)
		}
	}

	res = mustCall(t, cs, "get_catalog_entry", map[string]any{"project_id": projectID, "name": entryName})
	if res.IsError {
		t.Fatalf("program %s", textOf(t, res))
	}
	prog := structuredMap(t, res)
	if _, ok := prog["spec"]; ok {
		t.Fatalf("program spec returned: %#v", prog)
	}
	if b, _ := json.Marshal(prog); strings.Contains(string(b), "s3cret") || strings.Contains(string(b), "raw-token") {
		t.Fatalf("program leaked: %s", b)
	}
}

func TestSetEntryImageKeepsSpec(t *testing.T) {
	fake := &fakeAPI{token: userToken}
	mcpSrv, _ := newTestServer(t, fake)
	cs, _ := connectMCP(t, mcpSrv, userToken)

	before := fake.v1Count()
	for _, bad := range []map[string]any{
		{"project_id": projectID, "name": imageEntry, "image": "nginx latest", "idempotency_key": "img-1"},
		{"project_id": projectID, "name": imageEntry, "image": "nginx‮", "idempotency_key": "img-1"},
		{"project_id": projectID, "name": imageEntry, "image": "nginx", "idempotency_key": "bad key"},
		{"project_id": projectID, "name": imageEntry, "image": "nginx", "idempotency_key": "img-1", "spec": map[string]any{}},
	} {
		res := mustCall(t, cs, "set_entry_image", bad)
		if !res.IsError {
			t.Fatalf("accepted %#v", bad)
		}
	}
	if fake.v1Count() != before {
		t.Fatal("rejected input called the api")
	}

	res := mustCall(t, cs, "set_entry_image", map[string]any{
		"project_id": projectID, "name": imageEntry, "image": newImageRef, "idempotency_key": "img-1",
	})
	if res.IsError {
		t.Fatalf("set %s", textOf(t, res))
	}
	calls := fake.snapshot()
	if len(calls) < 2 {
		t.Fatalf("calls %d", len(calls))
	}
	read := calls[len(calls)-2]
	if read.Method != http.MethodGet || read.Path != "/v1/projects/"+projectID+"/catalog/"+imageEntry {
		t.Fatalf("read %s %s", read.Method, read.Path)
	}
	stored := fullImageEntry()["spec"].(map[string]any)
	stored["image"] = newImageRef
	assertCatalogCall(t, fake, http.MethodPut, "/v1/projects/"+projectID+"/catalog/"+imageEntry, map[string]any{
		"kind": "image", "spec": stored, "idempotency_key": "img-1",
	})
	if got := structuredMap(t, res); got["confirmation_id"] == nil {
		t.Fatalf("no confirmation: %#v", got)
	}

	before = fake.v1Count()
	res = mustCall(t, cs, "set_entry_image", map[string]any{
		"project_id": projectID, "name": entryName, "image": newImageRef, "idempotency_key": "img-2",
	})
	if !res.IsError || !strings.Contains(textOf(t, res), "program entries are edited in the console") {
		t.Fatalf("program error=%v text %q", res.IsError, textOf(t, res))
	}
	if fake.v1Count() != before+1 || fake.last().Method != http.MethodGet {
		t.Fatal("program entry was written")
	}
}
