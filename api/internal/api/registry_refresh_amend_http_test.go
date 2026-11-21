package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Tests for spec-github-token-refresh §10 (the review amendments).

// flakyStore fails chosen registry calls, for the store-failure paths.
type flakyStore struct {
	store.Store
	mu           sync.Mutex
	refreshFails int
	// commitFails makes a RefreshRegistry commit and then report an error
	// (a reset after commit); it applies before refreshFails.
	commitFails int
	clearFails  int
	getFails    bool
	// getFailOnCommit turns getFails on when a commitFails write commits.
	getFailOnCommit bool
	refreshCalls    int
}

var errFlaky = errors.New("store down")

func (f *flakyStore) RefreshRegistry(ctx context.Context, projectID uuid.UUID, host string, old []byte, in store.RegistryRefresh) (bool, error) {
	f.mu.Lock()
	f.refreshCalls++
	if f.commitFails > 0 {
		f.commitFails--
		if f.getFailOnCommit {
			f.getFails = true
		}
		f.mu.Unlock()
		_, _ = f.Store.RefreshRegistry(ctx, projectID, host, old, in)
		return false, errFlaky
	}
	if f.refreshFails > 0 {
		f.refreshFails--
		f.mu.Unlock()
		return false, errFlaky
	}
	f.mu.Unlock()
	return f.Store.RefreshRegistry(ctx, projectID, host, old, in)
}

func (f *flakyStore) ClearRegistryRefresh(ctx context.Context, projectID uuid.UUID, host string, old []byte) (bool, error) {
	f.mu.Lock()
	if f.clearFails > 0 {
		f.clearFails--
		f.mu.Unlock()
		return false, errFlaky
	}
	f.mu.Unlock()
	return f.Store.ClearRegistryRefresh(ctx, projectID, host, old)
}

func (f *flakyStore) GetRegistryMaterial(ctx context.Context, projectID uuid.UUID, host string) (store.RegistryMaterial, error) {
	f.mu.Lock()
	fail := f.getFails
	f.mu.Unlock()
	if fail {
		return store.RegistryMaterial{}, errFlaky
	}
	return f.Store.GetRegistryMaterial(ctx, projectID, host)
}

func (f *flakyStore) set(fn func(f *flakyStore)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

// flaky wraps the fixture's store.
func (rf *refreshFixture) flaky() *flakyStore {
	fs := &flakyStore{Store: rf.w.srv.Store}
	rf.w.srv.Store = fs
	return fs
}

func shortStoreBackoff(t *testing.T) {
	old := refreshStoreBackoff
	refreshStoreBackoff = time.Millisecond
	t.Cleanup(func() { refreshStoreBackoff = old })
}

// TestGitHubRefreshTagsAndRollback (T2): a tags lookup with 10 minutes left
// refreshes exactly once and uses the new token; a rollback frame built
// with 60 minutes left carries the refreshed token.
func TestGitHubRefreshTagsAndRollback(t *testing.T) {
	rf := newRefreshFixture(t)
	w := rf.w
	createImage(t, w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 600 })
	rf.connect(t)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 8 * 3600 })
	status, env, raw := mustCall(t, w, rf.owner, http.MethodGet, tagsPath(rf.project, "ghcr.io/joe/private:v1"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	rf.seen = append(rf.seen, raw)
	if tags, _ := tagNames(t, env); len(tags) != 1 || tags[0] != "v1" {
		t.Fatalf("tags %v", tags)
	}
	access, _, n := rf.gh.state()
	if n != 1 || access != fakeGitHubToken+"2" {
		t.Fatalf("tags lookup refreshes %d", n)
	}
	// The digest lookup with the new token: no second refresh.
	status, env, raw = mustCall(t, w, rf.owner, http.MethodGet, digestPath(rf.project, "ghcr.io/joe/private:v1"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	rf.seen = append(rf.seen, raw)
	if _, _, n := rf.gh.state(); n != 1 {
		t.Fatalf("digest lookup refreshed again: %d", n)
	}

	conn := helloAgent(t, w, rf.sc.host, rf.sc.credPlain, rf.sc.credID, allowImages(), nil)
	tasks := watchTasks(t, conn, func(msg map[string]any) map[string]any {
		return imageDeployReply(msg, shaDigest(9))
	})
	host := rf.sc.host.ID.String()
	status, env, raw = postImage(t, w, rf.owner, rf.project, "web", "deploy", host, "rb-deploy", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	rf.seen = append(rf.seen, raw)
	// Reconnect with a 60-minute token, then roll back.
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 3600 })
	rf.connect(t)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 8 * 3600 })
	status, env, raw = postImage(t, w, rf.owner, rf.project, "web", "rollback", host, "rb-1", map[string]any{"digest": shaDigest(9)})
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	rf.seen = append(rf.seen, raw)
	access, _, n = rf.gh.state()
	frame := tasks.raw(tasks.count() - 1)
	spec, _ := frame["spec"].(map[string]any)
	reg, _ := spec["registry_auth"].(map[string]any)
	if frame["kind"] != "image_rollback" || n != 2 || reg["token"] != access || access != fakeGitHubToken+"4" {
		t.Fatalf("rollback frame kind %v refreshes %d", frame["kind"], n)
	}
	rf.assertNoTokens(t)
}

