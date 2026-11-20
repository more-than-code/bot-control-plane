package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Connect GitHub failures (api-v1 v1.39 items 1 and 2).

const crCallback = "/v1/auth/github/callback"

func failBody(code, state, requestID string) string {
	raw, _ := json.Marshal(map[string]string{"code": code, "state": state, "connect_request_id": requestID})
	return string(raw)
}

// lastFailure is the request's last_failure as read by its creator: nil
// for JSON null. It fails the test when the key is missing.
func (f crFixture) lastFailure(t *testing.T, tok, id string) map[string]any {
	t.Helper()
	got := f.read(t, tok, id)
	raw, ok := got["last_failure"]
	if !ok {
		t.Fatalf("last_failure missing: %v", got)
	}
	if raw == nil {
		return nil
	}
	m, _ := raw.(map[string]any)
	return m
}

func (f crFixture) wantFailure(t *testing.T, tok, id, reason string) {
	t.Helper()
	lf := f.lastFailure(t, tok, id)
	if lf == nil {
		t.Fatalf("no last_failure, want %s", reason)
	}
	rawAt, _ := lf["at"].(string)
	at, _ := time.Parse(time.RFC3339, rawAt)
	if lf["reason"] != reason || len(lf) != 2 || !at.Equal(f.w.srv.Now()) {
		t.Fatalf("last_failure %v, want %s", lf, reason)
	}
	if s := f.read(t, tok, id)["state"]; s != "pending" {
		t.Fatalf("state %v after a failure", s)
	}
}

// stepLines counts the Warn lines for step since mark.
func (f crFixture) stepLines(mark int, step string) int {
	return strings.Count(f.log.String()[mark:], `level=WARN msg="github sign-in failed" step=`+step+"\n")
}

func TestConnectFailureEachStep(t *testing.T) {
	f := newCRFixture(t)
	mark := 0
	check := func(id, step, reason string) {
		t.Helper()
		if n := f.stepLines(mark, step); n != 1 {
			t.Fatalf("step %s: %d log lines in %q", step, n, f.log.String()[mark:])
		}
		f.wantFailure(t, f.owner, id, reason)
		mark = len(f.log.String())
	}

	// state: the API no longer knows the request; the console's id names it.
	id := f.create(t, f.owner, f.project, "")
	if lf := f.lastFailure(t, f.owner, id); lf != nil {
		t.Fatalf("new request has a failure: %v", lf)
	}
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, crCallback, failBody("good-code", "unknown-state", id))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgGitHubState)
	check(id, "state", "expired_sign_in")

	// exchange.
	state := f.start(t, f.owner, f.project, id)
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, crCallback, callbackBody("bad-code", state))
	mustHTTP(t, status, env, http.StatusBadGateway, "bad_gateway", msgGitHubFailed)
	check(id, "exchange", "github_error")

	// user.
	f.gh.set(func(g *fakeGitHub) { g.userFail = true })
	state = f.start(t, f.owner, f.project, id)
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, crCallback, callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusBadGateway, "bad_gateway", msgGitHubFailed)
	f.gh.set(func(g *fakeGitHub) { g.userFail = false })
	check(id, "user", "github_error")

	// scope.
	f.gh.set(func(g *fakeGitHub) { g.scopes = "repo" })
	state = f.start(t, f.owner, f.project, id)
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, crCallback, callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgGitHubScope)
	f.gh.set(func(g *fakeGitHub) { g.scopes = "read:packages" })
	check(id, "scope", "scope_missing")

	// store: a later time shows the newest failure.
	f.setNow(f.w.now.Add(time.Minute))
	state = f.start(t, f.owner, f.project, id)
	real := f.w.srv.Store
	f.w.srv.Store = failingConnect{real}
	status, _, _ = mustCall(t, f.w, f.owner, http.MethodPost, crCallback, callbackBody("good-code", state))
	f.w.srv.Store = real
	if status != http.StatusInternalServerError {
		t.Fatalf("store failure %d", status)
	}
	check(id, "store", "save_failed")

	// The page data carries it too.
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodGet, crPath(id)+"/page", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if lf, _ := objectPayload(t, env)["last_failure"].(map[string]any); lf["reason"] != "save_failed" {
		t.Fatalf("page last_failure %v", lf)
	}

	// A later success clears it with the move to connected.
	state = f.start(t, f.owner, f.project, id)
	f.callback(t, f.owner, state)
	if got := f.read(t, f.owner, id); got["state"] != "connected" || got["last_failure"] != nil {
		t.Fatalf("after success %v", got)
	}

	// No line ever holds the request id, a state, a code or a token.
	for _, secret := range []string{id, "unknown-state", "good-code", "bad-code", fakeGitHubToken, "GITHUB-BODY"} {
		if strings.Contains(f.log.String(), secret) {
			t.Fatalf("log holds %q", secret)
		}
	}
}

