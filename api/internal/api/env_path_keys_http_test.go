package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// api-v1 v1.39 item 4: X_PRIVATE_KEY_FILE=/etc/x.pem saves for a program and
// an image from the console, and an image from a delegated (MCP) session
// (program entries are edited in the console only); API_KEY=abc is refused
// with the message that names the key.
func TestInlineEnvPathKeysHTTP(t *testing.T) {
	c := newCatalogCast(t)
	path := catalogCollection(c.sc.project.ID)
	const refused = "API_KEY looks like a secret: put it in secret_env or in a secret env-set key"
	imageSpec := func(key, val string) string {
		raw, _ := json.Marshal(map[string]any{"kind": "service", "image": "nginx", "env": map[string]string{key: val}})
		return string(raw)
	}

	// Console: saves directly.
	status, env := c.w.do(c.console, http.MethodPost, path, programBodyEnv("prog-file", "X_PRIVATE_KEY_FILE", "/etc/x.pem"))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	status, env = c.w.do(c.console, http.MethodPost, path, catalogImageBody("img-file", imageSpec("X_PRIVATE_KEY_FILE", "/etc/x.pem"), ""))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	status, env = c.w.do(c.console, http.MethodPost, path, programBodyEnv("prog-key", "API_KEY", "abc"))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", refused)
	status, env = c.w.do(c.console, http.MethodPost, path, catalogImageBody("img-key", imageSpec("API_KEY", "abc"), ""))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", refused)

	// Delegated: a plan, then the confirm saves it.
	for _, tc := range []struct{ name, body string }{
		{"dimg-file", catalogImageBody("dimg-file", imageSpec("X_PRIVATE_KEY_FILE", "/etc/x.pem"), "dimg-file")},
	} {
		status, env = c.w.do(c.tok, http.MethodPost, path, tc.body)
		mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
		id, _ := objectPayload(t, env)["confirmation_id"].(string)
		status, env = c.w.do(c.tok, http.MethodPost, "/v1/confirmations/"+id, confirmBody(tc.name))
		mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
		if catalogNameCount(t, c, tc.name) != 1 {
			t.Fatalf("%s not saved", tc.name)
		}
	}
	status, env = c.w.do(c.tok, http.MethodPost, path, catalogImageBody("dimg-key", imageSpec("API_KEY", "abc"), "dimg-key"))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", refused)
}

// api-v1 v1.39 item 4 (amended 2026-11-20): the env-set plan reminder uses
// the same path exemption as inline env. A plain X_PRIVATE_KEY_FILE gets no
// reminder in a create or an update plan; API_KEY still does.
func TestEnvSetPlanPathKeys(t *testing.T) {
	val := "/etc/x.pem"
	create := envSetCreatePlan("s", []envCreateKey{{Key: "X_PRIVATE_KEY_FILE", Value: &val}, {Key: "API_KEY", Value: &val}})
	if want := "create env set s with X_PRIVATE_KEY_FILE (plain, value given), API_KEY (plain, value given). API_KEY" + plainSecretWarning; create != want {
		t.Fatalf("create plan %q", create)
	}
	update := envSetUpdatePlan("s", envChangePlanBody{
		Set: map[string]string{"X_PRIVATE_KEY_FILE": val, "API_KEY": val},
		Add: []envCreateKey{{Key: "GITHUB_TOKEN_PATH", Value: &val}, {Key: "DB_PASSWORD", Value: &val}},
	})
	for _, key := range []string{"API_KEY", "DB_PASSWORD"} {
		if !strings.Contains(update, key+plainSecretWarning) {
			t.Fatalf("update plan misses %s: %q", key, update)
		}
	}
	for _, key := range []string{"X_PRIVATE_KEY_FILE", "GITHUB_TOKEN_PATH"} {
		if strings.Contains(update, key+" looks like a secret") {
			t.Fatalf("update plan warns %s: %q", key, update)
		}
	}
}
