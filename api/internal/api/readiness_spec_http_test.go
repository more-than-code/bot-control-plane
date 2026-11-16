package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Readiness from a spec (api-v1 v1.32 item 3).

func specReadinessPath(project string) string {
	return "/v1/projects/" + project + "/catalog/readiness"
}

func specReadinessBody(spec, host string) string {
	body := map[string]any{"spec": json.RawMessage(spec)}
	if host != "" {
		body["host_id"] = host
	}
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// payloadOf returns the payload JSON of an ok envelope, for an exact compare.
func payloadOf(t *testing.T, raw string) string {
	t.Helper()
	var env struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}
	return string(env.Payload)
}

// TestReadinessSpecMatchesEntry: for the same spec, the spec route answers
// exactly what the entry route answers once the entry is created, with and
// without a host.
func TestReadinessSpecMatchesEntry(t *testing.T) {
	f := newRegistryFixture(t)
	f.w.srv.Secrets = testBox(t)
	project := f.project
	host := f.sc.host.ID.String()
	helloAgent(t, f.w, f.sc.host, f.sc.credPlain, f.sc.credID, allowImages(), nil)
	f.private("bot", "stored-pass", true)
	putEnvFlag(t, f.w, f.owner, project, "app", "PLAIN=plain-value-zz\nURL=http://${MISSING_KEY}/x\nTOKEN=tok-value-zz\n", false)
	if !f.w.mem.SetNeedsValue(f.sc.project.ID, "app", "TOKEN") {
		t.Fatal("placeholder")
	}
	putEnvFlag(t, f.w, f.owner, project, "keys", "KEY1=k1-value-zz\n", true)
	if status, env, _ := putFile(t, f.w, f.owner, project, "conf.txt", "file-body-zz", nil); status != http.StatusOK {
		t.Fatalf("file %d %v", status, env)
	}

	cases := []struct{ name, spec string }{
		{"plain", `{"kind":"service","image":"docker.io/library/nginx"}`},
		{"sets-files", `{"kind":"service","image":"ghcr.io/joe/app:v1","env_sets":["app"],"files":[{"file":"conf.txt","target":"/etc/conf.txt"}]}`},
		{"secret-networks", `{"kind":"job","image":"ghcr.io/joe/gone:v1","secret_env":{"TOKEN":"keys.KEY1"},"networks":["shared"]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, h := range []string{"", host} {
				status, env, fromSpec := mustCall(t, f.w, f.owner, http.MethodPost, specReadinessPath(project), specReadinessBody(c.spec, h))
				mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
				// The spec route stores nothing.
				if _, err := f.w.mem.GetCatalogEntry(context.Background(), f.sc.project.ID, c.name); err == nil {
					t.Fatal("the spec route stored an entry")
				}
				createImage(t, f.w, f.owner, f.sc.project.ID, c.name, c.spec)
				_, _, fromEntry := readinessOf(t, f.w, f.owner, project, c.name, h)
				if payloadOf(t, fromSpec) != payloadOf(t, fromEntry) {
					t.Fatalf("host %q:\nspec  %s\nentry %s", h, payloadOf(t, fromSpec), payloadOf(t, fromEntry))
				}
				for _, leak := range []string{"plain-value-zz", "tok-value-zz", "k1-value-zz", "file-body-zz", "stored-pass"} {
					if strings.Contains(fromSpec, leak) {
						t.Fatalf("spec readiness leaked %s", leak)
					}
				}
				status, env, _ = mustCall(t, f.w, f.owner, http.MethodDelete, catalogItem(f.sc.project.ID, c.name), "")
				mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
			}
		})
	}
}

// TestReadinessSpecBeforeItExists: what a create would refuse because it
// does not exist yet (a set, a set key, a file) is a missing check instead.
func TestReadinessSpecBeforeItExists(t *testing.T) {
	f := newRegistryFixture(t)
	f.w.srv.Secrets = testBox(t)
	f.public(nil, "sha256:"+strings.Repeat("d", 64))
	putEnvFlag(t, f.w, f.owner, f.project, "keys", "OTHER=x\n", false)
	spec := `{"kind":"service","image":"docker.io/library/nginx","env_sets":["gone"],"secret_env":{"DB":"keys.DB_PASSWORD"},"files":[{"file":"new.conf","target":"/etc/new.conf"}]}`
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, catalogCollection(f.sc.project.ID), catalogImageBody("web", spec, ""))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "unknown env set")

	status, env, raw := mustCall(t, f.w, f.del, http.MethodPost, specReadinessPath(f.project), specReadinessBody(spec, ""))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	var body struct {
		Payload struct {
			Ready  bool             `json:"ready"`
			Checks []readinessCheck `json:"checks"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	if body.Payload.Ready || checkNames(body.Payload.Checks) != "env_sets:missing,env_values:missing,env_refs:ok,files:missing,registry:ok" {
		t.Fatalf("checks %s", checkNames(body.Payload.Checks))
	}
	if c := findCheck(t, body.Payload.Checks, "env_sets"); c.Fix["action"] != "create_env_set" || c.Fix["set"] != "gone" {
		t.Fatalf("env_sets %+v", c)
	}
	if c := findCheck(t, body.Payload.Checks, "env_values"); c.Message != "env DB_PASSWORD in set keys does not exist" || c.Fix["key"] != "DB_PASSWORD" {
		t.Fatalf("env_values %+v", c)
	}
	if c := findCheck(t, body.Payload.Checks, "files"); c.Fix["action"] != "add_file" || c.Fix["name"] != "new.conf" {
		t.Fatalf("files %+v", c)
	}
}

// TestReadinessSpecInvalid: an invalid spec answers the create texts.
func TestReadinessSpecInvalid(t *testing.T) {
	f := newRegistryFixture(t)
	for _, spec := range []string{
		`{"kind":"bogus","image":"nginx"}`,
		`{"kind":"service"}`,
		`{"kind":"service","image":"nginx","extra":1}`,
		`{"kind":"service","image":"Bad Ref"}`,
		`{"kind":"service","image":"nginx","env":{"PATH":"/x"}}`,
		`{"kind":"service","image":"nginx","env_sets":["Bad"]}`,
		`{"kind":"service","image":"nginx","files":[{"file":"bad/name","target":"/etc/a.conf"}]}`,
		`{"kind":"service","image":"nginx","ports":[{"host":0,"container":80}]}`,
		`[]`,
	} {
		status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, catalogCollection(f.sc.project.ID), catalogImageBody("web", spec, ""))
		if status != http.StatusBadRequest {
			t.Fatalf("create %s: %d %v", spec, status, env)
		}
		status2, env2, _ := mustCall(t, f.w, f.owner, http.MethodPost, specReadinessPath(f.project), specReadinessBody(spec, ""))
		if status2 != http.StatusBadRequest || env2.Code != env.Code || env2.Msg != env.Msg {
			t.Fatalf("spec %s: %d %s %q, create %s %q", spec, status2, env2.Code, env2.Msg, env.Code, env.Msg)
		}
	}
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, specReadinessPath(f.project), `{}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid spec")
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, specReadinessPath(f.project), `{"spec":{"kind":"service","image":"nginx"},"name":"x"}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid json")
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, specReadinessPath(f.project), specReadinessBody(`{"kind":"service","image":"nginx"}`, "nope"))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid id")
}

// TestReadinessSpecAccess: owner or admin, console or delegated; a viewer
// is refused and a stranger does not see the project.
func TestReadinessSpecAccess(t *testing.T) {
	f := newRegistryFixture(t)
	f.public(nil, "sha256:"+strings.Repeat("d", 64))
	spec := specReadinessBody(`{"kind":"service","image":"docker.io/library/nginx"}`, f.sc.host.ID.String())
	for _, tok := range []string{f.owner, f.admin, f.del} {
		status, env, _ := mustCall(t, f.w, tok, http.MethodPost, specReadinessPath(f.project), spec)
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	}
	status, env, _ := mustCall(t, f.w, f.viewer, http.MethodPost, specReadinessPath(f.project), spec)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	status, env, _ = mustCall(t, f.w, f.extra, http.MethodPost, specReadinessPath(f.project), spec)
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, specReadinessPath(f.project),
		specReadinessBody(`{"kind":"service","image":"docker.io/library/nginx"}`, "00000000-0000-4000-8000-000000000000"))
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
}
