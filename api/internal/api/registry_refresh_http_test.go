package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/github"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// fakeRefreshToken prefixes every refresh token the refreshing fake hands
// out. Tests check it never leaves the API.
const fakeRefreshToken = "ghr_FAKErefreshSECRETvalue987654321"

// refreshGitHub is GitHub's token endpoint with expiring tokens (api-v1
// v1.43): the code exchange and grant_type=refresh_token both issue a new
// access token (prefix fakeGitHubToken) and a new refresh token. A refresh
// spends its refresh token and ends the access token issued with it
// (rotation); a used or unknown refresh token is bad_refresh_token. live
// is the access tokens GitHub accepts; revoke ends one. /user answers as
// the plain fake does.
type refreshGitHub struct {
	srv *httptest.Server

	mu sync.Mutex
	// expiresIn is the access token life in seconds; 0 issues a long-lived
	// token without a refresh token.
	expiresIn int
	issued    int
	refreshes int
	// rejects counts bad_refresh_token answers.
	rejects int
	// failStatus, when set, answers every refresh with that status.
	failStatus int
	// reject answers every refresh with bad_refresh_token.
	reject bool
	// access and refresh are the latest issued.
	access, refresh string
	live            map[string]bool
	pairs           map[string]string // refresh token -> its access token
	revoked         []string
	// attempts is every token a revoke call carried, live or not.
	attempts []string
	// delay holds a refresh answer, so concurrent callers overlap.
	delay time.Duration
	// started is signalled when a refresh request arrives.
	started chan struct{}
}

func newRefreshGitHub(t *testing.T) *refreshGitHub {
	t.Helper()
	f := &refreshGitHub{expiresIn: 8 * 3600, live: map[string]bool{}, pairs: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login/oauth/access_token":
			_ = r.ParseForm()
			isRefresh := r.PostForm.Get("grant_type") == "refresh_token"
			f.mu.Lock()
			delay, started := f.delay, f.started
			f.mu.Unlock()
			if isRefresh {
				if started != nil {
					select {
					case started <- struct{}{}:
					default:
					}
				}
				time.Sleep(delay)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if isRefresh {
				f.refreshes++
				if f.failStatus != 0 {
					w.WriteHeader(f.failStatus)
					_, _ = io.WriteString(w, `GITHUB-BODY-DETAIL`)
					return
				}
				old, ok := f.pairs[r.PostForm.Get("refresh_token")]
				if f.reject || !ok {
					f.rejects++
					_, _ = io.WriteString(w, `{"error":"bad_refresh_token","error_description":"GITHUB-BODY-DETAIL"}`)
					return
				}
				delete(f.pairs, r.PostForm.Get("refresh_token"))
				delete(f.live, old)
			} else if r.PostForm.Get("code") != "good-code" {
				_, _ = io.WriteString(w, `{"error":"bad_verification_code"}`)
				return
			}
			f.issued++
			f.access = fakeGitHubToken + strconv.Itoa(f.issued)
			f.live[f.access] = true
			body := map[string]any{"access_token": f.access, "token_type": "bearer", "scope": "read:packages"}
			f.refresh = ""
			if f.expiresIn > 0 {
				f.refresh = fakeRefreshToken + strconv.Itoa(f.issued)
				f.pairs[f.refresh] = f.access
				body["expires_in"] = f.expiresIn
				body["refresh_token"] = f.refresh
				body["refresh_token_expires_in"] = 15897600
			}
			_ = json.NewEncoder(w).Encode(body)
		case r.URL.Path == "/user":
			w.Header().Set("X-OAuth-Scopes", "read:packages")
			_ = json.NewEncoder(w).Encode(map[string]any{"login": "octo-cat"})
		case r.Method == http.MethodDelete && r.URL.Path == "/applications/test-client/token":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			defer f.mu.Unlock()
			f.attempts = append(f.attempts, body["access_token"])
			if !f.live[body["access_token"]] {
				// GitHub cannot revoke a token it no longer knows (expired).
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(f.live, body["access_token"])
			f.revoked = append(f.revoked, body["access_token"])
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *refreshGitHub) set(fn func(f *refreshGitHub)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *refreshGitHub) state() (access, refresh string, refreshes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.access, f.refresh, f.refreshes
}

func (f *refreshGitHub) isLive(token string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live[token]
}

// expireAll ends every access token at GitHub (they expired).
func (f *refreshGitHub) expireAll() {
	f.set(func(g *refreshGitHub) { g.live = map[string]bool{} })
}

func (f *refreshGitHub) revokeAttempts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.attempts...)
}

func (f *refreshGitHub) revokedTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.revoked...)
}

