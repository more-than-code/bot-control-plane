package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	changelog "github.com/more-than-code/bot-control-plane"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.41 (spec-bell-problems): problems in the bell.

// problemWire is one Problem of the problem routes.
type problemWire struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Level      string     `json:"level"`
	Title      string     `json:"title"`
	Detail     string     `json:"detail"`
	Href       string     `json:"href"`
	OpenedAt   time.Time  `json:"opened_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
	HostID     *string    `json:"host_id"`
	ProjectID  *string    `json:"project_id"`
	EntryID    *string    `json:"entry_id"`
}

func getProblems(t *testing.T, w *world, token, path string) []problemWire {
	t.Helper()
	status, env, raw := mustCall(t, w, token, http.MethodGet, path, "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	var out struct {
		Payload struct {
			Items []problemWire `json:"items"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if out.Payload.Items == nil {
		t.Fatalf("items must be an array: %s", raw)
	}
	return out.Payload.Items
}

// sweepNow is one sweep with its hourly work (run in a goroutine) done.
func sweepNow(ctx context.Context, s *Server) {
	s.SweepProblems(ctx)
	s.sweep.hourly.Wait()
}

// problemClock makes the server clock cur and returns a setter.
func problemClock(w *world) func(time.Time) {
	var mu sync.Mutex
	cur := w.now
	w.srv.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return cur
	}
	return func(t time.Time) {
		mu.Lock()
		cur = t
		mu.Unlock()
	}
}