// TestGitHubRefreshBoundaries (T3): 100 minutes left is not refreshed by the
// sweep (90) or a deploy (75); 45 minutes left is not refreshed on use (30).
func TestGitHubRefreshBoundaries(t *testing.T) {
	rf := newRefreshFixture(t)
	w := rf.w
	createImage(t, w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 100 * 60 })
	rf.connect(t)
	w.srv.SweepGitHubRefresh(context.Background())
	conn := helloAgent(t, w, rf.sc.host, rf.sc.credPlain, rf.sc.credID, allowImages(), nil)
	watchTasks(t, conn, func(msg map[string]any) map[string]any { return imageDeployReply(msg, shaDigest(9)) })
	status, env, raw := postImage(t, w, rf.owner, rf.project, "web", "deploy", rf.sc.host.ID.String(), "bd-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	rf.seen = append(rf.seen, raw)
	if _, _, n := rf.gh.state(); n != 0 {
		t.Fatalf("100 min left refreshed: %d", n)
	}
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 47 * 60 })
	rf.connect(t)
	if p := rf.access(t); p != accessPrivateOK {
		t.Fatalf("access %v", p)
	}
	if _, _, n := rf.gh.state(); n != 0 {
		t.Fatalf("45 min left refreshed on use: %d", n)
	}
	rf.assertNoTokens(t)
}

// slowRefresh makes the next refresh wait at GitHub and returns a channel
// that is signalled when it arrives there.
func (rf *refreshFixture) slowRefresh() chan struct{} {
	started := make(chan struct{}, 1)
	rf.gh.set(func(g *refreshGitHub) {
		g.expiresIn = 8 * 3600
		g.delay = 300 * time.Millisecond
		g.started = started
	})
	return started
}

// lookupAsync asks image-access in a goroutine; wait returns its answer.
func (rf *refreshFixture) lookupAsync(t *testing.T) (wait func() string) {
	var status int
	var env envelope
	var raw string
	done := make(chan struct{})
	go func() {
		defer close(done)
		status, env, raw = mustCall(t, rf.w, rf.owner, http.MethodGet, accessPath(rf.project, "ghcr.io/joe/private:v1"), "")
	}()
	return func() string {
		<-done
		rf.seen = append(rf.seen, raw)
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		got, _ := objectPayload(t, env)["access"].(string)
		return got
	}
}

// TestGitHubRefreshLostWrite (T5, §10.4): a manual login is stored while a
// slow refresh is at GitHub. The refresh's conditional write is lost, its
// new access token is revoked, and the lookup uses the manual login.
func TestGitHubRefreshLostWrite(t *testing.T) {
	rf := newRefreshFixture(t)
	createImage(t, rf.w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 600; g.live["manual-live-token"] = true })
	rf.connect(t)
	started := rf.slowRefresh()
	rf.at(2 * time.Minute)
	wait := rf.lookupAsync(t)
	<-started
	rf.gh.set(func(g *refreshGitHub) { g.delay = 0; g.started = nil })
	putRegistry(t, rf.w, rf.owner, rf.project, "ghcr.io", "octo-cat", "manual-live-token")
	got := wait()
	_, _, n := rf.gh.state()
	if n != 1 || got != accessPrivateOK {
		t.Fatalf("refreshes %d access %q", n, got)
	}
	// The refresh issued token 2, which no row holds: revoked.
	revoked := rf.gh.revokedTokens()
	if len(revoked) != 1 || revoked[0] != fakeGitHubToken+"2" || rf.gh.isLive(fakeGitHubToken+"2") {
		t.Fatalf("stale refreshed token not revoked (%d revokes)", len(revoked))
	}
	mat := rf.material(t)
	if mat.Source != store.RegistrySourceManual || mat.HasRefresh() || strings.Contains(rf.log.String(), `msg="github token refreshed"`) {
		t.Fatal("stale refresh stored or logged as refreshed")
	}
	rf.assertNoTokens(t)
	rf.assertNoIdle(t)
}

// TestGitHubRefreshReconnectDuringRefresh (§10.4, review item 5): a
// reconnect waits for the login's refresh lock, so the pair the refresh
// rotated is the old token it revokes; the reconnected token is kept.
func TestGitHubRefreshReconnectDuringRefresh(t *testing.T) {
	rf := newRefreshFixture(t)
	createImage(t, rf.w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 600 })
	rf.connect(t)
	started := rf.slowRefresh()
	rf.at(2 * time.Minute)
	wait := rf.lookupAsync(t)
	<-started
	rf.gh.set(func(g *refreshGitHub) { g.delay = 0; g.started = nil })
	rf.connect(t) // token 2 at GitHub; the write waits for the lock
	wait()
	if _, _, n := rf.gh.state(); n != 1 {
		t.Fatalf("refreshes %d", n)
	}
	// The refresh stored token 3 first; the reconnect replaced and revoked it.
	revoked := rf.gh.revokedTokens()
	if len(revoked) != 1 || revoked[0] != fakeGitHubToken+"3" {
		t.Fatalf("revokes %d", len(revoked))
	}
	mat := rf.material(t)
	if token, err := rf.w.srv.openRegistry(rf.sc.project.ID, mat); err != nil || token != fakeGitHubToken+"2" || rf.storedRefresh(t) != fakeRefreshToken+"2" {
		t.Fatal("reconnected token not kept")
	}
	if p := rf.access(t); p != accessPrivateOK {
		t.Fatalf("access after reconnect %v", p)
	}
	rf.assertNoTokens(t)
	rf.assertNoIdle(t)
}