// syncBuffer is a log buffer that a handler still running in the background
// may write while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// refreshFixture is the registry fixture with the refreshing fake, a
// movable clock and a registry that accepts only GitHub's current token.
type refreshFixture struct {
	*registryFixture
	gh  *refreshGitHub
	log *syncBuffer
	set func(time.Time)
	cur time.Time
	// seen collects every API answer body, for the secrets check.
	seen []string
}

func newRefreshFixture(t *testing.T) *refreshFixture {
	t.Helper()
	rf := &refreshFixture{registryFixture: newRegistryFixture(t), gh: newRefreshGitHub(t)}
	rf.log = &syncBuffer{}
	captureLog(rf.w)
	logger := slog.New(slog.NewTextHandler(rf.log, nil))
	rf.w.srv.Log = logger
	rf.w.srv.Audit.Log = logger
	rf.w.hub.SetLog(logger)
	rf.set = problemClock(rf.w)
	rf.cur = rf.w.now
	w := rf.w
	w.srv.Secrets = testBox(t)
	w.srv.ConsoleOrigin = "http://console.test"
	w.srv.GitHub = github.New(github.Config{
		ClientID: "test-client", ClientSecret: "test-secret",
		TokenURL: rf.gh.srv.URL + "/login/oauth/access_token", APIURL: rf.gh.srv.URL, HTTP: rf.gh.srv.Client(),
		Now: func() time.Time { return w.srv.now() },
	})
	w.srv.GitHubStates = github.NewStates()
	w.srv.GitHubFailures = github.NewFailures()
	// ghcr.io accepts octo-cat with GitHub's current access token only.
	rf.fake.Handle(func(rw http.ResponseWriter, r *http.Request) {
		rf.count(r)
		if r.URL.Path == "/token" {
			u, p, ok := r.BasicAuth()
			if !ok {
				fmt.Fprint(rw, `{"token":"anon"}`)
				return
			}
			if u != "octo-cat" || !rf.gh.isLive(p) {
				rw.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(rw, `{"token":"good"}`)
			return
		}
		switch r.Header.Get("Authorization") {
		case "":
			rw.Header().Set("WWW-Authenticate", `Bearer realm="https://`+r.Host+`/token",service="`+r.Host+`"`)
			rw.WriteHeader(http.StatusUnauthorized)
		case "Bearer good":
			if strings.HasSuffix(r.URL.Path, "/tags/list") {
				fmt.Fprint(rw, `{"tags":["v1"]}`)
				return
			}
			rw.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("e", 64))
		default:
			rw.WriteHeader(http.StatusForbidden)
		}
	})
	return rf
}

// at moves the server clock to base + d.
func (rf *refreshFixture) at(d time.Duration) {
	rf.cur = rf.w.now.Add(d)
	rf.set(rf.cur)
}

