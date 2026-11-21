package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/github"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// fakeGitHubToken prefixes every token the fake hands out (a counter
// follows, so a reconnect gets a new token). Tests check it never
// leaves the API.
const fakeGitHubToken = "gho_FAKEsecretTOKENvalue1234567890"

// fakeGitHub is GitHub's token, /user and revoke endpoints.
type fakeGitHub struct {
	srv *httptest.Server

	mu        sync.Mutex
	tokenFail bool
	userFail  bool
	issued    int
	scopes    string
	login     string
	revokeOK  bool
	revoked   []string
	verifier  string
	redirect  string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{scopes: "read:packages", login: "octo-cat", revokeOK: true}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/login/oauth/access_token":
			_ = r.ParseForm()
			f.verifier = r.PostForm.Get("code_verifier")
			f.redirect = r.PostForm.Get("redirect_uri")
			if f.tokenFail || r.PostForm.Get("code") != "good-code" {
				_, _ = io.WriteString(w, `{"error":"bad_verification_code","error_description":"GITHUB-BODY-DETAIL"}`)
				return
			}
			f.issued++
			_, _ = io.WriteString(w, `{"access_token":"`+fakeGitHubToken+strconv.Itoa(f.issued)+`","token_type":"bearer","scope":"`+f.scopes+`"}`)
		case r.URL.Path == "/user":
			if f.userFail {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "+fakeGitHubToken) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("X-OAuth-Scopes", f.scopes)
			_ = json.NewEncoder(w).Encode(map[string]any{"login": f.login})
		case r.Method == http.MethodDelete && r.URL.Path == "/applications/test-client/token":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !f.revokeOK {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			f.revoked = append(f.revoked, body["access_token"])
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) set(fn func(f *fakeGitHub)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeGitHub) revokedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}

// wireGitHub turns Connect GitHub on for w.
func wireGitHub(w *world, f *fakeGitHub) {
	w.srv.ConsoleOrigin = "http://console.test"
	w.srv.GitHub = github.New(github.Config{
		ClientID: "test-client", ClientSecret: "test-secret",
		TokenURL: f.srv.URL + "/login/oauth/access_token", APIURL: f.srv.URL, HTTP: f.srv.Client(),
	})
	w.srv.GitHubStates = github.NewStates()
	w.srv.GitHubFailures = github.NewFailures()
}

func githubStartPath(project string) string {
	return "/v1/projects/" + project + "/registries/github/start"
}

// startGitHub returns the state from a successful start.
func startGitHub(t *testing.T, w *world, tok, project string) string {
	t.Helper()
	status, env, _ := mustCall(t, w, tok, http.MethodPost, githubStartPath(project), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	raw, _ := objectPayload(t, env)["authorize_url"].(string)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

func callbackBody(code, state string) string {
	raw, _ := json.Marshal(map[string]string{"code": code, "state": state})
	return string(raw)
}

// githubConnectFlag is the registries list's github_connect (api-v1 v1.29 item 5).
func githubConnectFlag(t *testing.T, w *world, tok, project string) any {
	t.Helper()
	status, env, _ := mustCall(t, w, tok, http.MethodGet, "/v1/projects/"+project+"/registries", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	return objectPayload(t, env)["github_connect"]
}

func TestRegistryListGitHubConnect(t *testing.T) {
	f := newRegistryFixture(t)
	if got := githubConnectFlag(t, f.w, f.owner, f.project); got != false {
		t.Fatalf("not configured: %v", got)
	}
	wireGitHub(f.w, newFakeGitHub(t))
	// Configured but no secrets key: still false (item 5).
	if got := githubConnectFlag(t, f.w, f.owner, f.project); got != false {
		t.Fatalf("no secrets key: %v", got)
	}
	f.w.srv.Secrets = testBox(t)
	if got := githubConnectFlag(t, f.w, f.owner, f.project); got != true {
		t.Fatalf("configured: %v", got)
	}
	// Without the console origin the callback has nowhere to go: not configured.
	f.w.srv.ConsoleOrigin = ""
	if got := githubConnectFlag(t, f.w, f.owner, f.project); got != false {
		t.Fatalf("no console origin: %v", got)
	}
}

func TestGitHubConnectNotConfigured(t *testing.T) {
	f := newRegistryFixture(t)
	f.w.srv.Secrets = testBox(t)
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, githubStartPath(f.project), "")
	mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", "GitHub is not configured")
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, "/v1/auth/github/callback", callbackBody("good-code", "x"))
	mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", "GitHub is not configured")
}

func TestGitHubStartRules(t *testing.T) {
	f := newRegistryFixture(t)
	gh := newFakeGitHub(t)
	wireGitHub(f.w, gh)
	path := githubStartPath(f.project)
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, path, "")
	mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", "secrets key not configured")
	f.w.srv.Secrets = testBox(t)
	status, env, _ = mustCall(t, f.w, f.viewer, http.MethodPost, path, "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	status, env, _ = mustCall(t, f.w, f.extra, http.MethodPost, path, "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, env, _ = mustCall(t, f.w, f.del, http.MethodPost, path, "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	for _, tok := range []string{f.owner, f.admin} {
		status, env, _ = mustCall(t, f.w, tok, http.MethodPost, path, "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		u, err := url.Parse(objectPayload(t, env)["authorize_url"].(string))
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		if u.Host != "github.com" || q.Get("client_id") != "test-client" || q.Get("scope") != "read:packages" ||
			q.Get("redirect_uri") != "http://console.test/auth/github/callback" || q.Get("code_challenge_method") != "S256" ||
			q.Get("code_challenge") == "" || len(q.Get("state")) != 43 {
			t.Fatalf("authorize url %s", u)
		}
		if strings.Contains(u.String(), "test-secret") {
			t.Fatal("client secret in the authorize URL")
		}
	}
}

func TestGitHubCallbackStateRules(t *testing.T) {
	f := newRegistryFixture(t)
	gh := newFakeGitHub(t)
	wireGitHub(f.w, gh)
	f.w.srv.Secrets = testBox(t)
	const stateMsg = "this GitHub sign-in expired or was already used; start again"
	cb := "/v1/auth/github/callback"

	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, cb, callbackBody("good-code", "unknown"))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", stateMsg)

	// Another user's state is refused and spent.
	state := startGitHub(t, f.w, f.owner, f.project)
	status, env, _ = mustCall(t, f.w, f.admin, http.MethodPost, cb, callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", stateMsg)
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, cb, callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", stateMsg)

	// Expired.
	state = startGitHub(t, f.w, f.owner, f.project)
	later := f.w.now.Add(github.StateTTL)
	f.w.srv.Now = func() time.Time { return later }
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, cb, callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", stateMsg)
	f.w.srv.Now = func() time.Time { return f.w.now }

	// Used once, then refused.
	state = startGitHub(t, f.w, f.owner, f.project)
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, cb, callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, cb, callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", stateMsg)

	// Delegated sessions are refused before the state is read.
	state = startGitHub(t, f.w, f.owner, f.project)
	status, env, _ = mustCall(t, f.w, f.del, http.MethodPost, cb, callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")

	// The caller lost admin between start and callback.
	adminState := startGitHub(t, f.w, f.admin, f.project)
	admin := f.w.user("admin@ex.com", "Admin")
	if _, err := f.w.mem.UpdateMemberRole(context.Background(), f.sc.project.ID, admin.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	status, env, _ = mustCall(t, f.w, f.admin, http.MethodPost, cb, callbackBody("good-code", adminState))
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
}

func TestGitHubCallbackGitHubErrors(t *testing.T) {
	f := newRegistryFixture(t)
	buf := captureLog(f.w)
	gh := newFakeGitHub(t)
	wireGitHub(f.w, gh)
	f.w.srv.Secrets = testBox(t)
	cb := "/v1/auth/github/callback"

	state := startGitHub(t, f.w, f.owner, f.project)
	status, env, raw := mustCall(t, f.w, f.owner, http.MethodPost, cb, callbackBody("bad-code", state))
	mustHTTP(t, status, env, http.StatusBadGateway, "bad_gateway", "GitHub sign-in failed; try again")
	if strings.Contains(raw, "GITHUB-BODY") || strings.Contains(buf.String(), "GITHUB-BODY") {
		t.Fatal("GitHub body echoed")
	}

	gh.set(func(g *fakeGitHub) { g.scopes = "repo" })
	state = startGitHub(t, f.w, f.owner, f.project)
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, cb, callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "GitHub did not grant read:packages")
	if got := gh.revokedTokens(); len(got) != 1 || !strings.HasPrefix(got[0], fakeGitHubToken) {
		t.Fatalf("token not revoked after the scope refusal: %v", got)
	}
	if items := listRegistries(t, f.w, f.owner, f.project); len(items) != 0 {
		t.Fatalf("stored after a refusal: %v", items)
	}
}

func listRegistries(t *testing.T, w *world, tok, project string) []map[string]any {
	t.Helper()
	status, env, _ := mustCall(t, w, tok, http.MethodGet, "/v1/projects/"+project+"/registries", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	items, _ := objectPayload(t, env)["items"].([]any)
	out := make([]map[string]any, len(items))
	for i, it := range items {
		out[i], _ = it.(map[string]any)
	}
	return out
}

func TestGitHubConnectAndDisconnect(t *testing.T) {
	f := newRegistryFixture(t)
	buf := captureLog(f.w)
	gh := newFakeGitHub(t)
	wireGitHub(f.w, gh)
	f.w.srv.Secrets = testBox(t)
	owner := f.w.user("owner@ex.com", "Owner")
	var seen []string

	state := startGitHub(t, f.w, f.owner, f.project)
	status, env, raw := mustCall(t, f.w, f.owner, http.MethodPost, "/v1/auth/github/callback", callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	seen = append(seen, raw)
	p := objectPayload(t, env)
	if p["project_id"] != f.project || p["host"] != "ghcr.io" || p["username"] != "octo-cat" || p["source"] != "github" || len(p) != 4 {
		t.Fatalf("callback payload %v", p)
	}
	if gh.redirect != "http://console.test/auth/github/callback" || len(gh.verifier) < 43 {
		t.Fatalf("exchange redirect %q verifier %d", gh.redirect, len(gh.verifier))
	}
	items := listRegistries(t, f.w, f.owner, f.project)
	by, _ := items[0]["connected_by"].(map[string]any)
	if len(items) != 1 || items[0]["source"] != "github" || items[0]["username"] != "octo-cat" || by["id"] != owner.ID.String() || by["name"] != "Owner" {
		t.Fatalf("registries %v", items)
	}
	// The token reaches the deploy path exactly as a manual login does.
	got, err := f.w.srv.registryAuth(context.Background(), f.sc.project.ID, "ghcr.io", refreshOnTask)
	if m, _ := got.(map[string]any); err != nil || m["token"] != fakeGitHubToken+"1" || m["username"] != "octo-cat" {
		t.Fatalf("registry_auth %v %v", got, err)
	}

	// Reconnect replaces the token in place.
	state = startGitHub(t, f.w, f.admin, f.project)
	status, env, raw = mustCall(t, f.w, f.admin, http.MethodPost, "/v1/auth/github/callback", callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	seen = append(seen, raw)
	if items = listRegistries(t, f.w, f.owner, f.project); len(items) != 1 || items[0]["connected_by"].(map[string]any)["name"] != "Admin" {
		t.Fatalf("reconnect %v", items)
	}
	// The replaced GitHub token is revoked after the write (item 3).
	if got := gh.revokedTokens(); len(got) != 1 || got[0] != fakeGitHubToken+"1" {
		t.Fatalf("reconnect revoke %v", got)
	}

	// Disconnect deletes the row and revokes at GitHub.
	status, env, raw = mustCall(t, f.w, f.owner, http.MethodDelete, regPath(f.project, "ghcr.io"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	seen = append(seen, raw)
	if objectPayload(t, env)["revoked"] != true || len(gh.revokedTokens()) != 2 || gh.revokedTokens()[1] != fakeGitHubToken+"2" {
		t.Fatalf("disconnect %v revoked %v", env.Payload, gh.revokedTokens())
	}
	if items = listRegistries(t, f.w, f.owner, f.project); len(items) != 0 {
		t.Fatalf("row left %v", items)
	}

	// A failed revoke still deletes the row and is reported.
	state = startGitHub(t, f.w, f.owner, f.project)
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, "/v1/auth/github/callback", callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	gh.set(func(g *fakeGitHub) { g.revokeOK = false })
	status, env, raw = mustCall(t, f.w, f.owner, http.MethodDelete, regPath(f.project, "ghcr.io"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	seen = append(seen, raw)
	if objectPayload(t, env)["revoked"] != false || len(listRegistries(t, f.w, f.owner, f.project)) != 0 {
		t.Fatalf("failed revoke %v", env.Payload)
	}

	// Delegated sessions cannot disconnect.
	gh.set(func(g *fakeGitHub) { g.revokeOK = true })
	state = startGitHub(t, f.w, f.owner, f.project)
	mustCall(t, f.w, f.owner, http.MethodPost, "/v1/auth/github/callback", callbackBody("good-code", state))
	status, env, _ = mustCall(t, f.w, f.del, http.MethodDelete, regPath(f.project, "ghcr.io"), "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")

	// A manual PUT over the GitHub row makes it manual and does not revoke.
	before := len(gh.revokedTokens())
	putRegistry(t, f.w, f.owner, f.project, "ghcr.io", "bot", "manual-token-value")
	items = listRegistries(t, f.w, f.owner, f.project)
	if items[0]["source"] != "manual" || items[0]["connected_by"] != nil || len(gh.revokedTokens()) != before {
		t.Fatalf("manual over github %v", items)
	}
	status, env, raw = mustCall(t, f.w, f.owner, http.MethodDelete, regPath(f.project, "ghcr.io"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if strings.Contains(raw, "revoked") {
		t.Fatalf("manual delete %s", raw)
	}

	// Audit rows: connected and disconnected, never the token.
	var connected, disconnected int
	for _, row := range f.w.mem.AllAudits() {
		if strings.Contains(string(row.Detail), fakeGitHubToken) {
			t.Fatalf("token in audit %s %s", row.Action, row.Detail)
		}
		switch row.Action {
		case audit.ActionRegistryGitHubConnected:
			connected++
			var d map[string]any
			_ = json.Unmarshal(row.Detail, &d)
			if d["host"] != "ghcr.io" || d["login"] != "octo-cat" || d["via"] != "console" {
				t.Fatalf("connected detail %v", d)
			}
		case audit.ActionRegistryGitHubDisconnected:
			disconnected++
		}
	}
	if connected != 4 || disconnected != 2 {
		t.Fatalf("audit connected %d disconnected %d", connected, disconnected)
	}
	for _, r := range seen {
		if strings.Contains(r, fakeGitHubToken) || strings.Contains(r, "test-secret") {
			t.Fatalf("token in response %s", r)
		}
	}
	for _, it := range listRegistries(t, f.w, f.owner, f.project) {
		raw, _ := json.Marshal(it)
		if strings.Contains(string(raw), fakeGitHubToken) {
			t.Fatal("token in registry list")
		}
	}
	if strings.Contains(buf.String(), fakeGitHubToken) || strings.Contains(buf.String(), "test-secret") {
		t.Fatal("token in the log")
	}
}

// failingPut is a store whose registry writes fail, for the callback's
// store-failure revoke.
type failingPut struct{ store.Store }

func (failingPut) PutRegistry(context.Context, uuid.UUID, string, store.RegistryWrite, uuid.UUID, time.Time) (store.Registry, error) {
	return store.Registry{}, errors.New("write failed")
}

func TestGitHubCallbackOrderAndRevokes(t *testing.T) {
	f := newRegistryFixture(t)
	gh := newFakeGitHub(t)
	wireGitHub(f.w, gh)
	cb := "/v1/auth/github/callback"

	// Cheap checks run before the state is spent: the same state works after.
	f.w.srv.Secrets = testBox(t)
	state := startGitHub(t, f.w, f.owner, f.project)
	for _, code := range []string{"", strings.Repeat("c", 513)} {
		status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, cb, callbackBody(code, state))
		mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid code")
	}
	f.w.srv.Secrets = nil
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, cb, callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", "secrets key not configured")
	f.w.srv.Secrets = testBox(t)
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, cb, callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")

	// A failed store write revokes the new token.
	before := len(gh.revokedTokens())
	real := f.w.srv.Store
	f.w.srv.Store = failingPut{real}
	state = startGitHub(t, f.w, f.owner, f.project)
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, cb, callbackBody("good-code", state))
	if status != http.StatusInternalServerError {
		t.Fatalf("store failure %d %v", status, env)
	}
	f.w.srv.Store = real
	if got := gh.revokedTokens(); len(got) != before+1 || got[len(got)-1] != fakeGitHubToken+"2" {
		t.Fatalf("new token not revoked: %v", got)
	}

	// A token that does not open (another key) still deletes the row: revoked false, no GitHub call.
	f.w.srv.Secrets = testBoxByte(t, 9)
	before = len(gh.revokedTokens())
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodDelete, regPath(f.project, "ghcr.io"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if objectPayload(t, env)["revoked"] != false || len(gh.revokedTokens()) != before || len(listRegistries(t, f.w, f.owner, f.project)) != 0 {
		t.Fatalf("unopenable %v %v", env.Payload, gh.revokedTokens())
	}
}

// TestGitHubStartPerUserLimit: a sixth start drops the user's oldest state.
func TestGitHubStartPerUserLimit(t *testing.T) {
	f := newRegistryFixture(t)
	wireGitHub(f.w, newFakeGitHub(t))
	f.w.srv.Secrets = testBox(t)
	var states []string
	for i := 0; i < github.PerUser+1; i++ {
		states = append(states, startGitHub(t, f.w, f.owner, f.project))
	}
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, "/v1/auth/github/callback", callbackBody("good-code", states[0]))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "this GitHub sign-in expired or was already used; start again")
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, "/v1/auth/github/callback", callbackBody("good-code", states[github.PerUser]))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
}