// TestGitHubRefreshNotStored (§10.2): the store fails every try after GitHub
// rotated; the new pair is used at once and kept in memory, the next use
// (not due on its own) stores it with no GitHub call, nothing is audited as
// rejected and the spent refresh token is never sent again.
func TestGitHubRefreshNotStored(t *testing.T) {
	shortStoreBackoff(t)
	rf := newRefreshFixture(t)
	createImage(t, rf.w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 3600 })
	rf.connect(t)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 8 * 3600 })
	fs := rf.flaky()
	fs.set(func(f *flakyStore) { f.refreshFails = 3 })
	// A deploy-window lookup (60 min left) refreshes; the write fails 3 times.
	_, token, err := rf.w.srv.registryToken(context.Background(), rf.sc.project.ID, "ghcr.io", refreshOnTask)
	access, _, n := rf.gh.state()
	if err != nil || n != 1 || token != access || access != fakeGitHubToken+"2" {
		t.Fatalf("refresh %d err %v, new token used %v", n, err, token == access)
	}
	if fs.refreshCalls != 3 || strings.Count(rf.log.String(), `level=ERROR msg="github refresh not stored" project_id=`+rf.project+` host=ghcr.io`) != 1 {
		t.Fatalf("store tries %d", fs.refreshCalls)
	}
	if rf.storedRefresh(t) != fakeRefreshToken+"1" {
		t.Fatal("row changed")
	}
	// The next use is not due (58 min left, 30 min window) but the pending
	// pair is stored first, with no GitHub call; the lookup uses it.
	if p := rf.access(t); p != accessPrivateOK {
		t.Fatalf("access %v", p)
	}
	if _, _, n := rf.gh.state(); n != 1 {
		t.Fatalf("GitHub called again: %d", n)
	}
	if rf.storedRefresh(t) != fakeRefreshToken+"2" || len(refreshAudits(rf)) != 0 || rf.gh.rejects != 0 {
		t.Fatal("pending pair not stored, or a rejection")
	}
	if !strings.Contains(rf.log.String(), `msg="github token refreshed" project_id=`+rf.project) {
		t.Fatal("no refreshed line after the store")
	}
	// The sweep also stores a pending pair: fail again, then sweep.
	rf.at(rf.cur.Sub(rf.w.now) + 8*time.Hour)
	fs.set(func(f *flakyStore) { f.refreshFails = 3 })
	rf.w.srv.SweepGitHubRefresh(context.Background())
	if _, _, n := rf.gh.state(); n != 2 || rf.storedRefresh(t) != fakeRefreshToken+"2" {
		t.Fatalf("sweep refresh %d", n)
	}
	rf.w.srv.SweepGitHubRefresh(context.Background())
	if _, refresh, n := rf.gh.state(); n != 2 || rf.storedRefresh(t) != refresh || rf.gh.rejects != 0 {
		t.Fatalf("sweep did not store the pending pair (%d)", n)
	}
	rf.assertNoTokens(t)
}

// TestGitHubRefreshBackoff (§10.3): after an unavailable refresh, a use
// within 60 s makes no GitHub call and logs nothing more; after 60 s it
// tries again. A caller whose context ends while waiting for the login's
// lock returns promptly.
func TestGitHubRefreshBackoff(t *testing.T) {
	rf := newRefreshFixture(t)
	ctx := context.Background()
	pid := rf.sc.project.ID
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 600 })
	rf.connect(t)
	rf.gh.set(func(g *refreshGitHub) { g.failStatus = http.StatusServiceUnavailable })
	for range 2 {
		if _, token, err := rf.w.srv.registryToken(ctx, pid, "ghcr.io", refreshOnUse); err != nil || token != fakeGitHubToken+"1" {
			t.Fatal("current token not used")
		}
	}
	if _, _, n := rf.gh.state(); n != 1 || strings.Count(rf.log.String(), "reason=unavailable") != 1 {
		t.Fatalf("GitHub calls %d within the backoff", n)
	}
	rf.at(59 * time.Second)
	rf.w.srv.SweepGitHubRefresh(ctx)
	if _, _, n := rf.gh.state(); n != 1 {
		t.Fatalf("sweep called GitHub within the backoff: %d", n)
	}
	rf.gh.set(func(g *refreshGitHub) { g.failStatus = 0 })
	rf.at(61 * time.Second)
	if _, token, err := rf.w.srv.registryToken(ctx, pid, "ghcr.io", refreshOnUse); err != nil || token != fakeGitHubToken+"2" {
		t.Fatal("no refresh after the backoff")
	}

	// Lock wait respects the caller's context.
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 600 })
	rf.connect(t)
	_, unlock, err := rf.w.srv.refresh.lock(ctx, registrySubject(pid, "ghcr.io"), rf.w.srv.now)
	if err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err = rf.w.srv.registryToken(wctx, pid, "ghcr.io", refreshOnUse)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("waiting caller %v after %v", err, time.Since(start))
	}
	unlock()
	if _, token, err := rf.w.srv.registryToken(ctx, pid, "ghcr.io", refreshOnUse); err != nil || !strings.HasPrefix(token, fakeGitHubToken) {
		t.Fatal("lock not usable after a cancelled wait")
	}
	rf.assertNoTokens(t)
}