// connect runs Connect GitHub for the owner.
func (rf *refreshFixture) connect(t *testing.T) {
	t.Helper()
	state := startGitHub(t, rf.w, rf.owner, rf.project)
	status, env, raw := mustCall(t, rf.w, rf.owner, http.MethodPost, "/v1/auth/github/callback", callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	rf.seen = append(rf.seen, raw)
}

func (rf *refreshFixture) material(t *testing.T) store.RegistryMaterial {
	t.Helper()
	mat, err := rf.w.mem.GetRegistryMaterial(context.Background(), rf.sc.project.ID, "ghcr.io")
	if err != nil {
		t.Fatal(err)
	}
	return mat
}

// storedRefresh opens the stored refresh token, or "" when none is stored.
func (rf *refreshFixture) storedRefresh(t *testing.T) string {
	t.Helper()
	mat := rf.material(t)
	if !mat.HasRefresh() {
		return ""
	}
	plain, err := rf.w.srv.Secrets.Open(seal.RegistryRefreshAAD(rf.project, "ghcr.io"), mat.RefreshNonce, mat.RefreshCiphertext)
	if err != nil {
		t.Fatal("refresh token does not open with its AAD")
	}
	return string(plain)
}

// access asks image-access for the private image, past the 60 s cache.
func (rf *refreshFixture) access(t *testing.T) string {
	t.Helper()
	rf.at(rf.cur.Sub(rf.w.now) + 2*time.Minute)
	status, env, raw := mustCall(t, rf.w, rf.owner, http.MethodGet, accessPath(rf.project, "ghcr.io/joe/private:v1"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	rf.seen = append(rf.seen, raw)
	got, _ := objectPayload(t, env)["access"].(string)
	return got
}

// assertNoTokens is acceptance 7: no access or refresh token in any log
// line, audit detail, problem text or API answer.
func (rf *refreshFixture) assertNoTokens(t *testing.T) {
	t.Helper()
	for _, secret := range []string{fakeGitHubToken, fakeRefreshToken, "ghr_", "test-secret", "GITHUB-BODY"} {
		if strings.Contains(rf.log.String(), secret) {
			t.Fatalf("log holds %q", secret)
		}
		for _, raw := range rf.seen {
			if strings.Contains(raw, secret) {
				t.Fatalf("answer holds %q", secret)
			}
		}
		for _, row := range rf.w.mem.AllAudits() {
			if strings.Contains(string(row.Detail), secret) {
				t.Fatalf("audit %s holds %q", row.Action, secret)
			}
		}
	}
	for _, it := range listRegistries(t, rf.w, rf.owner, rf.project) {
		raw, _ := json.Marshal(it)
		for _, word := range []string{fakeGitHubToken, fakeRefreshToken, "expires", "refresh"} {
			if strings.Contains(string(raw), word) {
				t.Fatalf("registry list holds %q: %s", word, raw)
			}
		}
	}
	assertNoSecrets(t, rf.w, fakeGitHubToken, fakeRefreshToken)
}

// assertNoIdle: after the test's calls, the refresh state holds no entry
// that holds nothing (no leak), once the sweep's cleanup ran.
func (rf *refreshFixture) assertNoIdle(t *testing.T) {
	t.Helper()
	rf.w.srv.refresh.dropIdle(rf.w.srv.now())
	rf.w.srv.refresh.mu.Lock()
	defer rf.w.srv.refresh.mu.Unlock()
	for key, k := range rf.w.srv.refresh.keys {
		if k.users != 0 || k.idle(rf.w.srv.now()) {
			t.Fatalf("refresh state %s users %d idle %v", key, k.users, k.idle(rf.w.srv.now()))
		}
	}
}

func refreshAudits(rf *refreshFixture) []store.AuditEntry {
	var out []store.AuditEntry
	for _, row := range rf.w.mem.AllAudits() {
		if row.Action == audit.ActionRegistryGitHubRefreshRejected {
			out = append(out, row)
		}
	}
	return out
}

// TestGitHubRefreshExchangeStores is acceptance 1.
func TestGitHubRefreshExchangeStores(t *testing.T) {
	rf := newRefreshFixture(t)
	rf.connect(t)
	mat := rf.material(t)
	if mat.AccessExpiresAt == nil || !mat.AccessExpiresAt.Equal(rf.w.now.Add(8*time.Hour)) ||
		mat.RefreshExpiresAt == nil || !mat.RefreshExpiresAt.Equal(rf.w.now.Add(15897600*time.Second)) {
		t.Fatalf("expiry times %v %v", mat.AccessExpiresAt, mat.RefreshExpiresAt)
	}
	if got := rf.storedRefresh(t); got != fakeRefreshToken+"1" {
		t.Fatal("refresh token not stored sealed")
	}
	// The access token is sealed under its own AAD, not the refresh one.
	if _, err := rf.w.srv.Secrets.Open(seal.RegistryRefreshAAD(rf.project, "ghcr.io"), mat.Nonce, mat.Ciphertext); err == nil {
		t.Fatal("access token opens with the refresh AAD")
	}

	// A long-lived token stores nulls and is never refreshed.
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 0 })
	rf.connect(t)
	mat = rf.material(t)
	if mat.HasRefresh() || mat.AccessExpiresAt != nil || mat.RefreshExpiresAt != nil {
		t.Fatalf("long-lived token stored refresh material %v", mat.HasRefresh())
	}
	if p := rf.access(t); p != accessPrivateOK {
		t.Fatalf("long-lived access %v", p)
	}
	if _, _, n := rf.gh.state(); n != 0 {
		t.Fatalf("refreshed a long-lived token %d", n)
	}

	// A manual login over the github row drops the refresh material.
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 600 })
	rf.connect(t)
	if !rf.material(t).HasRefresh() {
		t.Fatal("reconnect stored no refresh token")
	}
	putRegistry(t, rf.w, rf.owner, rf.project, "ghcr.io", "bot", "manual-token-value")
	if mat = rf.material(t); mat.HasRefresh() || mat.AccessExpiresAt != nil {
		t.Fatal("manual login kept refresh material")
	}
	rf.assertNoTokens(t)
}