// allProblems is every stored problem (open, and resolved since the epoch).
func allProblems(t *testing.T, w *world) []store.Problem {
	t.Helper()
	since := time.Time{}
	rows, err := w.mem.ListProblems(context.Background(), store.ProblemFilter{All: true, ResolvedSince: &since})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func problemOf(t *testing.T, w *world, kind, subject string) (store.Problem, bool) {
	t.Helper()
	for _, p := range allProblems(t, w) {
		if p.Kind == kind && p.Subject == subject {
			return p, true
		}
	}
	return store.Problem{}, false
}

// assertNoSecrets: no title or detail carries any of the words (tokens,
// usernames, emails, env values), an @ or a line break.
func assertNoSecrets(t *testing.T, w *world, words ...string) {
	t.Helper()
	for _, p := range allProblems(t, w) {
		for _, text := range []string{p.Title, p.Detail} {
			if strings.ContainsAny(text, "@\n\r") {
				t.Fatalf("problem text %q", text)
			}
			for _, word := range words {
				if strings.Contains(text, word) {
					t.Fatalf("problem text %q holds %q", text, word)
				}
			}
		}
	}
}

// enrollAsleep adds a host with always_on false to the project's org.
func enrollAsleep(t *testing.T, w *world, project store.Project, by uuid.UUID, name string) store.Host {
	t.Helper()
	ctx := context.Background()
	_, hash, _ := secret.NewToken()
	if _, err := w.mem.CreateJoinToken(ctx, project.OrgID, hash, nil, nil, by, w.now.Add(time.Hour), w.now); err != nil {
		t.Fatal(err)
	}
	_, cred, _ := secret.NewToken()
	h, err := w.mem.ConsumeJoinToken(ctx, hash, store.EnrollInput{Name: name, NodeType: "pi", OS: "linux", Arch: "arm64", AgentVersion: "0.2.9"}, uuid.New(), cred, w.now)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestProblemsHostSweep is acceptance 2.
func TestProblemsHostSweep(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	set := problemClock(w)
	owner := w.user("owner@ex.com", "Owner")
	p, err := w.mem.CreateProject(ctx, w.mem.PersonalOrg(owner.ID), owner.ID, "Proj", "proj", w.now)
	if err != nil {
		t.Fatal(err)
	}
	quiet := enrollAt(t, w, p, owner.ID, "pi2-2", "0.2.9")
	recent := enrollAt(t, w, p, owner.ID, "recent", "0.2.9")
	asleep := enrollAsleep(t, w, p, owner.ID, "laptop")
	revoked := enrollAt(t, w, p, owner.ID, "gone", "0.2.9")
	start := w.now
	w.srv.sweep.start = start
	// Everyone was last seen at start.
	for _, h := range []store.Host{quiet, recent, asleep, revoked} {
		touchAt(t, w, h, start, "")
	}
	if err := w.mem.RevokeHost(ctx, revoked.ID, start); err != nil {
		t.Fatal(err)
	}

	// The first 5 minutes after start: nothing, even for a quiet host.
	touchAt(t, w, quiet, start.Add(-time.Hour), "")
	set(start.Add(4*time.Minute + 59*time.Second))
	sweepNow(ctx, w.srv)
	if n := len(allProblems(t, w)); n != 0 {
		t.Fatalf("problems in the grace period %d", n)
	}

	// 11 minutes quiet opens one needs_action problem; 9 minutes does not;
	// asleep and revoked hosts never do.
	at := start.Add(20 * time.Minute)
	set(at)
	touchAt(t, w, quiet, at.Add(-11*time.Minute), "")
	touchAt(t, w, recent, at.Add(-9*time.Minute), "")
	touchAt(t, w, asleep, at.Add(-time.Hour), "")
	sweepNow(ctx, w.srv)
	rows := allProblems(t, w)
	if len(rows) != 1 {
		t.Fatalf("problems %+v", rows)
	}
	open := rows[0]
	if open.Kind != store.ProblemHostOffline || open.Subject != quiet.ID.String() || open.Level != store.ProblemNeedsAction ||
		open.Title != "pi2-2 is offline" || open.Detail != "No report for 11 min." || open.HostID == nil || *open.HostID != quiet.ID ||
		open.OrgID != quiet.OrgID || open.ResolvedAt != nil {
		t.Fatalf("open %+v", open)
	}
	// It lasts: same row, detail moves on.
	set(at.Add(time.Minute))
	sweepNow(ctx, w.srv)
	if p, _ := problemOf(t, w, store.ProblemHostOffline, quiet.ID.String()); p.ID != open.ID || p.Detail != "No report for 12 min." {
		t.Fatalf("lasting %+v", p)
	}
	// Between 2 and 10 minutes: left open (no flapping on the edge).
	touchAt(t, w, quiet, at.Add(-5*time.Minute), "")
	sweepNow(ctx, w.srv)
	if p, _ := problemOf(t, w, store.ProblemHostOffline, quiet.ID.String()); p.ResolvedAt != nil {
		t.Fatal("resolved at 5 minutes")
	}
	// Fresh again: resolved, "back online" (no duration).
	set(at.Add(3 * time.Minute))
	touchAt(t, w, quiet, at.Add(3*time.Minute-20*time.Second), "")
	sweepNow(ctx, w.srv)
	back, _ := problemOf(t, w, store.ProblemHostOffline, quiet.ID.String())
	if back.ResolvedAt == nil || back.Title != "pi2-2 is back online" || back.Detail != "Reporting again." {
		t.Fatalf("back %+v", back)
	}
	// Flapping within 30 minutes reuses the row.
	set(at.Add(20 * time.Minute))
	sweepNow(ctx, w.srv)
	again, _ := problemOf(t, w, store.ProblemHostOffline, quiet.ID.String())
	rowsFor := 0
	for _, p := range allProblems(t, w) {
		if p.Subject == quiet.ID.String() {
			rowsFor++
		}
	}
	if again.ID != open.ID || again.ResolvedAt != nil || rowsFor != 1 {
		t.Fatalf("flap %+v", allProblems(t, w))
	}
	// Revoked (and so archived) hosts with an open problem get it resolved.
	if err := w.mem.RevokeHost(ctx, quiet.ID, at.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	set(at.Add(21 * time.Minute))
	sweepNow(ctx, w.srv)
	if p, _ := problemOf(t, w, store.ProblemHostOffline, quiet.ID.String()); p.ResolvedAt == nil || !strings.Contains(p.Detail, "No longer checked") {
		t.Fatalf("revoked %+v", p)
	}
	assertNoSecrets(t, w, "owner@ex.com")
}

// TestProblemsHourlyPrune: resolved problems older than 30 days go in the
// hourly work, and the hourly work runs once an hour.
func TestProblemsHourlyPrune(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	set := problemClock(w)
	owner := w.user("owner@ex.com", "Owner")
	org := w.mem.PersonalOrg(owner.ID)
	w.srv.sweep.start = w.now.Add(-time.Hour)
	mk := func(subject string, resolved time.Time) {
		if _, err := w.mem.OpenProblem(ctx, store.ProblemOpen{Kind: store.ProblemHostOffline, Subject: subject, OrgID: org,
			Level: store.ProblemNeedsAction, Title: "x is offline"}, resolved.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := w.mem.ResolveProblem(ctx, store.ProblemHostOffline, subject, "", "", resolved); err != nil {
			t.Fatal(err)
		}
	}
	mk("old", w.now.Add(-31*24*time.Hour))
	sweepNow(ctx, w.srv)
	if len(allProblems(t, w)) != 0 {
		t.Fatal("old problem kept")
	}
	mk("old2", w.now.Add(-31*24*time.Hour))
	mk("recent", w.now.Add(-29*24*time.Hour))
	set(w.now.Add(59 * time.Minute))
	sweepNow(ctx, w.srv)
	if len(allProblems(t, w)) != 2 {
		t.Fatal("pruned within the hour")
	}
	set(w.now.Add(60 * time.Minute))
	sweepNow(ctx, w.srv)
	if rows := allProblems(t, w); len(rows) != 1 || rows[0].Subject != "recent" {
		t.Fatalf("hourly prune %+v", rows)
	}
	// No second run while one is still going.
	mk("old3", w.now.Add(-31*24*time.Hour))
	w.srv.sweep.mu.Lock()
	w.srv.sweep.hourlyBusy = true
	w.srv.sweep.mu.Unlock()
	set(w.now.Add(3 * time.Hour))
	sweepNow(ctx, w.srv)
	if len(allProblems(t, w)) != 2 {
		t.Fatal("hourly work overlapped a running one")
	}
	w.srv.sweep.mu.Lock()
	w.srv.sweep.hourlyBusy = false
	w.srv.sweep.mu.Unlock()
	sweepNow(ctx, w.srv)
	if len(allProblems(t, w)) != 1 {
		t.Fatal("hourly work did not run after the earlier one ended")
	}
}

// TestProblemsRegistry is acceptance 3.
func TestProblemsRegistry(t *testing.T) {
	f := newRegistryFixture(t)
	ctx := context.Background()
	w := f.w
	set := problemClock(w)
	w.srv.Secrets = testBox(t)
	pid := f.sc.project.ID
	subject := pid.String() + ":ghcr.io"
	f.private("robot-user", "right-token-123", true)
	createImage(t, w, f.owner, pid, "web", `{"kind":"service","image":"ghcr.io/joe/private:v1"}`)

	putRegistry(t, w, f.owner, f.project, "ghcr.io", "robot-user", "wrong-token-456")
	if p := accessOf(t, f, f.owner, "ghcr.io/joe/private:v1"); p["access"] != "login_failed" {
		t.Fatalf("access %v", p)
	}
	got, ok := problemOf(t, w, store.ProblemRegistryLogin, subject)
	if !ok || got.Level != store.ProblemNeedsAction || got.Title != "ghcr.io login was refused" ||
		got.Detail != "Token expired or revoked. Reconnect it in Settings." || got.ProjectID == nil || *got.ProjectID != pid || got.ResolvedAt != nil {
		t.Fatalf("opened %+v", got)
	}
	// A cached answer keeps it open (one row).
	accessOf(t, f, f.owner, "ghcr.io/joe/private:v1")
	if n := len(allProblems(t, w)); n != 1 {
		t.Fatalf("rows %d", n)
	}
	// The registries section is the link.
	items := getProblems(t, w, f.owner, "/v1/projects/"+f.project+"/problems")
	if len(items) != 1 || items[0].Href != "/projects/"+f.project+"?tab=settings#registries" || items[0].Kind != "registry_login" {
		t.Fatalf("route %+v", items)
	}
	// Replacing the login resolves it.
	putRegistry(t, w, f.owner, f.project, "ghcr.io", "robot-user", "right-token-123")
	if got, _ := problemOf(t, w, store.ProblemRegistryLogin, subject); got.ResolvedAt == nil || got.Title != "ghcr.io login was replaced" {
		t.Fatalf("replaced %+v", got)
	}
	// Wrong again, then a private_ok lookup resolves it (the same row: within 30 min).
	putRegistry(t, w, f.owner, f.project, "ghcr.io", "robot-user", "wrong-token-456")
	accessOf(t, f, f.owner, "ghcr.io/joe/private:v1")
	reopened, _ := problemOf(t, w, store.ProblemRegistryLogin, subject)
	if reopened.ID != got.ID || reopened.ResolvedAt != nil {
		t.Fatalf("reopen %+v", reopened)
	}
	f.private("robot-user", "wrong-token-456", true)
	set(w.now.Add(2 * time.Minute)) // past the 60 s cache
	accessOf(t, f, f.owner, "ghcr.io/joe/private:v1")
	if got, _ := problemOf(t, w, store.ProblemRegistryLogin, subject); got.ResolvedAt == nil || got.Title != "ghcr.io login works again" {
		t.Fatalf("private_ok %+v", got)
	}
	// Deleting the login resolves it.
	f.private("robot-user", "right-token-123", true)
	set(w.now.Add(4 * time.Minute))
	accessOf(t, f, f.owner, "ghcr.io/joe/private:v1")
	if got, _ := problemOf(t, w, store.ProblemRegistryLogin, subject); got.ResolvedAt != nil {
		t.Fatalf("not reopened %+v", got)
	}
	status, env, _ := mustCall(t, w, f.owner, http.MethodDelete, regPath(f.project, "ghcr.io"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if got, _ := problemOf(t, w, store.ProblemRegistryLogin, subject); got.ResolvedAt == nil || got.Title != "ghcr.io login was removed" {
		t.Fatalf("deleted %+v", got)
	}
	assertNoSecrets(t, w, "robot-user", "right-token-123", "wrong-token-456", "owner@ex.com")
	_ = ctx
}

// TestProblemsRegistryHourly: the sweep looks up one catalog image per
// stored login once an hour, and opens or resolves from that answer.
func TestProblemsRegistryHourly(t *testing.T) {
	f := newRegistryFixture(t)
	ctx := context.Background()
	w := f.w
	set := problemClock(w)
	w.srv.Secrets = testBox(t)
	pid := f.sc.project.ID
	subject := pid.String() + ":ghcr.io"
	f.private("robot-user", "right-token-123", true)
	createImage(t, w, f.owner, pid, "b-web", `{"kind":"service","image":"ghcr.io/joe/second:v1"}`)
	createImage(t, w, f.owner, pid, "a-web", `{"kind":"service","image":"ghcr.io/joe/first:v1"}`)
	createImage(t, w, f.owner, pid, "nginx", `{"kind":"service","image":"nginx"}`)
	putRegistry(t, w, f.owner, f.project, "ghcr.io", "robot-user", "wrong-token-456")
	manifests := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for k, v := range f.calls {
			if strings.Contains(k, "/manifests/") {
				n += v
			}
		}
		return n
	}
	w.srv.sweep.start = w.now.Add(-time.Hour)
	sweepNow(ctx, w.srv)
	got, ok := problemOf(t, w, store.ProblemRegistryLogin, subject)
	if !ok || got.ResolvedAt != nil {
		t.Fatalf("hourly check did not open %+v", got)
	}
	// One login, one image (the first by name on that registry).
	first := manifests()
	if first == 0 || f.upstream("HEAD ghcr.io/v2/joe/first/manifests/v1") != first {
		t.Fatalf("lookups %d %v", first, f.calls)
	}
	// Sweeps within the hour (and past the 60 s cache) look nothing up.
	for _, m := range []time.Duration{2, 30, 59} {
		set(w.now.Add(m * time.Minute))
		sweepNow(ctx, w.srv)
	}
	if manifests() != first {
		t.Fatalf("lookups within the hour %d", manifests()-first)
	}
	// The next hour checks again; a fixed token resolves.
	putRegistry(t, w, f.owner, f.project, "ghcr.io", "robot-user", "right-token-123")
	putRegistry(t, w, f.owner, f.project, "ghcr.io", "robot-user", "wrong-token-456")
	if _, err := w.mem.OpenProblem(ctx, store.ProblemOpen{Kind: store.ProblemRegistryLogin, Subject: subject, OrgID: f.sc.project.OrgID,
		ProjectID: &pid, Level: store.ProblemNeedsAction, Title: "ghcr.io login was refused"}, w.now.Add(59*time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.private("robot-user", "wrong-token-456", true)
	set(w.now.Add(60 * time.Minute))
	sweepNow(ctx, w.srv)
	if manifests() == first {
		t.Fatal("no lookup in the second hour")
	}
	if got, _ := problemOf(t, w, store.ProblemRegistryLogin, subject); got.ResolvedAt == nil {
		t.Fatalf("hourly private_ok %+v", got)
	}
	// A login whose project is gone: its open problem resolves in the hourly work.
	other, err := w.mem.CreateProject(ctx, f.sc.project.OrgID, f.sc.project.CreatedBy, "Gone", "gone", w.now)
	if err != nil {
		t.Fatal(err)
	}
	oid := other.ID
	if _, err := w.mem.OpenProblem(ctx, store.ProblemOpen{Kind: store.ProblemRegistryLogin, Subject: oid.String() + ":ghcr.io", OrgID: other.OrgID,
		ProjectID: &oid, Level: store.ProblemNeedsAction, Title: "ghcr.io login was refused"}, w.now); err != nil {
		t.Fatal(err)
	}
	set(w.now.Add(120 * time.Minute))
	sweepNow(ctx, w.srv)
	if got, _ := problemOf(t, w, store.ProblemRegistryLogin, oid.String()+":ghcr.io"); got.ResolvedAt == nil {
		t.Fatalf("orphan %+v", got)
	}
	assertNoSecrets(t, w, "robot-user", "right-token-123", "wrong-token-456")
}

// TestProblemsRegistryWalk: the hourly check walks the project's images on
// the registry by name past public ones, stops at the first other answer,
// and resolves the problem once no image is left on that registry.
func TestProblemsRegistryWalk(t *testing.T) {
	f := newRegistryFixture(t)
	ctx := context.Background()
	w := f.w
	set := problemClock(w)
	w.srv.Secrets = testBox(t)
	pid := f.sc.project.ID
	subject := pid.String() + ":ghcr.io"
	// joe/pub answers anonymously; every other repository needs the right login.
	f.fake.Handle(func(rw http.ResponseWriter, r *http.Request) {
		f.count(r)
		if r.URL.Path == "/token" {
			if u, p, ok := r.BasicAuth(); ok && (u != "robot-user" || p != "right-token-123") {
				rw.WriteHeader(http.StatusUnauthorized)
				return
			} else if ok {
				fmt.Fprint(rw, `{"token":"good"}`)
				return
			}
			fmt.Fprint(rw, `{"token":"anon"}`)
			return
		}
		auth := r.Header.Get("Authorization")
		switch {
		case strings.Contains(r.URL.Path, "/joe/pub/") || auth == "Bearer good":
			rw.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("e", 64))
		case auth == "":
			rw.Header().Set("WWW-Authenticate", `Bearer realm="https://`+r.Host+`/token",service="`+r.Host+`"`)
			rw.WriteHeader(http.StatusUnauthorized)
		default:
			rw.WriteHeader(http.StatusForbidden)
		}
	})
	createImage(t, w, f.owner, pid, "a-pub", `{"kind":"service","image":"ghcr.io/joe/pub:v1"}`)
	createImage(t, w, f.owner, pid, "b-priv", `{"kind":"service","image":"ghcr.io/joe/priv:v1"}`)
	createImage(t, w, f.owner, pid, "c-priv", `{"kind":"service","image":"ghcr.io/joe/later:v1"}`)
	putRegistry(t, w, f.owner, f.project, "ghcr.io", "robot-user", "wrong-token-456")
	w.srv.sweep.start = w.now.Add(-time.Hour)
	sweepNow(ctx, w.srv)
	got, ok := problemOf(t, w, store.ProblemRegistryLogin, subject)
	if !ok || got.ResolvedAt != nil || got.Title != "ghcr.io login was refused" {
		t.Fatalf("a public first image hid the dead login %+v", got)
	}
	// Walked a-pub then b-priv, and stopped there.
	if f.upstream("HEAD ghcr.io/v2/joe/pub/manifests/v1") == 0 || f.upstream("HEAD ghcr.io/v2/joe/priv/manifests/v1") == 0 ||
		f.upstream("HEAD ghcr.io/v2/joe/later/manifests/v1") != 0 {
		t.Fatalf("walk %v", f.calls)
	}
	// No image left on that registry: the next hour resolves it.
	for _, name := range []string{"a-pub", "b-priv", "c-priv"} {
		status, env, _ := mustCall(t, w, f.owner, http.MethodDelete, "/v1/projects/"+f.project+"/catalog/"+name, "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	}
	createImage(t, w, f.owner, pid, "nginx", `{"kind":"service","image":"nginx"}`)
	set(w.now.Add(time.Hour))
	sweepNow(ctx, w.srv)
	if got, _ := problemOf(t, w, store.ProblemRegistryLogin, subject); got.ResolvedAt == nil ||
		got.Title != "ghcr.io login is no longer checked" || got.Detail != "No catalog image uses this registry any more." {
		t.Fatalf("no image left %+v", got)
	}
}

// deployScene: an image entry "web" in a project, a host with deploy access.
type deployScene struct {
	w       *world
	owner   store.User
	project store.Project
	host    store.Host
	entry   store.CatalogEntry
}

func newDeployScene(t *testing.T) deployScene {
	t.Helper()
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	p, err := w.mem.CreateProject(context.Background(), w.mem.PersonalOrg(owner.ID), owner.ID, "Proj", "proj", w.now)
	if err != nil {
		t.Fatal(err)
	}
	host := enrollAt(t, w, p, owner.ID, "pi1", "0.2.9")
	createImage(t, w, w.token(owner.ID), p.ID, "web", `{"kind":"service","image":"nginx","env":{"MODE":"prod-value-42"}}`)
	entry, err := w.mem.GetCatalogEntry(context.Background(), p.ID, "web")
	if err != nil {
		t.Fatal(err)
	}
	return deployScene{w: w, owner: owner, project: p, host: host, entry: entry}
}

func (d deployScene) finish(t *testing.T, outcome string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	row, err := d.w.mem.BeginImageOp(ctx, d.entry.ID, d.host.ID, d.entry.SpecHash, d.owner.ID, "console", at, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.w.mem.FinishImageDeploy(ctx, row.ID, outcome, nil, nil, at); err != nil {
		t.Fatal(err)
	}
	d.w.srv.DeployHook(ctx, d.project.ID, d.entry.ID, d.entry.Name, d.host.ID)
}

// TestProblemsDeploySweep: the minute sweep resolves an open deploy
// problem when the host is revoked or archived or the project loses deploy
// access to it, and leaves it open otherwise.
func TestProblemsDeploySweep(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(t *testing.T, d deployScene)
		detail string
	}{
		"unchanged": {func(*testing.T, deployScene) {}, ""},
		"revoked": {func(t *testing.T, d deployScene) {
			if err := d.w.mem.RevokeHost(context.Background(), d.host.ID, d.w.now); err != nil {
				t.Fatal(err)
			}
		}, "No longer checked: the host was revoked."},
		"archived": {func(t *testing.T, d deployScene) {
			ctx := context.Background()
			if err := d.w.mem.RevokeHost(ctx, d.host.ID, d.w.now); err != nil {
				t.Fatal(err)
			}
			if _, _, err := d.w.mem.ArchiveHost(ctx, d.host.ID, d.w.now); err != nil {
				t.Fatal(err)
			}
		}, "No longer checked: the host was removed."},
		"access removed": {func(t *testing.T, d deployScene) {
			if _, err := d.w.mem.DeleteHostAccess(context.Background(), d.host.ID, d.project.ID, true); err != nil {
				t.Fatal(err)
			}
		}, "No longer checked: the project has no deploy access to the host."},
		"access lowered to metrics": {func(t *testing.T, d deployScene) {
			if _, _, err := d.w.mem.PutHostAccess(context.Background(), d.host.ID, d.project.ID, "metrics", d.owner.ID, d.w.now); err != nil {
				t.Fatal(err)
			}
		}, "No longer checked: the project has no deploy access to the host."},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			d := newDeployScene(t)
			subject := d.entry.ID.String() + ":" + d.host.ID.String()
			d.finish(t, "failed", d.w.now)
			tc.change(t, d)
			d.w.srv.sweep.start = d.w.now.Add(-time.Hour)
			touchAt(t, d.w, d.host, d.w.now, "")
			sweepNow(ctx, d.w.srv)
			p, ok := problemOf(t, d.w, store.ProblemDeploy, subject)
			if !ok {
				t.Fatal("no problem")
			}
			if tc.detail == "" {
				if p.ResolvedAt != nil {
					t.Fatalf("resolved %+v", p)
				}
				return
			}
			if p.ResolvedAt == nil || p.Detail != tc.detail || p.Title != "web deploy failed on pi1" {
				t.Fatalf("not resolved %+v", p)
			}
		})
	}
}

// TestProblemsDeployOutcomes is acceptance 4 at the hook: each listed
// outcome opens or updates one problem with its level; succeeded and
// unchanged resolve; auto_restarted leaves it.
func TestProblemsDeployOutcomes(t *testing.T) {
	d := newDeployScene(t)
	subject := d.entry.ID.String() + ":" + d.host.ID.String()
	levels := map[string]string{
		"failed": "heads_up", "timeout": "heads_up", "image_unavailable": "heads_up", "forbidden_spec": "heads_up",
		"conflict": "heads_up", "rolled_back": "heads_up", "rollback_failed": "needs_action",
	}
	at := d.w.now
	var id uuid.UUID
	for outcome, level := range levels {
		at = at.Add(time.Minute)
		d.finish(t, outcome, at)
		p, ok := problemOf(t, d.w, store.ProblemDeploy, subject)
		if !ok || p.Level != level || p.ResolvedAt != nil || !strings.HasPrefix(p.Title, "web ") || !strings.Contains(p.Title, " on pi1") ||
			p.EntryID == nil || *p.EntryID != d.entry.ID || p.HostID == nil || *p.HostID != d.host.ID {
			t.Fatalf("%s: %+v", outcome, p)
		}
		if id != uuid.Nil && p.ID != id {
			t.Fatalf("%s: a second row", outcome)
		}
		id = p.ID
	}
	for _, outcome := range []string{"succeeded", "unchanged"} {
		at = at.Add(time.Minute)
		d.finish(t, "failed", at)
		at = at.Add(time.Minute)
		d.finish(t, outcome, at)
		if p, _ := problemOf(t, d.w, store.ProblemDeploy, subject); p.ResolvedAt == nil || p.Title != "web is running on pi1" {
			t.Fatalf("%s: %+v", outcome, p)
		}
	}
	// Agent auto actions: needs_action for gave_up and failed, heads_up for a rollback.
	ctx := context.Background()
	for outcome, level := range map[string]string{"auto_gave_up": "needs_action", "auto_failed": "needs_action", "auto_rolled_back": "heads_up"} {
		at = at.Add(time.Minute)
		if _, _, err := d.w.mem.RecordAutoAction(ctx, store.AutoAction{ID: uuid.New(), EntryID: d.entry.ID, HostID: d.host.ID, Outcome: outcome, At: at}); err != nil {
			t.Fatal(err)
		}
		d.w.srv.DeployHook(ctx, d.project.ID, d.entry.ID, d.entry.Name, d.host.ID)
		if p, _ := problemOf(t, d.w, store.ProblemDeploy, subject); p.ResolvedAt != nil || p.Level != level {
			t.Fatalf("%s: %+v", outcome, p)
		}
	}
	// auto_restarted is neither a failure nor a recovery.
	at = at.Add(time.Minute)
	if _, _, err := d.w.mem.RecordAutoAction(ctx, store.AutoAction{ID: uuid.New(), EntryID: d.entry.ID, HostID: d.host.ID, Outcome: "auto_restarted", At: at}); err != nil {
		t.Fatal(err)
	}
	d.w.srv.DeployHook(ctx, d.project.ID, d.entry.ID, d.entry.Name, d.host.ID)
	if p, _ := problemOf(t, d.w, store.ProblemDeploy, subject); p.ResolvedAt != nil {
		t.Fatalf("auto_restarted resolved %+v", p)
	}
	// The bell link opens the entry in Catalog & services on that host.
	items := getProblems(t, d.w, d.w.token(d.owner.ID), "/v1/projects/"+d.project.ID.String()+"/problems")
	want := "/projects/" + d.project.ID.String() + "?catalog.host=" + d.host.ID.String() + "&catalog.q=web&tab=catalog"
	if len(items) != 1 || items[0].Href != want {
		t.Fatalf("href %+v want %s", items, want)
	}
	// Deleting the entry deletes its problems with its deploy history.
	status, env, _ := mustCall(t, d.w, d.w.token(d.owner.ID), http.MethodDelete, "/v1/projects/"+d.project.ID.String()+"/catalog/web", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if _, ok := problemOf(t, d.w, store.ProblemDeploy, subject); ok {
		t.Fatal("problem outlived the entry")
	}
	assertNoSecrets(t, d.w, "prod-value-42", "owner@ex.com")
}

// TestProblemsDeployHTTP: the real deploy and remove paths call the hook.
func TestProblemsDeployHTTP(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	set := problemClock(w)
	created := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	subject := created["id"].(string) + ":" + sc.host.ID.String()
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	var mu sync.Mutex
	next := "rolled_back"
	removeFails := false
	watchTasks(t, conn, func(msg map[string]any) map[string]any {
		mu.Lock()
		outcome := next
		mu.Unlock()
		status := map[string]any{"container": "web", "state": "running", "restart_count": 0, "ports": []int{}}
		if msg["kind"] == "image_remove" {
			mu.Lock()
			fails := removeFails
			mu.Unlock()
			if fails {
				// A failed remove still carries a status payload.
				return map[string]any{"v": 1, "type": "task_result", "task_id": msg["task_id"], "state": "failed", "error_code": "failed",
					"payload": map[string]any{"status": status}}
			}
			return map[string]any{"v": 1, "type": "task_result", "task_id": msg["task_id"], "state": "succeeded",
				"payload": map[string]any{"status": map[string]any{"container": "web", "state": "missing", "restart_count": 0, "ports": []int{}}}}
		}
		payload := map[string]any{"outcome": outcome, "digest": shaDigest(3), "exit_code": 0, "status": status, "lines": []string{"x"}}
		if outcome == "unchanged" {
			return map[string]any{"v": 1, "type": "task_result", "task_id": msg["task_id"], "state": "succeeded", "payload": payload}
		}
		return map[string]any{"v": 1, "type": "task_result", "task_id": msg["task_id"], "state": "failed", "error_code": outcome, "payload": payload}
	})
	host := sc.host.ID.String()
	status, env, _ := postImage(t, w, tok, project, "web", "deploy", host, "p-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if p, ok := problemOf(t, w, store.ProblemDeploy, subject); !ok || p.Title != "web deploy rolled back on pi4" ||
		p.Detail != "Unhealthy after deploy; the previous version is running." || p.Level != store.ProblemHeadsUp {
		t.Fatalf("rolled back %+v", p)
	}
	set(w.now.Add(time.Second))
	mu.Lock()
	next = "unchanged"
	mu.Unlock()
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "p-2", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if p, _ := problemOf(t, w, store.ProblemDeploy, subject); p.ResolvedAt == nil {
		t.Fatalf("unchanged %+v", p)
	}
	set(w.now.Add(2 * time.Second))
	mu.Lock()
	next = "rollback_failed"
	mu.Unlock()
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "p-3", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if p, _ := problemOf(t, w, store.ProblemDeploy, subject); p.ResolvedAt != nil || p.Level != store.ProblemNeedsAction {
		t.Fatalf("rollback_failed %+v", p)
	}
	// A remove that failed (with a status payload) leaves the problem open.
	set(w.now.Add(3 * time.Second))
	mu.Lock()
	removeFails = true
	mu.Unlock()
	status, env, _ = postImage(t, w, tok, project, "web", "remove", host, "p-4f", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if p, _ := problemOf(t, w, store.ProblemDeploy, subject); p.ResolvedAt != nil {
		t.Fatalf("failed remove resolved %+v", p)
	}
	mu.Lock()
	removeFails = false
	mu.Unlock()
	set(w.now.Add(4 * time.Second))
	status, env, _ = postImage(t, w, tok, project, "web", "remove", host, "p-4", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if p, _ := problemOf(t, w, store.ProblemDeploy, subject); p.ResolvedAt == nil || p.Title != "web was removed from pi4" {
		t.Fatalf("remove %+v", p)
	}
}

// audienceScene: in one org, an owner, an org admin, a project admin who is
// not in the org, a project viewer holding web's deploy action, a plain
// viewer, an org member with no project role, and an outsider. One open
// problem of each kind.
type audienceScene struct {
	w                                                     *world
	project                                               store.Project
	owner, orgAdmin, projAdmin, deployer, viewer, orgOnly string
	outsider, delegated, deployerDelegated, viewerDeleg   string
	orgAdminViewer                                        string
	hostP, regP, depP                                     store.Problem
}

func newAudienceScene(t *testing.T) audienceScene {
	t.Helper()
	w := newWorld(t)
	ctx := context.Background()
	owner := w.user("owner@ex.com", "Owner")
	orgAdmin := w.user("oa@ex.com", "Org Admin")
	projAdmin := w.user("pa@ex.com", "Proj Admin")
	deployer := w.user("dep@ex.com", "Deployer")
	viewer := w.user("view@ex.com", "Viewer")
	orgOnly := w.user("om@ex.com", "Org Member")
	outsider := w.user("out@ex.com", "Outsider")
	orgAdminViewer := w.user("oav@ex.com", "Org Admin Viewer")
	org := w.mem.PersonalOrg(owner.ID)
	for _, u := range []uuid.UUID{orgAdmin.ID, orgAdminViewer.ID} {
		if _, err := w.mem.AddOrgMember(ctx, org, u, store.OrgRoleAdmin, owner.ID, w.now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.mem.AddOrgMember(ctx, org, orgOnly.ID, store.OrgRoleMember, owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	p, err := w.mem.CreateProject(ctx, org, owner.ID, "Proj", "proj", w.now)
	if err != nil {
		t.Fatal(err)
	}
	for u, role := range map[uuid.UUID]string{projAdmin.ID: "admin", deployer.ID: "viewer", viewer.ID: "viewer", orgAdminViewer.ID: "viewer"} {
		if _, err := w.mem.AddMember(ctx, p.ID, u, role, owner.ID, w.now); err != nil {
			t.Fatal(err)
		}
	}
	host := enrollAt(t, w, p, owner.ID, "pi1", "0.2.9")
	createImage(t, w, w.token(owner.ID), p.ID, "web", `{"kind":"service","image":"nginx"}`)
	entry, err := w.mem.GetCatalogEntry(ctx, p.ID, "web")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.mem.ReplaceCatalogGrants(ctx, p.ID, "web", []store.CatalogGrant{{Subject: "user:" + deployer.ID.String(), Actions: []string{"deploy"}}}); err != nil {
		t.Fatal(err)
	}
	hid, pid, eid := host.ID, p.ID, entry.ID
	open := func(in store.ProblemOpen, at time.Time) store.Problem {
		got, err := w.mem.OpenProblem(ctx, in, at)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	s := audienceScene{w: w, project: p,
		owner: w.token(owner.ID), orgAdmin: w.token(orgAdmin.ID), projAdmin: w.token(projAdmin.ID), deployer: w.token(deployer.ID),
		viewer: w.token(viewer.ID), orgOnly: w.token(orgOnly.ID), outsider: w.token(outsider.ID),
		delegated: tokenKind(t, w, owner.ID, store.KindDelegated), deployerDelegated: tokenKind(t, w, deployer.ID, store.KindDelegated),
		viewerDeleg:    tokenKind(t, w, viewer.ID, store.KindDelegated),
		orgAdminViewer: w.token(orgAdminViewer.ID),
	}
	s.hostP = open(store.ProblemOpen{Kind: store.ProblemHostOffline, Subject: hid.String(), OrgID: org, HostID: &hid,
		Level: store.ProblemNeedsAction, Title: "pi1 is offline", Detail: "No report for 11 min."}, w.now.Add(-3*time.Minute))
	s.regP = open(store.ProblemOpen{Kind: store.ProblemRegistryLogin, Subject: pid.String() + ":ghcr.io", OrgID: org, ProjectID: &pid,
		Level: store.ProblemNeedsAction, Title: "ghcr.io login was refused"}, w.now.Add(-2*time.Minute))
	s.depP = open(store.ProblemOpen{Kind: store.ProblemDeploy, Subject: eid.String() + ":" + hid.String(), OrgID: org, ProjectID: &pid, HostID: &hid, EntryID: &eid,
		Level: store.ProblemHeadsUp, Title: "web deploy failed on pi1"}, w.now.Add(-time.Minute))
	return s
}

func problemKinds(items []problemWire) string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Kind
	}
	return strings.Join(out, ",")
}

// TestProblemsAudience is api-v1 v1.41 item 2 on the bell and both routes.
func TestProblemsAudience(t *testing.T) {
	s := newAudienceScene(t)
	w := s.w
	org := "/v1/orgs/" + s.project.OrgID.String() + "/problems"
	proj := "/v1/projects/" + s.project.ID.String() + "/problems"
	bellKinds := func(tok string) string {
		var out []string
		for _, it := range getNotifications(t, w, tok).Items {
			if it.Kind == "problem" {
				out = append(out, it.Title)
			}
		}
		return strings.Join(out, "|")
	}
	// Needs action first, newest first within a level.
	all := "ghcr.io login was refused|pi1 is offline|web deploy failed on pi1"
	projectOnly := "ghcr.io login was refused|web deploy failed on pi1"
	for name, tc := range map[string]struct {
		tok, bell, org, proj string
	}{
		"org owner": {s.owner, all, "registry_login,host_offline,deploy", "registry_login,deploy"},
		"org admin": {s.orgAdmin, all, "registry_login,host_offline,deploy", "registry_login,deploy"},
		// An org admin with a lower explicit project role is still a project admin.
		"org admin, project viewer": {s.orgAdminViewer, all, "registry_login,host_offline,deploy", "registry_login,deploy"},
		"project admin":             {s.projAdmin, projectOnly, "not_found", "registry_login,deploy"},
		"deploy grant member":       {s.deployer, "web deploy failed on pi1", "not_found", "deploy"},
		"viewer":                    {s.viewer, "", "not_found", ""},
		"org member, no role":       {s.orgOnly, "", "forbidden", "not_found"},
		"outsider":                  {s.outsider, "", "not_found", "not_found"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := bellKinds(tc.tok); got != tc.bell {
				t.Fatalf("bell %q want %q", got, tc.bell)
			}
			for path, want := range map[string]string{org: tc.org, proj: tc.proj} {
				status, env, raw := mustCall(t, w, tc.tok, http.MethodGet, path, "")
				switch want {
				case "not_found":
					mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
				case "forbidden":
					mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
				default:
					mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
					var out struct {
						Payload struct {
							Items []problemWire `json:"items"`
						} `json:"payload"`
					}
					if err := json.Unmarshal([]byte(raw), &out); err != nil {
						t.Fatal(err)
					}
					if got := problemKinds(out.Payload.Items); got != want {
						t.Fatalf("%s: %q want %q", path, got, want)
					}
				}
			}
		})
	}
	// Delegated sessions: allowed on both problem routes with the same
	// audience, still refused on the bell.
	if got := problemKinds(getProblems(t, w, s.delegated, org)); got != "registry_login,host_offline,deploy" {
		t.Fatalf("delegated org %q", got)
	}
	if got := problemKinds(getProblems(t, w, s.deployerDelegated, proj)); got != "deploy" {
		t.Fatalf("delegated deployer %q", got)
	}
	if got := problemKinds(getProblems(t, w, s.viewerDeleg, proj)); got != "" {
		t.Fatalf("delegated viewer %q", got)
	}
	assertNoDelegatedDenied(t, w)
	status, env, _ := mustCall(t, w, s.delegated, http.MethodGet, "/v1/me/notifications", "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
}

// TestProblemsRouteShape: the Problem fields, open only by default,
// ?resolved=1 adds the last 24 hours, other values are refused.
func TestProblemsRouteShape(t *testing.T) {
	s := newAudienceScene(t)
	w := s.w
	ctx := context.Background()
	org := "/v1/orgs/" + s.project.OrgID.String() + "/problems"
	items := getProblems(t, w, s.owner, org)
	if len(items) != 3 {
		t.Fatalf("items %+v", items)
	}
	var h problemWire
	for _, it := range items {
		if it.Kind == "host_offline" {
			h = it
		}
	}
	if h.ID != s.hostP.ID.String() || h.Level != "needs_action" || h.Href != "/hosts/"+s.hostP.HostID.String() ||
		h.HostID == nil || h.ProjectID != nil || h.EntryID != nil || h.ResolvedAt != nil || !h.OpenedAt.Equal(w.now.Add(-3*time.Minute)) {
		t.Fatalf("host problem %+v", h)
	}
	status, env, raw := mustCall(t, w, s.owner, http.MethodGet, org, "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	var shape struct {
		Payload struct {
			Items []map[string]any `json:"items"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(raw), &shape); err != nil {
		t.Fatal(err)
	}
	if len(shape.Payload.Items[0]) != 12 {
		t.Fatalf("fields %v", shape.Payload.Items[0])
	}
	// Resolved 23 h ago is in with ?resolved=1; 25 h ago is not.
	if _, err := w.mem.ResolveProblem(ctx, store.ProblemHostOffline, s.hostP.Subject, "", "", w.now.Add(-23*time.Hour+time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := problemKinds(getProblems(t, w, s.owner, org)); got != "registry_login,deploy" {
		t.Fatalf("open only %q", got)
	}
	if got := problemKinds(getProblems(t, w, s.owner, org+"?resolved=1")); !strings.Contains(got, "host_offline") {
		t.Fatalf("resolved=1 %q", got)
	}
	if _, err := w.mem.ResolveProblem(ctx, store.ProblemRegistryLogin, s.regP.Subject, "", "", w.now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := problemKinds(getProblems(t, w, s.owner, org+"?resolved=1")); strings.Contains(got, "registry_login") {
		t.Fatalf("resolved 25 h ago %q", got)
	}
	status, env, _ = mustCall(t, w, s.owner, http.MethodGet, org+"?resolved=yes", "")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid resolved")
}

// TestProblemsBell is api-v1 v1.41 item 3: kind problem with level and
// state, open first (needs_action first) and never cut, resolved never
// unread, read marks on problem keys.
func TestProblemsBell(t *testing.T) {
	s := newAudienceScene(t)
	w := s.w
	ctx := context.Background()
	w.srv.releases = func() []changelog.Entry { return releasesAt(w.now, 0, 1, 2, 3, 4) }
	n := getNotifications(t, w, s.owner)
	if n.Items[0].Key != "problem:"+s.regP.ID.String() || n.Items[1].Key != "problem:"+s.hostP.ID.String() || n.Items[2].Key != "problem:"+s.depP.ID.String() {
		t.Fatalf("order %v", wireKeys(n))
	}
	if n.Unread != 8 {
		t.Fatalf("unread %d", n.Unread)
	}
	_, _, raw := mustCall(t, w, s.owner, http.MethodGet, "/v1/me/notifications", "")
	if !strings.Contains(raw, `"kind":"problem"`) || !strings.Contains(raw, `"level":"needs_action"`) || !strings.Contains(raw, `"state":"open"`) ||
		strings.Count(raw, `"level"`) != 3 || strings.Count(raw, `"state"`) != 3 {
		t.Fatalf("problem fields only on problem items: %s", raw)
	}
	// 30 open problems are never cut; the rest fill nothing.
	org := s.project.OrgID
	for i := 0; i < 30; i++ {
		if _, err := w.mem.OpenProblem(ctx, store.ProblemOpen{Kind: store.ProblemHostOffline, Subject: fmt.Sprintf("extra-%d", i), OrgID: org,
			Level: store.ProblemHeadsUp, Title: fmt.Sprintf("h%d is offline", i)}, w.now); err != nil {
			t.Fatal(err)
		}
	}
	n = getNotifications(t, w, s.owner)
	if len(n.Items) != 33 {
		t.Fatalf("items %d", len(n.Items))
	}
	for _, it := range n.Items {
		if !strings.HasPrefix(it.Key, "problem:") {
			t.Fatalf("a release beat an open problem: %v", wireKeys(n))
		}
	}
	for i := 0; i < 30; i++ {
		if _, err := w.mem.ResolveProblem(ctx, store.ProblemHostOffline, fmt.Sprintf("extra-%d", i), "", "", w.now.Add(-time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	// Resolved problems are read and fill the rest up to 20, by time with the releases.
	n = getNotifications(t, w, s.owner)
	if len(n.Items) != 20 || !strings.HasPrefix(n.Items[2].Key, "problem:"+s.depP.ID.String()) {
		t.Fatalf("items %d %v", len(n.Items), wireKeys(n))
	}
	sawResolved := false
	for _, it := range n.Items {
		if strings.HasPrefix(it.Title, "h") && strings.HasSuffix(it.Title, "is offline") {
			sawResolved = true
			if !it.Read {
				t.Fatalf("resolved unread %+v", it)
			}
		}
	}
	if !sawResolved {
		t.Fatal("no resolved problem in the bell")
	}
	// Older than 7 days: gone from the bell.
	if _, err := w.mem.ResolveProblem(ctx, store.ProblemDeploy, s.depP.Subject, "", "", w.now.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, it := range getNotifications(t, w, s.owner).Items {
		if it.Key == "problem:"+s.depP.ID.String() {
			t.Fatal("resolved 8 days ago still in the bell")
		}
	}
	// Marks on problem keys, one at a time and with all.
	status, env, _ := markRead(t, w, s.owner, `{"keys":["problem:`+s.regP.ID.String()+`"]}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	n = getNotifications(t, w, s.owner)
	if n.Items[0].Key != "problem:"+s.regP.ID.String() || !n.Items[0].Read {
		t.Fatalf("marked %+v", n.Items[0])
	}
	status, env, _ = markRead(t, w, s.owner, `{"all":true}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if n := getNotifications(t, w, s.owner); n.Unread != 0 {
		t.Fatalf("after all %d", n.Unread)
	}
}

// TestProblemsOff: BOT_API_PROBLEMS=off writes nothing and answers empty lists.
func TestProblemsOff(t *testing.T) {
	s := newAudienceScene(t)
	w := s.w
	w.srv.ProblemsOff = true
	ctx := context.Background()
	if got := getProblems(t, w, s.owner, "/v1/orgs/"+s.project.OrgID.String()+"/problems"); len(got) != 0 {
		t.Fatalf("org %+v", got)
	}
	if got := getProblems(t, w, s.owner, "/v1/projects/"+s.project.ID.String()+"/problems?resolved=1"); len(got) != 0 {
		t.Fatalf("project %+v", got)
	}
	for _, it := range getNotifications(t, w, s.owner).Items {
		if it.Kind == "problem" {
			t.Fatalf("bell %+v", it)
		}
	}
	before := len(allProblems(t, w))
	d := newDeployScene(t)
	d.w.srv.ProblemsOff = true
	d.finish(t, "failed", d.w.now)
	d.w.srv.sweep.start = d.w.now.Add(-time.Hour)
	touchAt(t, d.w, d.host, d.w.now.Add(-time.Hour), "")
	sweepNow(ctx, d.w.srv)
	d.w.srv.noteRegistryAccess(ctx, d.project.ID, "ghcr.io", loginManual, accessLoginFailed)
	if n := len(allProblems(t, d.w)); n != 0 {
		t.Fatalf("problems written while off %d", n)
	}
	if len(allProblems(t, w)) != before {
		t.Fatal("changed")
	}
}