// TestGitHubRefreshClearFails (§10.5): GitHub rejects, the clear fails; the
// rejected refresh token is not sent again, the clear is retried on the
// next use, and the audit row is written once when it succeeds.
func TestGitHubRefreshClearFails(t *testing.T) {
	rf := newRefreshFixture(t)
	createImage(t, rf.w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 600 })
	rf.connect(t)
	fs := rf.flaky()
	fs.set(func(f *flakyStore) { f.clearFails = 1 })
	rf.gh.set(func(g *refreshGitHub) { g.reject = true })
	rf.gh.expireAll()
	if p := rf.access(t); p != accessLoginFailed {
		t.Fatalf("access %v", p)
	}
	if !strings.Contains(rf.log.String(), `level=ERROR msg="github refresh clear failed" project_id=`+rf.project+` host=ghcr.io`) ||
		len(refreshAudits(rf)) != 0 || !rf.material(t).HasRefresh() {
		t.Fatal("clear failure not logged, or audited, or cleared")
	}
	if p := rf.access(t); p != accessLoginFailed {
		t.Fatalf("access %v", p)
	}
	if _, _, n := rf.gh.state(); n != 1 {
		t.Fatalf("rejected refresh token sent again: %d", n)
	}
	if rf.material(t).HasRefresh() || len(refreshAudits(rf)) != 1 {
		t.Fatal("clear not retried, or audit not once")
	}
	rf.assertNoTokens(t)
}

// TestGitHubRefreshLogFields (E): the refresh path's decrypt failure and
// the sweep's per-login failure name the project and host.
func TestGitHubRefreshLogFields(t *testing.T) {
	rf := newRefreshFixture(t)
	ctx := context.Background()
	pid := rf.sc.project.ID
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 600 })
	rf.connect(t)
	// The refresh token does not open under its AAD.
	mat := rf.material(t)
	bad := store.RegistryRefresh{Nonce: mat.Nonce, Ciphertext: mat.Ciphertext, KeyVersion: mat.KeyVersion,
		RegistryRefreshMaterial: store.RegistryRefreshMaterial{RefreshNonce: mat.RefreshNonce, RefreshCiphertext: mat.Ciphertext, AccessExpiresAt: mat.AccessExpiresAt}}
	if ok, err := rf.w.mem.RefreshRegistry(ctx, pid, "ghcr.io", mat.RefreshNonce, bad); !ok || err != nil {
		t.Fatal(err)
	}
	if _, _, err := rf.w.srv.registryToken(ctx, pid, "ghcr.io", refreshOnUse); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rf.log.String(), `level=ERROR msg="decrypt failed" project_id=`+rf.project+` host=ghcr.io`) {
		t.Fatal("decrypt failed line without project and host")
	}
	// Review item 4: the next use within 60 s does not repeat the line.
	if _, _, err := rf.w.srv.registryToken(ctx, pid, "ghcr.io", refreshOnUse); err != nil {
		t.Fatal(err)
	}
	if c := strings.Count(rf.log.String(), `msg="decrypt failed"`); c != 1 {
		t.Fatalf("decrypt failed lines %d", c)
	}
	if _, _, n := rf.gh.state(); n != 0 {
		t.Fatalf("GitHub called with an unreadable refresh token: %d", n)
	}
	// The sweep's per-login failure.
	fs := rf.flaky()
	fs.set(func(f *flakyStore) { f.getFails = true })
	rf.w.srv.SweepGitHubRefresh(ctx)
	if !strings.Contains(rf.log.String(), `level=ERROR msg="github refresh sweep failed" project_id=`+rf.project+` host=ghcr.io`) {
		t.Fatal("sweep failure line without project and host")
	}
	rf.assertNoTokens(t)
}

// parkPair refreshes with the store failing every try, so the new pair
// (the fake's latest token; token 2 on a fresh fixture) is pending, and
// returns the store.
func (rf *refreshFixture) parkPair(t *testing.T) *flakyStore {
	t.Helper()
	shortStoreBackoff(t)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 600 })
	rf.connect(t)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 8 * 3600 })
	fs := rf.flaky()
	fs.set(func(f *flakyStore) { f.refreshFails = 3 })
	_, token, err := rf.w.srv.registryToken(context.Background(), rf.sc.project.ID, "ghcr.io", refreshOnUse)
	if access, _, _ := rf.gh.state(); err != nil || token != access {
		t.Fatal("refresh did not hand out the new token")
	}
	if !rf.w.srv.refresh.hasPending(registrySubject(rf.sc.project.ID, "ghcr.io")) {
		t.Fatal("no pending pair")
	}
	return fs
}