// TestGitHubRefreshOnUse is acceptance 2: 10 minutes left refreshes once,
// uses the new token and stores the rotated refresh token; with the new
// token (or 2 hours left) nothing refreshes.
func TestGitHubRefreshOnUse(t *testing.T) {
	rf := newRefreshFixture(t)
	createImage(t, rf.w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 2 * 3600 })
	rf.connect(t)
	// 2 hours minus 2 minutes left: no refresh.
	if p := rf.access(t); p != accessPrivateOK {
		t.Fatalf("fresh access %v", p)
	}
	if _, _, n := rf.gh.state(); n != 0 {
		t.Fatalf("refreshed with 2 h left: %d", n)
	}
	// 10 minutes left: one refresh, the new token is used and stored.
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 8 * 3600 })
	rf.at(2*time.Hour - 12*time.Minute)
	if p := rf.access(t); p != accessPrivateOK {
		t.Fatalf("refreshed access %v", p)
	}
	access, refresh, n := rf.gh.state()
	if n != 1 || access != fakeGitHubToken+"2" {
		t.Fatalf("refreshes %d", n)
	}
	if rf.storedRefresh(t) != refresh || refresh != fakeRefreshToken+"2" {
		t.Fatal("rotated refresh token not stored")
	}
	mat := rf.material(t)
	if token, err := rf.w.srv.openRegistry(rf.sc.project.ID, mat); err != nil || token != access {
		t.Fatal("stored access token is not the refreshed one")
	}
	if !mat.AccessExpiresAt.Equal(rf.cur.Add(8 * time.Hour)) {
		t.Fatalf("access expiry %v", mat.AccessExpiresAt)
	}
	// The registry list keeps its shape and updated_at.
	items := listRegistries(t, rf.w, rf.owner, rf.project)
	if len(items) != 1 || len(items[0]) != 7 || items[0]["updated_at"] != rf.w.now.Format(time.RFC3339) {
		t.Fatalf("registry list %v", items)
	}
	if p := rf.access(t); p != accessPrivateOK {
		t.Fatalf("second access %v", p)
	}
	if _, _, n := rf.gh.state(); n != 1 {
		t.Fatalf("refreshed again with a new token: %d", n)
	}
	if !strings.Contains(rf.log.String(), `msg="github token refreshed" project_id=`+rf.project+` host=ghcr.io`) {
		t.Fatal("no refresh Info line")
	}
	rf.assertNoTokens(t)
}

// TestGitHubRefreshConcurrent is acceptance 2: concurrent lookups refresh
// exactly once and all use the winner's token.
func TestGitHubRefreshConcurrent(t *testing.T) {
	rf := newRefreshFixture(t)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 600 })
	rf.connect(t)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 8 * 3600; g.delay = 50 * time.Millisecond })
	const n = 6
	tokens := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, tokens[i], errs[i] = rf.w.srv.registryToken(context.Background(), rf.sc.project.ID, "ghcr.io", refreshOnUse)
		}()
	}
	wg.Wait()
	access, refresh, refreshes := rf.gh.state()
	if refreshes != 1 {
		t.Fatalf("refreshes %d", refreshes)
	}
	for i := range n {
		if errs[i] != nil || tokens[i] != access {
			t.Fatalf("caller %d did not get the winner token (%v)", i, errs[i])
		}
	}
	if rf.storedRefresh(t) != refresh {
		t.Fatal("rotated refresh token not stored")
	}
	if len(refreshAudits(rf)) != 0 {
		t.Fatal("a concurrent caller sent a used refresh token")
	}
	rf.assertNoTokens(t)
}

