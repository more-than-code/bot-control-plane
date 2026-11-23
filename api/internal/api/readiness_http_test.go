package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/agentws"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// private serves a Bearer-protected registry on every host: the realm is
// <host>/token, user:pass gets the "good" token, anyone else "anon", and
// only "good" may read a manifest. exists false answers 404 to "good".
func (f *registryFixture) private(user, pass string, exists bool) {
	f.fake.Handle(func(w http.ResponseWriter, r *http.Request) {
		f.count(r)
		if r.URL.Path == "/token" {
			if u, p, ok := r.BasicAuth(); ok {
				if u != user || p != pass {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				fmt.Fprint(w, `{"token":"good"}`)
				return
			}
			fmt.Fprint(w, `{"token":"anon"}`)
			return
		}
		switch r.Header.Get("Authorization") {
		case "":
			w.Header().Set("WWW-Authenticate", `Bearer realm="https://`+r.Host+`/token",service="`+r.Host+`"`)
			w.WriteHeader(http.StatusUnauthorized)
		case "Bearer good":
			if !exists {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("e", 64))
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	})
}

func accessPath(project, image string) string {
	return "/v1/projects/" + project + "/image-access?image=" + url.QueryEscape(image)
}

func accessOf(t *testing.T, f *registryFixture, tok, image string) map[string]any {
	t.Helper()
	status, env, raw := mustCall(t, f.w, tok, http.MethodGet, accessPath(f.project, image), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if strings.Contains(raw, "stored-pass") {
		t.Fatal("token in the image-access answer")
	}
	p := objectPayload(t, env)
	if len(p) != 3 {
		t.Fatalf("fields %v", p)
	}
	return p
}

func TestImageAccess(t *testing.T) {
	f := newRegistryFixture(t)
	buf := captureLog(f.w)
	f.w.srv.Secrets = testBox(t)

	f.public([]string{"v1"}, "sha256:"+strings.Repeat("d", 64))
	if p := accessOf(t, f, f.owner, "docker.io/library/nginx"); p["access"] != "public" || p["login"] != "none" || p["registry"] != "docker.io" {
		t.Fatalf("public %v", p)
	}
	// Delegated sessions may ask; viewers and strangers may not.
	if p := accessOf(t, f, f.del, "docker.io/library/nginx"); p["access"] != "public" {
		t.Fatalf("delegated %v", p)
	}
	status, env, _ := mustCall(t, f.w, f.viewer, http.MethodGet, accessPath(f.project, "nginx"), "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	status, env, _ = mustCall(t, f.w, f.extra, http.MethodGet, accessPath(f.project, "nginx"), "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodGet, accessPath(f.project, "Bad Ref"), "")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid image")
	// A host outside the allowlist is unknown, not an error.
	if p := accessOf(t, f, f.owner, "registry.example.org/app"); p["access"] != "unknown" {
		t.Fatalf("refused host %v", p)
	}

	f.private("bot", "stored-pass", true)
	if p := accessOf(t, f, f.owner, "ghcr.io/joe/private:v1"); p["access"] != "private_no_login" || p["login"] != "none" {
		t.Fatalf("no login %v", p)
	}
	putRegistry(t, f.w, f.owner, f.project, "ghcr.io", "bot", "stored-pass")
	// A new login is looked up again at once (the cache key holds the login).
	if p := accessOf(t, f, f.owner, "ghcr.io/joe/private:v1"); p["access"] != "private_ok" || p["login"] != "manual" {
		t.Fatalf("stored login %v", p)
	}
	putRegistry(t, f.w, f.owner, f.project, "ghcr.io", "bot", "wrong-pass")
	if p := accessOf(t, f, f.owner, "ghcr.io/joe/private:v1"); p["access"] != "login_failed" {
		t.Fatalf("refused login %v", p)
	}
	f.private("bot", "wrong-pass", false)
	if p := accessOf(t, f, f.owner, "ghcr.io/joe/gone:v1"); p["access"] != "not_found" {
		t.Fatalf("authenticated 404 %v", p)
	}
	// An unreachable registry and a timeout are unknown.
	f.fake.Handle(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	if p := accessOf(t, f, f.owner, "quay.io/joe/down:v1"); p["access"] != "unknown" {
		t.Fatalf("5xx %v", p)
	}
	f.fake.Handle(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(7 * time.Second):
		}
	})
	if p := accessOf(t, f, f.owner, "quay.io/joe/slow:v1"); p["access"] != "unknown" {
		t.Fatalf("timeout %v", p)
	}
	if strings.Contains(buf.String(), "stored-pass") || strings.Contains(buf.String(), "wrong-pass") {
		t.Fatal("token in the log")
	}
}

func TestImageAccessRateLimit(t *testing.T) {
	f := newRegistryFixture(t)
	f.public(nil, "sha256:"+strings.Repeat("d", 64))
	for i := 0; i < 20; i++ {
		accessOf(t, f, f.owner, fmt.Sprintf("docker.io/library/app%d", i))
	}
	status, env, hdr := callHeaders(t, f.w, f.owner, accessPath(f.project, "docker.io/library/one-more"))
	mustHTTP(t, status, env, http.StatusTooManyRequests, "rate_limited", "rate limited")
	if hdr.Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	// A cached answer does not count.
	accessOf(t, f, f.owner, "docker.io/library/app0")
}

// readinessCheck is one row of the readiness answer.
type readinessCheck struct {
	Check   string         `json:"check"`
	Status  string         `json:"status"`
	Message string         `json:"message"`
	Fix     map[string]any `json:"fix"`
}

func readinessOf(t *testing.T, w *world, tok, project, entry, host string) (bool, []readinessCheck, string) {
	t.Helper()
	path := "/v1/projects/" + project + "/catalog/" + entry + "/readiness"
	if host != "" {
		path += "?host_id=" + host
	}
	status, env, raw := mustCall(t, w, tok, http.MethodGet, path, "")
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
	return body.Payload.Ready, body.Payload.Checks, raw
}

func checkNames(checks []readinessCheck) string {
	out := make([]string, len(checks))
	for i, c := range checks {
		out[i] = c.Check + ":" + c.Status
	}
	return strings.Join(out, ",")
}

func findCheck(t *testing.T, checks []readinessCheck, name string) readinessCheck {
	t.Helper()
	for _, c := range checks {
		if c.Check == name {
			return c
		}
	}
	t.Fatalf("no %s in %s", name, checkNames(checks))
	return readinessCheck{}
}

// replaceRaw edits the stored ImageSpec directly, as an older entry whose
// set or file was later removed would look.
func replaceRaw(t *testing.T, w *world, entry store.CatalogEntry, edit func(map[string]any)) {
	t.Helper()
	var spec map[string]any
	if err := json.Unmarshal(entry.ImageRaw, &spec); err != nil {
		t.Fatal(err)
	}
	edit(spec)
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.mem.ReplaceCatalogEntry(context.Background(), entry.ProjectID, entry.Name, store.CatalogWrite{
		Name: entry.Name, Kind: "image", ImageRaw: raw, SpecHash: entry.SpecHash, Actor: entry.CreatedBy, At: w.now,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessChecksAndFixes(t *testing.T) {
	f := newRegistryFixture(t)
	f.w.srv.Secrets = testBox(t)
	ctx := context.Background()
	project := f.project
	host := f.sc.host.ID.String()
	helloAgent(t, f.w, f.sc.host, f.sc.credPlain, f.sc.credID, allowImages(), nil)
	f.private("bot", "stored-pass", true)

	putEnvFlag(t, f.w, f.owner, project, "app", "PLAIN=plain-value-zz\nURL=http://${MISSING_KEY}/x\nTOKEN=tok-value-zz\n", false)
	if !f.w.mem.SetNeedsValue(f.sc.project.ID, "app", "TOKEN") {
		t.Fatal("placeholder")
	}
	if status, env, _ := putFile(t, f.w, f.owner, project, "conf.txt", "file-body-zz", nil); status != http.StatusOK {
		t.Fatalf("file %d %v", status, env)
	}
	createImage(t, f.w, f.owner, f.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/app:v1","env_sets":["app"],"files":[{"file":"conf.txt","target":"/etc/conf.txt"}]}`)
	entry, err := f.w.mem.GetCatalogEntry(ctx, f.sc.project.ID, "web")
	if err != nil {
		t.Fatal(err)
	}
	replaceRaw(t, f.w, entry, func(spec map[string]any) {
		spec["env_sets"] = []any{"app", "gone"}
		spec["files"] = append(spec["files"].([]any), map[string]any{"file": "missing.txt", "target": "/etc/missing.txt"})
	})

	ready, checks, raw := readinessOf(t, f.w, f.owner, project, "web", host)
	want := "env_sets:missing,env_values:missing,env_refs:missing,env_pairs:ok,files:missing,registry:missing,host_access:ok,host_online:ok,host_images:ok,host_policy:unknown,networks:ok"
	if ready || checkNames(checks) != want {
		t.Fatalf("checks %s ready %v", checkNames(checks), ready)
	}
	for _, leak := range []string{"plain-value-zz", "tok-value-zz", "file-body-zz", "stored-pass"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("readiness leaked %s", leak)
		}
	}
	type fx = map[string]any
	expect := map[string]struct {
		msg string
		fix fx
	}{
		"env_sets":    {"env set gone does not exist", fx{"action": "create_env_set", "set": "gone"}},
		"env_values":  {"env TOKEN in set app has no value yet; set it in the console", fx{"action": "fill_value", "set": "app", "key": "TOKEN"}},
		"env_refs":    {"env URL references ${MISSING_KEY}, which is not set for this entry", fx{"action": "fill_value", "set": "app", "key": "MISSING_KEY"}},
		"files":       {"file missing.txt does not exist", fx{"action": "add_file", "name": "missing.txt"}},
		"registry":    {"private or missing; connect GitHub", fx{"action": "connect_github", "registry": "ghcr.io"}},
		"host_policy": {"the host's image allowlist is checked at deploy", fx{"action": "none"}},
	}
	for name, e := range expect {
		c := findCheck(t, checks, name)
		got, _ := json.Marshal(c.Fix)
		wantFix, _ := json.Marshal(e.fix)
		if c.Message != e.msg || string(got) != string(wantFix) {
			t.Fatalf("%s: %q %s, want %q %s", name, c.Message, got, e.msg, wantFix)
		}
	}
	// Without a host only the entry checks run.
	if _, checks, _ := readinessOf(t, f.w, f.owner, project, "web", ""); checkNames(checks) != "env_sets:missing,env_values:missing,env_refs:missing,env_pairs:ok,files:missing,registry:missing" {
		t.Fatalf("no host %s", checkNames(checks))
	}

	// Fix each item.
	putEnvFlag(t, f.w, f.owner, project, "app", "PLAIN=plain-value-zz\nURL=http://${MISSING_KEY}/x\nTOKEN=tok-value-zz\nMISSING_KEY=db\n", false)
	entry, _ = f.w.mem.GetCatalogEntry(ctx, f.sc.project.ID, "web")
	replaceRaw(t, f.w, entry, func(spec map[string]any) {
		spec["env_sets"] = []any{"app"}
		spec["files"] = []any{map[string]any{"file": "conf.txt", "target": "/etc/conf.txt"}}
	})
	putRegistry(t, f.w, f.owner, project, "ghcr.io", "bot", "stored-pass")
	ready, checks, _ = readinessOf(t, f.w, f.owner, project, "web", host)
	if !ready || strings.Contains(checkNames(checks), "missing") {
		t.Fatalf("after fixes %s", checkNames(checks))
	}
	if c := findCheck(t, checks, "registry"); c.Message != "private image; the stored login works" {
		t.Fatalf("registry ok %q", c.Message)
	}

	// The stored login is refused.
	putRegistry(t, f.w, f.owner, project, "ghcr.io", "bot", "wrong-pass")
	_, checks, _ = readinessOf(t, f.w, f.owner, project, "web", host)
	if c := findCheck(t, checks, "registry"); c.Status != "missing" || c.Message != "the stored login for ghcr.io was refused; reconnect GitHub" || c.Fix["action"] != "connect_github" {
		t.Fatalf("login failed %+v", c)
	}
	putRegistry(t, f.w, f.owner, project, "ghcr.io", "bot", "stored-pass")

	// A policy_denied deploy makes host_policy missing until a later deploy.
	owner := f.w.user("owner@ex.com", "Owner")
	deny := store.AuditRow{
		At: f.w.now.Add(time.Minute), ActorUserID: &owner.ID, Via: "console", ProjectID: &f.sc.project.ID, HostID: &f.sc.host.ID,
		Action: audit.ActionCatalogDeploy, Decision: audit.DecisionDenied,
		Detail: map[string]any{"entry": "web", "host": "pi4", "reason": agentws.PolicyDenied},
	}
	if err := f.w.mem.InsertAudit(ctx, deny); err != nil {
		t.Fatal(err)
	}
	ready, checks, _ = readinessOf(t, f.w, f.owner, project, "web", host)
	c := findCheck(t, checks, "host_policy")
	if ready || c.Status != "missing" || c.Message != "this host does not allow image ghcr.io/joe/app" || c.Fix["action"] != "host_policy_snippet" || c.Fix["host_id"] != host || c.Fix["image_pattern"] != "ghcr.io/joe/app" {
		t.Fatalf("policy denied %+v", c)
	}
	status, env, _ := mustCall(t, f.w, f.del, http.MethodGet, "/v1/projects/"+project+"/catalog/web/host-policy-snippet?host_id="+host, "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	snip := objectPayload(t, env)
	if snip["image_pattern"] != "ghcr.io/joe/app" || snip["project"] != "proj" ||
		snip["command"] != "sudo ./install.sh install --runner --project proj --image 'ghcr.io/joe/app'" ||
		snip["toml"] != "images = [\"ghcr.io/joe/app\"]\nprojects = [\"proj\"]\n" || !strings.Contains(snip["note"].(string), "root") {
		t.Fatalf("snippet %v", snip)
	}
	ok := deny
	ok.At = f.w.now.Add(2 * time.Minute)
	ok.Decision = audit.DecisionAllowed
	ok.Detail = map[string]any{"entry": "web", "host": "pi4", "outcome": "succeeded"}
	if err := f.w.mem.InsertAudit(ctx, ok); err != nil {
		t.Fatal(err)
	}
	if _, checks, _ = readinessOf(t, f.w, f.owner, project, "web", host); findCheck(t, checks, "host_policy").Status != "unknown" {
		t.Fatal("a later deploy should clear host_policy")
	}

	// api-v1 v1.51: a console-managed host sends its patterns, so the check
	// is exact and names the pattern in the fix.
	h, err := f.w.mem.GetHost(ctx, f.sc.host.ID)
	if err != nil || h.Programs == nil {
		t.Fatal(err)
	}
	managed := *h.Programs
	managed.ConsolePolicy, managed.ImagePatterns = true, []string{"ghcr.io/other/*"}
	setPrograms(t, f.w, f.sc.host.ID, &managed)
	_, checks, _ = readinessOf(t, f.w, f.owner, project, "web", host)
	if c := findCheck(t, checks, "host_policy"); c.Status != "missing" || c.Fix["action"] != "host_policy_snippet" || c.Fix["image_pattern"] != "ghcr.io/joe/app" || !strings.Contains(c.Message, "Policy tab") {
		t.Fatalf("managed missing %+v", c)
	}
	managed.ImagePatterns = []string{"ghcr.io/joe/*"}
	setPrograms(t, f.w, f.sc.host.ID, &managed)
	if _, checks, _ = readinessOf(t, f.w, f.owner, project, "web", host); findCheck(t, checks, "host_policy").Status != "ok" {
		t.Fatal("a matching pattern should pass")
	}
	setPrograms(t, f.w, f.sc.host.ID, h.Programs)

	// No deploy access: host_access missing with grant_deploy_access.
	if _, _, err := f.w.grant(ctx, f.sc.host.ID, f.sc.project.ID, "metrics", owner.ID, f.w.now); err != nil {
		t.Fatal(err)
	}
	_, checks, _ = readinessOf(t, f.w, f.owner, project, "web", host)
	if c := findCheck(t, checks, "host_access"); c.Status != "missing" || c.Fix["action"] != "grant_deploy_access" || c.Fix["host_id"] != host {
		t.Fatalf("host access %+v", c)
	}
}

func TestReadinessAccessRules(t *testing.T) {
	f := newRegistryFixture(t)
	f.public(nil, "sha256:"+strings.Repeat("d", 64))
	project := f.project
	host := f.sc.host.ID.String()
	createImage(t, f.w, f.owner, f.sc.project.ID, "web", `{"kind":"service","image":"docker.io/library/nginx"}`)
	createProgram(t, f.w, f.owner, f.sc.project.ID, "job", "pnpm run dev", false)

	// Delegated sessions may read readiness. The host never connected: it is
	// offline (warn) and has not reported containers (missing).
	ready, checks, _ := readinessOf(t, f.w, f.del, project, "web", host)
	if ready || findCheck(t, checks, "host_online").Status != "warn" || findCheck(t, checks, "host_images").Status != "missing" {
		t.Fatalf("delegated %s", checkNames(checks))
	}
	if c := findCheck(t, checks, "host_online"); c.Message != "pi4 is offline; the deploy will wait or fail" {
		t.Fatalf("offline %q", c.Message)
	}
	// A program skips registry, host_images and networks.
	if _, checks, _ := readinessOf(t, f.w, f.owner, project, "job", host); checkNames(checks) != "env_sets:ok,env_values:ok,env_refs:ok,env_pairs:ok,files:ok,host_access:ok,host_online:warn,host_policy:unknown" {
		t.Fatalf("program %s", checkNames(checks))
	}
	path := "/v1/projects/" + project + "/catalog/web/readiness"
	status, env, _ := mustCall(t, f.w, f.viewer, http.MethodGet, path, "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	status, env, _ = mustCall(t, f.w, f.viewer, http.MethodGet, path+"?host_id="+host, "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	// A viewer granted deploy on the entry may check a host the project deploys to.
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPut, "/v1/projects/"+project+"/catalog/web/permissions",
		`{"grants":[{"subject":"role:admin","actions":["run","stop","restart","logs","deploy"]},{"subject":"role:viewer","actions":["logs","deploy"]}]}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	readinessOf(t, f.w, f.viewer, project, "web", host)
	status, env, _ = mustCall(t, f.w, f.extra, http.MethodGet, path, "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodGet, path+"?host_id=nope", "")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid id")
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodGet, path+"?host_id=00000000-0000-4000-8000-000000000000", "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	// The snippet is for owners and admins, image entries only.
	snip := "/v1/projects/" + project + "/catalog/%s/host-policy-snippet?host_id=" + host
	status, env, _ = mustCall(t, f.w, f.viewer, http.MethodGet, fmt.Sprintf(snip, "web"), "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodGet, fmt.Sprintf(snip, "job"), "")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "kind must be image")
	status, env, _ = mustCall(t, f.w, f.admin, http.MethodGet, fmt.Sprintf(snip, "web"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if objectPayload(t, env)["image_pattern"] != "docker.io/library/nginx" {
		t.Fatalf("hub pattern %v", env.Payload)
	}
}

// TestReadinessMoreChecks covers env_values through secret_env, networks
// missing, registry not_found and unknown (warn, including a rate-limited
// lookup), and a program's env_refs text.
func TestReadinessMoreChecks(t *testing.T) {
	f := newRegistryFixture(t)
	f.w.srv.Secrets = testBox(t)
	project := f.project
	host := f.sc.host.ID.String()
	helloAgent(t, f.w, f.sc.host, f.sc.credPlain, f.sc.credID, allowImages(), nil)

	putEnv(t, f.w, f.owner, project, "keys", "KEY1=secret-value-zz\n")
	if !f.w.mem.SetNeedsValue(f.sc.project.ID, "keys", "KEY1") {
		t.Fatal("placeholder")
	}
	f.private("bot", "stored-pass", false)
	putRegistry(t, f.w, f.owner, project, "ghcr.io", "bot", "stored-pass")
	createImage(t, f.w, f.owner, f.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/gone:v1","secret_env":{"TOKEN":"keys.KEY1"},"networks":["shared"]}`)
	_, checks, raw := readinessOf(t, f.w, f.owner, project, "web", host)
	if strings.Contains(raw, "secret-value-zz") {
		t.Fatal("value leaked")
	}
	if c := findCheck(t, checks, "env_values"); c.Status != "missing" || c.Message != "env KEY1 in set keys has no value yet; set it in the console" || c.Fix["key"] != "KEY1" || c.Fix["set"] != "keys" {
		t.Fatalf("secret_env placeholder %+v", c)
	}
	if c := findCheck(t, checks, "registry"); c.Status != "missing" || c.Message != "tag v1 of ghcr.io/joe/gone not found in the registry" || c.Fix["action"] != "none" {
		t.Fatalf("not_found %+v", c)
	}
	if c := findCheck(t, checks, "networks"); c.Status != "missing" || !strings.HasPrefix(c.Message, reasonNoNetworks) || c.Fix["action"] != "none" {
		t.Fatalf("networks %+v", c)
	}

	// A registry outside the allowlist is unknown: warn, not missing.
	createImage(t, f.w, f.owner, f.sc.project.ID, "other", `{"kind":"service","image":"registry.example.org/team/app:v1"}`)
	_, checks, _ = readinessOf(t, f.w, f.owner, project, "other", "")
	if c := findCheck(t, checks, "registry"); c.Status != "warn" || c.Message != "the registry could not be checked" {
		t.Fatalf("unknown %+v", c)
	}
	// A rate-limited lookup is unknown too.
	f.public(nil, "sha256:"+strings.Repeat("d", 64))
	for i := 0; i < 20; i++ {
		status, _, _ := mustCall(t, f.w, f.owner, http.MethodGet, accessPath(project, fmt.Sprintf("docker.io/library/app%d", i)), "")
		if status != http.StatusOK && status != http.StatusTooManyRequests {
			t.Fatalf("lookup %d", status)
		}
	}
	createImage(t, f.w, f.owner, f.sc.project.ID, "late", `{"kind":"service","image":"docker.io/library/late:v1"}`)
	_, checks, _ = readinessOf(t, f.w, f.owner, project, "late", "")
	if c := findCheck(t, checks, "registry"); c.Status != "warn" {
		t.Fatalf("rate limited %+v", c)
	}

	// A program takes its env literally.
	createProgram(t, f.w, f.owner, f.sc.project.ID, "job", "pnpm run dev", false)
	_, checks, _ = readinessOf(t, f.w, f.owner, project, "job", "")
	if c := findCheck(t, checks, "env_refs"); c.Status != "ok" || c.Message != "a program takes its env literally; ${KEY} is not resolved" {
		t.Fatalf("program env_refs %+v", c)
	}
}

func TestNotFoundText(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/joe/app:v2": "tag v2 of ghcr.io/joe/app not found in the registry",
		"ghcr.io/joe/app":    "tag latest of ghcr.io/joe/app not found in the registry (no tag was given, so latest is used)",
		"ghcr.io/joe/app@sha256:" + strings.Repeat("a", 64): "digest sha256:" + strings.Repeat("a", 64) + " of ghcr.io/joe/app not found in the registry",
	}
	for in, want := range cases {
		ref, err := catalog.ParseImageRef(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got := notFoundText(ref); got != want {
			t.Errorf("%s: got %q want %q", in, got, want)
		}
	}
}