// TestGitHubRefreshAmbiguousCommit (review item 1): a write that commits and
// then reports an error is treated as stored, never as a changed row: no
// revoke, the login keeps working, no pending pair, no "not stored" line.
// Both right after GitHub answers and on a pending pair's store.
func TestGitHubRefreshAmbiguousCommit(t *testing.T) {
	shortStoreBackoff(t)
	rf := newRefreshFixture(t)
	createImage(t, rf.w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	key := registrySubject(rf.sc.project.ID, "ghcr.io")
	for i, fails := range []struct{ commit, refresh int }{{1, 0}, {1, 2}} {
		rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 600 })
		rf.connect(t)
		rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 8 * 3600 })
		fs := rf.flaky()
		fs.set(func(f *flakyStore) { f.commitFails, f.refreshFails = fails.commit, fails.refresh })
		before := len(rf.gh.revokedTokens())
		_, token, err := rf.w.srv.registryToken(context.Background(), rf.sc.project.ID, "ghcr.io", refreshOnUse)
		access, refresh, _ := rf.gh.state()
		if err != nil || token != access || !rf.gh.isLive(access) || len(rf.gh.revokedTokens()) != before {
			t.Fatalf("case %d: stored token revoked or not used (%v)", i, err)
		}
		if rf.w.srv.refresh.hasPending(key) || strings.Contains(rf.log.String(), "not stored") || rf.storedRefresh(t) != refresh {
			t.Fatalf("case %d: treated as not stored", i)
		}
		if p := rf.access(t); p != accessPrivateOK {
			t.Fatalf("case %d: access %v", i, p)
		}
		rf.w.srv.Store = fs.Store
	}
	// A pending pair whose single store try commits and errors.
	fs := rf.parkPair(t)
	fs.set(func(f *flakyStore) { f.commitFails = 1 })
	before := len(rf.gh.revokedTokens())
	if p := rf.access(t); p != accessPrivateOK {
		t.Fatalf("pending access %v", p)
	}
	access, refresh, _ := rf.gh.state()
	if len(rf.gh.revokedTokens()) != before || !rf.gh.isLive(access) || rf.w.srv.refresh.hasPending(key) || rf.storedRefresh(t) != refresh {
		t.Fatal("pending pair committed but not treated as stored")
	}
	if c := strings.Count(rf.log.String(), "not stored"); c != 1 {
		t.Fatalf("not stored lines %d", c)
	}
	rf.assertNoTokens(t)
	rf.assertNoIdle(t)
}