// TestGitHubRefreshDeployFrame is acceptance 3: with 60 minutes left a
// deploy refreshes before the frame is built and the frame carries the new
// token; with 2 hours left it does not refresh.
func TestGitHubRefreshDeployFrame(t *testing.T) {
	rf := newRefreshFixture(t)
	w := rf.w
	createImage(t, w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 2 * 3600 })
	rf.connect(t)
	conn := helloAgent(t, w, rf.sc.host, rf.sc.credPlain, rf.sc.credID, allowImages(), nil)
	tasks := watchTasks(t, conn, func(msg map[string]any) map[string]any {
		return imageDeployReply(msg, shaDigest(9))
	})
	host := rf.sc.host.ID.String()
	frameToken := func(i int) string {
		t.Helper()
		spec, _ := tasks.raw(i)["spec"].(map[string]any)
		reg, _ := spec["registry_auth"].(map[string]any)
		token, _ := reg["token"].(string)
		return token
	}
	// 2 hours left: no refresh, the stored token goes out.
	status, env, raw := postImage(t, w, rf.owner, rf.project, "web", "deploy", host, "rf-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	rf.seen = append(rf.seen, raw)
	if _, _, n := rf.gh.state(); n != 0 || frameToken(0) != fakeGitHubToken+"1" {
		t.Fatalf("2 h left: refreshes %d", n)
	}
	// 60 minutes left (a reconnect that got a 60-minute token, so the
	// agent stays fresh on the clock): refreshed first, the frame carries
	// the new token.
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 3600 })
	rf.connect(t)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 8 * 3600 })
	status, env, raw = postImage(t, w, rf.owner, rf.project, "web", "deploy", host, "rf-2", map[string]any{"force": true})
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	rf.seen = append(rf.seen, raw)
	access, _, n := rf.gh.state()
	if n != 1 || tasks.count() != 2 || frameToken(1) != access || access != fakeGitHubToken+"3" {
		t.Fatalf("60 min left: refreshes %d tasks %d", n, tasks.count())
	}
	rf.assertNoTokens(t)
}