func TestConnectFailureForbidden(t *testing.T) {
	f := newCRFixture(t)
	id := f.create(t, f.admin, f.project, "")
	state := f.start(t, f.admin, f.project, id)
	if _, err := f.w.mem.UpdateMemberRole(context.Background(), f.sc.project.ID, f.admID, "viewer"); err != nil {
		t.Fatal(err)
	}
	status, env, _ := mustCall(t, f.w, f.admin, http.MethodPost, crCallback, callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	if n := f.stepLines(0, "forbidden"); n != 1 {
		t.Fatalf("forbidden lines %d", n)
	}
	f.wantFailure(t, f.admin, id, "forbidden")
}

func TestConnectFailureIgnoredIDs(t *testing.T) {
	f := newCRFixture(t)
	fail := func(tok, requestID string) {
		t.Helper()
		status, env, _ := mustCall(t, f.w, tok, http.MethodPost, crCallback, failBody("good-code", "unknown-state", requestID))
		mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgGitHubState)
	}
	none := func(tok, id string) {
		t.Helper()
		if lf := f.lastFailure(t, tok, id); lf != nil {
			t.Fatalf("recorded on %s: %v", id, lf)
		}
	}
	// Another user's request.
	adminID := f.create(t, f.admin, f.project, "")
	fail(f.owner, adminID)
	none(f.admin, adminID)
	// A declined request.
	declined := f.create(t, f.owner, f.project, "")
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, crPath(declined)+"/decline", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	fail(f.owner, declined)
	none(f.owner, declined)
	// A connected request.
	connected := f.create(t, f.owner, f.project, "")
	f.callback(t, f.owner, f.start(t, f.owner, f.project, connected))
	fail(f.owner, connected)
	none(f.owner, connected)
	// Malformed and unknown ids are ignored, not refused; so is none.
	for _, bad := range []string{"short", strings.Repeat("B", 43), "h-zz", ""} {
		fail(f.owner, bad)
	}
	if n := f.w.srv.GitHubFailures.Len(); n != 0 {
		t.Fatalf("%d failures recorded", n)
	}
	// An expired request.
	expiring := f.create(t, f.owner, f.project, "")
	f.setNow(f.w.now.Add(store.RequestTTL))
	fail(f.owner, expiring)
	if n := f.w.srv.GitHubFailures.Len(); n != 0 {
		t.Fatalf("%d failures recorded for an expired request", n)
	}
}

func TestConnectFailureExpiresWithRequest(t *testing.T) {
	f := newCRFixture(t)
	id := f.create(t, f.owner, f.project, "")
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, crCallback, failBody("good-code", "unknown-state", id))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgGitHubState)
	f.wantFailure(t, f.owner, id, "expired_sign_in")
	f.setNow(f.w.now.Add(store.RequestTTL))
	if got := f.read(t, f.owner, id); got["state"] != "expired" || got["last_failure"] != nil {
		t.Fatalf("after expiry %v", got)
	}
}

// TestConnectFailureNoRequest: a Settings connect (no request) logs the
// step and records nothing.
func TestConnectFailureNoRequest(t *testing.T) {
	f := newCRFixture(t)
	f.gh.set(func(g *fakeGitHub) { g.scopes = "repo" })
	state := startGitHub(t, f.w, f.owner, f.project)
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, crCallback, callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgGitHubScope)
	if f.stepLines(0, "scope") != 1 || f.w.srv.GitHubFailures.Len() != 0 {
		t.Fatalf("log %q, %d failures", f.log.String(), f.w.srv.GitHubFailures.Len())
	}
}

// TestConnectFailureOnlyWhilePending: last_failure shows only while the
// request is pending (spec-v0241-small-fixes §8 item 2). A failure written
// after a success (a late write) or on a declined request never shows.
func TestConnectFailureOnlyWhilePending(t *testing.T) {
	f := newCRFixture(t)
	fail := func(id string) {
		t.Helper()
		status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, crCallback, failBody("good-code", "unknown-state", id))
		mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgGitHubState)
	}
	hashOf := func(id string) []byte {
		t.Helper()
		hash, ok := requestHash(id, true)
		if !ok {
			t.Fatalf("hash of %s", id)
		}
		return hash
	}

	// Failure, then success: connected with null, even when a failure lands
	// in memory after the success.
	id := f.create(t, f.owner, f.project, "")
	fail(id)
	f.wantFailure(t, f.owner, id, "expired_sign_in")
	f.callback(t, f.owner, f.start(t, f.owner, f.project, id))
	f.w.srv.GitHubFailures.Record(hashOf(id), "github_error", f.w.srv.Now(), f.w.srv.Now().Add(time.Hour))
	if got := f.read(t, f.owner, id); got["state"] != "connected" || got["last_failure"] != nil {
		t.Fatalf("after success %v", got)
	}

	// Declined after a failure: null.
	declined := f.create(t, f.owner, f.project, "")
	fail(declined)
	f.wantFailure(t, f.owner, declined, "expired_sign_in")
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, crPath(declined)+"/decline", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if got := f.read(t, f.owner, declined); got["state"] != "declined" || got["last_failure"] != nil {
		t.Fatalf("after decline %v", got)
	}
}

// TestConnectFailureSeal: a seal failure logs exactly one line (the Warn
// with step=store) and records save_failed.
func TestConnectFailureSeal(t *testing.T) {
	f := newCRFixture(t)
	id := f.create(t, f.owner, f.project, "")
	state := f.start(t, f.owner, f.project, id)
	real := f.w.srv.Secrets
	f.w.srv.Secrets = &seal.Box{} // no key: Seal fails
	status, _, _ := mustCall(t, f.w, f.owner, http.MethodPost, crCallback, callbackBody("good-code", state))
	f.w.srv.Secrets = real
	if status != http.StatusInternalServerError {
		t.Fatalf("seal failure %d", status)
	}
	logs := f.log.String()
	if strings.Count(logs, "github sign-in failed") != 1 || f.stepLines(0, "store") != 1 ||
		strings.Contains(logs, "level=ERROR") || strings.Contains(logs, "encrypt failed") {
		t.Fatalf("log %q", f.log.String())
	}
	f.wantFailure(t, f.owner, id, "save_failed")
}