// TestGitHubRefreshPendingSingleTry (review item 3): storing a pending pair
// is one short try; a failure leaves it for the next use or sweep.
func TestGitHubRefreshPendingSingleTry(t *testing.T) {
	rf := newRefreshFixture(t)
	createImage(t, rf.w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	fs := rf.parkPair(t)
	fs.set(func(f *flakyStore) { f.refreshFails = 10; f.refreshCalls = 0 })
	if p := rf.access(t); p != accessPrivateOK {
		t.Fatalf("access with the pending token %v", p)
	}
	if fs.refreshCalls != 1 || strings.Count(rf.log.String(), "not stored") != 2 {
		t.Fatalf("pending store tries %d", fs.refreshCalls)
	}
	fs.set(func(f *flakyStore) { f.refreshFails = 0 })
	rf.w.srv.SweepGitHubRefresh(context.Background())
	if _, refresh, n := rf.gh.state(); n != 1 || rf.storedRefresh(t) != refresh {
		t.Fatalf("sweep did not store the pending pair (%d)", n)
	}
	rf.assertNoTokens(t)
	rf.assertNoIdle(t)
}

// TestGitHubRefreshPendingReplaced (review item 7): a reconnect over a row
// with a pending pair revokes the pending (live) token and drops it; the
// next use refreshes nothing.
func TestGitHubRefreshPendingReplaced(t *testing.T) {
	rf := newRefreshFixture(t)
	createImage(t, rf.w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	fs := rf.parkPair(t)
	fs.set(func(f *flakyStore) { f.refreshFails = 0 })
	rf.connect(t)
	revoked := rf.gh.revokedTokens()
	if len(revoked) != 1 || revoked[0] != fakeGitHubToken+"2" || rf.w.srv.refresh.hasPending(registrySubject(rf.sc.project.ID, "ghcr.io")) {
		t.Fatalf("pending token not revoked or kept (%d revokes)", len(revoked))
	}
	if token, err := rf.w.srv.openRegistry(rf.sc.project.ID, rf.material(t)); err != nil || token != fakeGitHubToken+"3" {
		t.Fatal("reconnected token not stored")
	}
	if p := rf.access(t); p != accessPrivateOK {
		t.Fatalf("access %v", p)
	}
	if _, _, n := rf.gh.state(); n != 1 {
		t.Fatalf("refreshes %d", n)
	}
	rf.assertNoTokens(t)
	rf.assertNoIdle(t)
}

// TestGitHubRefreshPendingDeleted (review items 2, 5 and 7): a disconnect
// over a pending pair revokes the pending (live) token; a row deleted
// behind the API's back drops the pending pair, the backoff and the
// rejected nonce on the next refresh, and the state entry goes.
func TestGitHubRefreshPendingDeleted(t *testing.T) {
	rf := newRefreshFixture(t)
	ctx := context.Background()
	pid := rf.sc.project.ID
	key := registrySubject(pid, "ghcr.io")
	fs := rf.parkPair(t)
	fs.set(func(f *flakyStore) { f.refreshFails = 0 })
	status, env, raw := mustCall(t, rf.w, rf.owner, http.MethodDelete, regPath(rf.project, "ghcr.io"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	rf.seen = append(rf.seen, raw)
	revoked := rf.gh.revokedTokens()
	if objectPayload(t, env)["revoked"] != true || len(revoked) != 1 || revoked[0] != fakeGitHubToken+"2" || rf.w.srv.refresh.size() != 0 {
		t.Fatalf("disconnect over a pending pair (%d revokes)", len(revoked))
	}
	// Behind the API's back: park again, delete the row in the store.
	rf.w.srv.Store = fs.Store
	fs = rf.parkPair(t)
	fs.set(func(f *flakyStore) { f.refreshFails = 0 })
	rf.w.srv.refresh.keys[key].rejectedNonce = []byte("x")
	rf.w.srv.refresh.keys[key].retryAfter = rf.w.srv.now().Add(time.Minute)
	if err := rf.w.mem.DeleteRegistry(ctx, pid, "ghcr.io"); err != nil {
		t.Fatal(err)
	}
	rf.w.srv.SweepGitHubRefresh(ctx)
	revoked = rf.gh.revokedTokens()
	if len(revoked) != 2 || revoked[1] != fakeGitHubToken+"4" || rf.w.srv.refresh.size() != 0 {
		t.Fatalf("deleted row kept state (%d revokes, %d entries)", len(revoked), rf.w.srv.refresh.size())
	}
	rf.assertNoTokens(t)
}

// waitLockUsers waits until key's refresh lock has n users (holder and
// waiters), so a request is known to be blocked on it.
func (rf *refreshFixture) waitLockUsers(t *testing.T, key string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		l := &rf.w.srv.refresh
		l.mu.Lock()
		k := l.keys[key]
		users := 0
		if k != nil {
			users = k.users
		}
		l.mu.Unlock()
		if users >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock users %d, want %d", users, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TestGitHubRefreshAmbiguousUnreadable (round 3 item 1): the first write
// commits and errors, the retry finds the row rotated (false, nil) and the
// re-read fails. The outcome is unknown, so nothing is revoked and the pair
// stays pending; the next use settles it as stored via holdsWrite.
func TestGitHubRefreshAmbiguousUnreadable(t *testing.T) {
	shortStoreBackoff(t)
	rf := newRefreshFixture(t)
	createImage(t, rf.w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	key := registrySubject(rf.sc.project.ID, "ghcr.io")
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 600 })
	rf.connect(t)
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 8 * 3600 })
	fs := rf.flaky()
	fs.set(func(f *flakyStore) { f.commitFails, f.getFailOnCommit = 1, true })
	_, token, err := rf.w.srv.registryToken(context.Background(), rf.sc.project.ID, "ghcr.io", refreshOnUse)
	access, refresh, n := rf.gh.state()
	if err != nil || token != access || n != 1 || len(rf.gh.revokeAttempts()) != 0 || !rf.gh.isLive(access) {
		t.Fatalf("unknown write: err %v, revokes %d", err, len(rf.gh.revokeAttempts()))
	}
	if !rf.w.srv.refresh.hasPending(key) || strings.Count(rf.log.String(), "not stored") != 1 {
		t.Fatal("pair not kept pending")
	}
	fs.set(func(f *flakyStore) { f.getFails = false })
	if p := rf.access(t); p != accessPrivateOK {
		t.Fatalf("access %v", p)
	}
	if _, _, n := rf.gh.state(); n != 1 || rf.w.srv.refresh.hasPending(key) || rf.storedRefresh(t) != refresh || len(rf.gh.revokeAttempts()) != 0 {
		t.Fatalf("not settled as stored (%d refreshes)", n)
	}
	rf.assertNoTokens(t)
	rf.assertNoIdle(t)
}

// TestGitHubRefreshSealFails (round 3 item 2, round 4 items 2 and 4):
// sealing fails after GitHub rotated. The unsealed pair is kept and its
// token used. Every pair here lives 10 minutes, so each later use is due:
// a use that bypassed the pending pair would send the spent refresh token.
// Instead the pending pair, once due, refreshes with its own refresh token
// and is replaced; GitHub never sees a spent or unknown refresh token. The
// "encrypt failed" line is written at most once a minute. When sealing
// works again the pair is stored.
func TestGitHubRefreshSealFails(t *testing.T) {
	rf := newRefreshFixture(t)
	createImage(t, rf.w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	key := registrySubject(rf.sc.project.ID, "ghcr.io")
	ctx := context.Background()
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 600 })
	rf.connect(t)
	sealDown := true
	rf.w.srv.refreshSealFail = func() error {
		if sealDown {
			return errors.New("seal down")
		}
		return nil
	}
	encryptLines := func() int { return strings.Count(rf.log.String(), `msg="encrypt failed" project_id=`+rf.project) }
	_, token, err := rf.w.srv.registryToken(ctx, rf.sc.project.ID, "ghcr.io", refreshOnUse)
	access, _, n := rf.gh.state()
	if err != nil || token != access || n != 1 || !rf.w.srv.refresh.hasPending(key) || encryptLines() != 1 {
		t.Fatalf("seal failure: err %v refreshes %d", err, n)
	}
	// Within the minute: the due pending pair refreshes with its own
	// refresh token (GitHub honours it), the new pair is used, one line.
	rf.at(20 * time.Second)
	_, token, err = rf.w.srv.registryToken(ctx, rf.sc.project.ID, "ghcr.io", refreshOnUse)
	access, _, n = rf.gh.state()
	if err != nil || token != access || n != 2 || encryptLines() != 1 {
		t.Fatalf("due unsealed pair: refreshes %d, encrypt lines %d", n, encryptLines())
	}
	// Past the minute: another line; still no spent token at GitHub.
	if p := rf.access(t); p != accessPrivateOK {
		t.Fatalf("access with the unsealed pair %v", p)
	}
	if encryptLines() != 2 || rf.gh.rejects != 0 {
		t.Fatalf("encrypt lines %d, rejects %d", encryptLines(), rf.gh.rejects)
	}
	// Sealing works again: the next use stores the (due, so refreshed) pair.
	sealDown = false
	if p := rf.access(t); p != accessPrivateOK {
		t.Fatalf("access after the seal works %v", p)
	}
	_, refresh, _ := rf.gh.state()
	if rf.w.srv.refresh.hasPending(key) || rf.storedRefresh(t) != refresh || rf.gh.rejects != 0 {
		t.Fatalf("pair not stored after the seal recovered (rejects %d)", rf.gh.rejects)
	}
	rf.assertNoTokens(t)
	rf.assertNoIdle(t)
}

// TestGitHubRefreshUnsealedRejected (round 4 item 4): a due unsealed pair
// whose refresh token GitHub rejects ends the login: the pair is dropped,
// the row's spent refresh token is cleared (never sent) and audited once.
func TestGitHubRefreshUnsealedRejected(t *testing.T) {
	rf := newRefreshFixture(t)
	ctx := context.Background()
	key := registrySubject(rf.sc.project.ID, "ghcr.io")
	rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 600 })
	rf.connect(t)
	rf.w.srv.refreshSealFail = func() error { return errors.New("seal down") }
	if _, _, err := rf.w.srv.registryToken(ctx, rf.sc.project.ID, "ghcr.io", refreshOnUse); err != nil {
		t.Fatal(err)
	}
	rf.gh.set(func(g *refreshGitHub) { g.reject = true })
	if _, _, err := rf.w.srv.registryToken(ctx, rf.sc.project.ID, "ghcr.io", refreshOnUse); err != nil {
		t.Fatal(err)
	}
	if rf.w.srv.refresh.hasPending(key) || rf.material(t).HasRefresh() || len(refreshAudits(rf)) != 1 {
		t.Fatal("rejected unsealed pair not ended")
	}
	// The row has no refresh token left: nothing more goes to GitHub.
	if _, _, err := rf.w.srv.registryToken(ctx, rf.sc.project.ID, "ghcr.io", refreshOnUse); err != nil {
		t.Fatal(err)
	}
	if _, _, n := rf.gh.state(); n != 2 {
		t.Fatalf("refreshes %d", n)
	}
	rf.assertNoTokens(t)
	rf.assertNoIdle(t)
}