// TestGitHubRefreshHourly is acceptance 4: the upkeep refreshes a login
// expiring within 90 minutes, not one with 3 hours left; it runs with
// BOT_API_PROBLEMS=off; BOT_API_GITHUB_REFRESH=off refreshes nothing.
func TestGitHubRefreshHourly(t *testing.T) {
	rf := newRefreshFixture(t)
	ctx := context.Background()
	rf.w.srv.ProblemsOff = true
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 3 * 3600 })
	rf.connect(t)
	rf.w.srv.SweepGitHubRefresh(ctx)
	if _, _, n := rf.gh.state(); n != 0 {
		t.Fatalf("refreshed with 3 h left: %d", n)
	}
	// Switch off: 80 minutes left, the sweep does nothing; 60 minutes left,
	// a deploy-window use (due were refresh on) does nothing either.
	rf.at(100 * time.Minute)
	rf.w.srv.GitHubRefreshOff = true
	rf.w.srv.SweepGitHubRefresh(ctx)
	rf.at(120 * time.Minute)
	if _, _, err := rf.w.srv.registryToken(ctx, rf.sc.project.ID, "ghcr.io", refreshOnTask); err != nil {
		t.Fatal(err)
	}
	if _, _, n := rf.gh.state(); n != 0 || rf.storedRefresh(t) != fakeRefreshToken+"1" {
		t.Fatalf("refreshed while off: %d", n)
	}
	// Switch on: the sweep refreshes (problems still off).
	rf.w.srv.GitHubRefreshOff = false
	rf.w.srv.SweepGitHubRefresh(ctx)
	_, refresh, n := rf.gh.state()
	if n != 1 || rf.storedRefresh(t) != refresh {
		t.Fatalf("sweep refreshes %d", n)
	}
	// The new token has 3 h: the next sweep does nothing.
	rf.w.srv.SweepGitHubRefresh(ctx)
	if _, _, n := rf.gh.state(); n != 1 {
		t.Fatalf("second sweep refreshed %d", n)
	}
	// RunGitHubRefresh sweeps at start, also with problems off.
	rf.at(100*time.Minute + 3*time.Hour - 30*time.Minute)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { rf.w.srv.RunGitHubRefresh(runCtx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, n := rf.gh.state(); n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("RunGitHubRefresh did not sweep at start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	// Off: RunGitHubRefresh returns at once.
	rf.w.srv.GitHubRefreshOff = true
	rf.w.srv.RunGitHubRefresh(ctx)
	rf.assertNoTokens(t)
}

// TestGitHubRefreshRejected is acceptance 5: bad_refresh_token clears the
// refresh columns, audits once, never retries, and the next lookup answers
// login_failed with the GitHub detail text.
func TestGitHubRefreshRejected(t *testing.T) {
	rf := newRefreshFixture(t)
	ctx := context.Background()
	pid := rf.sc.project.ID
	createImage(t, rf.w, rf.owner, pid, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 600 })
	rf.connect(t)
	rf.gh.set(func(g *refreshGitHub) { g.reject = true })
	rf.gh.expireAll()
	if p := rf.access(t); p != accessLoginFailed {
		t.Fatalf("access %v", p)
	}
	mat := rf.material(t)
	if mat.HasRefresh() || mat.RefreshExpiresAt != nil || mat.AccessExpiresAt == nil {
		t.Fatal("refresh columns not cleared, or access expiry dropped")
	}
	rows := refreshAudits(rf)
	if len(rows) != 1 || rows[0].Via != audit.ViaSystem || rows[0].ProjectID == nil || *rows[0].ProjectID != pid || string(rows[0].Detail) != `{"host":"ghcr.io"}` {
		t.Fatalf("audit %+v", rows)
	}
	got, ok := problemOf(t, rf.w, store.ProblemRegistryLogin, pid.String()+":ghcr.io")
	if !ok || got.ResolvedAt != nil || got.Detail != "GitHub sign-in expired or was revoked. Reconnect GitHub in Settings." {
		t.Fatalf("problem %+v", got)
	}
	if !strings.Contains(rf.log.String(), `msg="github refresh failed" reason=rejected project_id=`+pid.String()+` host=ghcr.io`) {
		t.Fatal("no rejected Warn line")
	}
	// Never retried: more lookups and sweeps send nothing to GitHub.
	rf.access(t)
	rf.w.srv.SweepGitHubRefresh(ctx)
	if _, _, n := rf.gh.state(); n != 1 {
		t.Fatalf("rejected token retried: %d", n)
	}
	if p := rf.access(t); p != accessLoginFailed || len(refreshAudits(rf)) != 1 {
		t.Fatalf("after rejection %v audits %d", p, len(refreshAudits(rf)))
	}
	// Reconnect works as today and resolves the problem.
	rf.gh.set(func(g *refreshGitHub) { g.reject = false })
	rf.connect(t)
	if !rf.material(t).HasRefresh() {
		t.Fatal("reconnect stored no refresh token")
	}
	if got, _ := problemOf(t, rf.w, store.ProblemRegistryLogin, pid.String()+":ghcr.io"); got.ResolvedAt == nil {
		t.Fatal("reconnect did not resolve")
	}
	rf.assertNoTokens(t)
}

// TestGitHubRefreshManualDetail: a manual login keeps the old detail text.
func TestGitHubRefreshManualDetail(t *testing.T) {
	rf := newRefreshFixture(t)
	pid := rf.sc.project.ID
	createImage(t, rf.w, rf.owner, pid, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	putRegistry(t, rf.w, rf.owner, rf.project, "ghcr.io", "octo-cat", "wrong-token")
	if p := rf.access(t); p != accessLoginFailed {
		t.Fatalf("access %v", p)
	}
	if got, _ := problemOf(t, rf.w, store.ProblemRegistryLogin, pid.String()+":ghcr.io"); got.Detail != "Token expired or revoked. Reconnect it in Settings." {
		t.Fatalf("manual detail %q", got.Detail)
	}
}

// TestGitHubRefreshUnavailable is acceptance 6: a GitHub 5xx keeps the row,
// logs one Warn, uses the current token, and the next attempt refreshes.
func TestGitHubRefreshUnavailable(t *testing.T) {
	rf := newRefreshFixture(t)
	createImage(t, rf.w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 1200 })
	rf.connect(t)
	before := rf.material(t)
	rf.gh.set(func(g *refreshGitHub) { g.failStatus = http.StatusBadGateway })
	if p := rf.access(t); p != accessPrivateOK {
		t.Fatalf("access with the current token %v", p)
	}
	after := rf.material(t)
	if !bytes.Equal(after.RefreshNonce, before.RefreshNonce) || !bytes.Equal(after.Nonce, before.Nonce) || rf.storedRefresh(t) != fakeRefreshToken+"1" {
		t.Fatal("row changed after a failed refresh")
	}
	if c := strings.Count(rf.log.String(), `msg="github refresh failed" reason=unavailable project_id=`+rf.project+` host=ghcr.io`); c != 1 {
		t.Fatalf("unavailable Warn lines %d", c)
	}
	if strings.Contains(rf.log.String(), "level=ERROR") || len(refreshAudits(rf)) != 0 {
		t.Fatal("an unavailable GitHub logged an error or audited")
	}
	rf.gh.set(func(g *refreshGitHub) { g.failStatus = 0 })
	if p := rf.access(t); p != accessPrivateOK {
		t.Fatalf("access after GitHub is back %v", p)
	}
	access, refresh, n := rf.gh.state()
	if n != 2 || rf.storedRefresh(t) != refresh || access != fakeGitHubToken+"2" {
		t.Fatalf("next attempt refreshes %d", n)
	}
	rf.assertNoTokens(t)
}

// TestGitHubRefreshRevokeExpired (T1): with refresh on, a reconnect or
// disconnect whose replaced access token has expired logs the revoke
// failure as Info reason=expired and never refreshes the token it replaces;
// the refresh material goes with the row.
func TestGitHubRefreshRevokeExpired(t *testing.T) {
	rf := newRefreshFixture(t)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 600 })
	rf.connect(t)
	rf.at(time.Hour)
	// GitHub forgot the expired token: the reconnect's revoke fails.
	rf.gh.expireAll()
	rf.connect(t)
	if !strings.Contains(rf.log.String(), `level=INFO msg="github revoke failed" reason=expired`) || strings.Contains(rf.log.String(), "level=WARN") {
		t.Fatal("expired revoke not logged as Info")
	}
	if _, _, n := rf.gh.state(); n != 0 {
		t.Fatalf("reconnect refreshed the replaced token: %d", n)
	}
	if rf.storedRefresh(t) != fakeRefreshToken+"2" {
		t.Fatal("reconnect did not replace the refresh token")
	}
	// Disconnect of a live token revokes it; the row and its material go.
	status, env, raw := mustCall(t, rf.w, rf.owner, http.MethodDelete, regPath(rf.project, "ghcr.io"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	rf.seen = append(rf.seen, raw)
	if objectPayload(t, env)["revoked"] != true {
		t.Fatal("live token not revoked")
	}
	if _, _, n := rf.gh.state(); n != 0 {
		t.Fatalf("disconnect refreshed: %d", n)
	}
	if _, err := rf.w.mem.GetRegistryMaterial(context.Background(), rf.sc.project.ID, "ghcr.io"); err != store.ErrNotFound {
		t.Fatalf("row left %v", err)
	}
	// An expired token at disconnect (10 min token, an hour later): Info,
	// no refresh.
	rf.connect(t)
	rf.at(2 * time.Hour)
	rf.gh.expireAll()
	before := strings.Count(rf.log.String(), "reason=expired")
	status, env, _ = mustCall(t, rf.w, rf.owner, http.MethodDelete, regPath(rf.project, "ghcr.io"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if objectPayload(t, env)["revoked"] != false || strings.Count(rf.log.String(), "reason=expired") != before+1 {
		t.Fatal("expired disconnect not logged as Info")
	}
	if _, _, n := rf.gh.state(); n != 0 {
		t.Fatalf("disconnect refreshed: %d", n)
	}
	// A disconnect of a live token that GitHub refuses stays a Warn.
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 8 * 3600 })
	rf.connect(t)
	rf.gh.expireAll()
	status, env, _ = mustCall(t, rf.w, rf.owner, http.MethodDelete, regPath(rf.project, "ghcr.io"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if !strings.Contains(rf.log.String(), `level=WARN msg="github revoke failed"`) {
		t.Fatal("live revoke failure not a Warn")
	}
	rf.assertNoTokens(t)
}