// swapGitHubRow writes a different github row behind the API's back (no
// refresh lock), so a pending pair becomes stale.
func (rf *refreshFixture) swapGitHubRow(t *testing.T, token string) {
	t.Helper()
	pid := rf.sc.project.ID
	nonce, ct, err := rf.w.srv.Secrets.Seal(seal.RegistryAAD(pid.String(), "ghcr.io"), []byte(token))
	if err != nil {
		t.Fatal(err)
	}
	owner := rf.w.user("owner@ex.com", "Owner")
	if _, err := rf.w.mem.PutRegistry(context.Background(), pid, "ghcr.io", store.RegistryWrite{
		Username: "octo-cat", Nonce: nonce, Ciphertext: ct, KeyVersion: seal.KeyVersion,
		Source: store.RegistrySourceGitHub, ConnectedBy: &owner.ID,
	}, owner.ID, rf.w.now); err != nil {
		t.Fatal(err)
	}
}

// TestGitHubRefreshStalePendingRevoked (round 3 item 3): a pending pair
// whose row changed since is revoked by a reconnect and by a disconnect.
func TestGitHubRefreshStalePendingRevoked(t *testing.T) {
	rf := newRefreshFixture(t)
	fs := rf.parkPair(t)
	fs.set(func(f *flakyStore) { f.refreshFails = 0 })
	parked, _, _ := rf.gh.state()
	rf.swapGitHubRow(t, "swapped-row-token-1")
	rf.connect(t)
	if !contains(rf.gh.revokeAttempts(), parked) || !contains(rf.gh.revokedTokens(), parked) || rf.w.srv.refresh.hasPending(registrySubject(rf.sc.project.ID, "ghcr.io")) {
		t.Fatal("reconnect did not revoke the stale pending token")
	}
	rf.w.srv.Store = fs.Store
	fs = rf.parkPair(t)
	fs.set(func(f *flakyStore) { f.refreshFails = 0 })
	parked, _, _ = rf.gh.state()
	rf.swapGitHubRow(t, "swapped-row-token-2")
	status, env, raw := mustCall(t, rf.w, rf.owner, http.MethodDelete, regPath(rf.project, "ghcr.io"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	rf.seen = append(rf.seen, raw)
	if !contains(rf.gh.revokedTokens(), parked) || rf.w.srv.refresh.size() != 0 {
		t.Fatal("disconnect did not revoke the stale pending token")
	}
	rf.assertNoTokens(t)
}

// TestGitHubRefreshDisconnectManualSwap (round 3 item 4): a manual login
// stored while the disconnect waits for the lock is left alone: 409
// conflict, no audit, no revoke, and the manual login keeps working.
func TestGitHubRefreshDisconnectManualSwap(t *testing.T) {
	rf := newRefreshFixture(t)
	createImage(t, rf.w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)
	rf.connect(t)
	rf.gh.set(func(g *refreshGitHub) { g.live["manual-pat-value"] = true })
	_, unlock, err := rf.w.srv.refresh.lock(context.Background(), registrySubject(rf.sc.project.ID, "ghcr.io"), rf.w.srv.now)
	if err != nil {
		t.Fatal(err)
	}
	var status int
	var env envelope
	var raw string
	done := make(chan struct{})
	go func() {
		defer close(done)
		status, env, raw, _ = callRaw(rf.w, rf.owner, http.MethodDelete, regPath(rf.project, "ghcr.io"), "")
	}()
	rf.waitLockUsers(t, registrySubject(rf.sc.project.ID, "ghcr.io"), 2) // the handler read the github row and waits
	putRegistry(t, rf.w, rf.owner, rf.project, "ghcr.io", "octo-cat", "manual-pat-value")
	audits := len(rf.w.mem.AllAudits())
	unlock()
	<-done
	rf.seen = append(rf.seen, raw)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "The registry login changed; reload and try again")
	if len(rf.gh.revokeAttempts()) != 0 || len(rf.w.mem.AllAudits()) != audits {
		t.Fatalf("revoke attempts %d, audits %d", len(rf.gh.revokeAttempts()), len(rf.w.mem.AllAudits())-audits)
	}
	if mat := rf.material(t); mat.Source != store.RegistrySourceManual {
		t.Fatal("manual row not kept")
	}
	if p := rf.access(t); p != accessPrivateOK {
		t.Fatalf("manual login access %v", p)
	}
	rf.assertNoTokens(t)
	rf.assertNoIdle(t)
}

// TestGitHubRefreshReconnectLockWait (round 3 item 5): a reconnect whose
// wait for the login's lock ends (its bound, or the browser going away)
// answers GitHub sign-in failed, revokes the new token, leaves the row,
// and the OAuth state cannot be used again.
func TestGitHubRefreshReconnectLockWait(t *testing.T) {
	rf := newRefreshFixture(t)
	rf.connect(t)
	before := rf.material(t)
	old := githubConnectLockWait
	githubConnectLockWait = 50 * time.Millisecond
	t.Cleanup(func() { githubConnectLockWait = old })
	_, unlock, err := rf.w.srv.refresh.lock(context.Background(), registrySubject(rf.sc.project.ID, "ghcr.io"), rf.w.srv.now)
	if err != nil {
		t.Fatal(err)
	}
	state := startGitHub(t, rf.w, rf.owner, rf.project)
	status, env, raw := mustCall(t, rf.w, rf.owner, http.MethodPost, "/v1/auth/github/callback", callbackBody("good-code", state))
	rf.seen = append(rf.seen, raw)
	mustHTTP(t, status, env, http.StatusBadGateway, "bad_gateway", "GitHub sign-in failed; try again")
	issued, _, _ := rf.gh.state()
	if !contains(rf.gh.revokedTokens(), issued) || !bytes.Equal(rf.material(t).Nonce, before.Nonce) {
		t.Fatal("new token not revoked, or the row changed")
	}
	status, env, _ = mustCall(t, rf.w, rf.owner, http.MethodPost, "/v1/auth/github/callback", callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "this GitHub sign-in expired or was already used; start again")

	// The browser goes away while the callback waits (bound long).
	githubConnectLockWait = time.Minute
	first := issued
	state = startGitHub(t, rf.w, rf.owner, rf.project)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, rf.w.http.URL+"/v1/auth/github/callback", strings.NewReader(callbackBody("good-code", state)))
	req.Header.Set("Authorization", "Bearer "+rf.owner)
	if res, err := rf.w.client.Do(req); err == nil {
		res.Body.Close()
		t.Fatal("cancelled request answered")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		issued, _, _ = rf.gh.state()
		if issued != first && contains(rf.gh.revokedTokens(), issued) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("abandoned reconnect did not revoke its token")
		}
		time.Sleep(10 * time.Millisecond)
	}
	unlock()
	if !bytes.Equal(rf.material(t).Nonce, before.Nonce) {
		t.Fatal("abandoned reconnect changed the row")
	}
	rf.assertNoTokens(t)
	rf.assertNoIdle(t)
}
