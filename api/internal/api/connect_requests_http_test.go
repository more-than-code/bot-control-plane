package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/github"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Connect requests (api-v1 v1.31).

const crReason = "Pull ghcr.io/acme/api for the deploy"

type crFixture struct {
	*registryFixture
	gh    *fakeGitHub
	log   fmt.Stringer
	ownID uuid.UUID
	admID uuid.UUID
	admD  string // admin, api_delegated
}

func newCRFixture(t *testing.T) crFixture {
	t.Helper()
	f := newRegistryFixture(t)
	log := captureLog(f.w)
	gh := newFakeGitHub(t)
	wireGitHub(f.w, gh)
	f.w.srv.Secrets = testBox(t)
	admID := f.w.user("admin@ex.com", "Admin").ID
	return crFixture{
		registryFixture: f, gh: gh, log: log,
		ownID: f.w.user("owner@ex.com", "Owner").ID,
		admID: admID, admD: tokenKind(t, f.w, admID, store.KindDelegated),
	}
}

func crCreatePath(project string) string {
	return "/v1/projects/" + project + "/registries/github/requests"
}
func crPath(id string) string { return "/v1/registry-connect-requests/" + id }

// create opens a request and returns its id after checking the 201 body.
func (f crFixture) create(t *testing.T, tok, project, reason string) string {
	t.Helper()
	body := "{}"
	if reason != "" {
		raw, _ := json.Marshal(map[string]string{"reason": reason})
		body = string(raw)
	}
	status, env, _ := mustCall(t, f.w, tok, http.MethodPost, crCreatePath(project), body)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	out := objectPayload(t, env)
	id, _ := out["request_id"].(string)
	if len(id) != 43 || !requestIDPattern.MatchString(id) {
		t.Fatalf("request_id %q", id)
	}
	if out["url"] != "http://console.test/connect/"+id || len(out) != 3 {
		t.Fatalf("create body %v", out)
	}
	return id
}

func (f crFixture) read(t *testing.T, tok, id string) map[string]any {
	t.Helper()
	status, env, _ := mustCall(t, f.w, tok, http.MethodGet, crPath(id), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	return objectPayload(t, env)
}

func (f crFixture) state(t *testing.T, id string) string {
	t.Helper()
	s, _ := f.read(t, f.del, id)["state"].(string)
	return s
}

// start continues a request: the state from a successful start.
func (f crFixture) start(t *testing.T, tok, project, id string) string {
	t.Helper()
	status, env, _ := mustCall(t, f.w, tok, http.MethodPost, githubStartPath(project), `{"connect_request_id":"`+id+`"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	u, err := url.Parse(objectPayload(t, env)["authorize_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

func (f crFixture) callback(t *testing.T, tok, state string) (map[string]any, string) {
	t.Helper()
	status, env, raw := mustCall(t, f.w, tok, http.MethodPost, "/v1/auth/github/callback", callbackBody("good-code", state))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	return objectPayload(t, env), raw
}

func (f crFixture) setNow(at time.Time) { f.w.srv.Now = func() time.Time { return at } }

func TestConnectRequestNotConfigured(t *testing.T) {
	f := newRegistryFixture(t)
	path := crCreatePath(f.project)
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, path, `{}`)
	mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", "GitHub is not configured")
	wireGitHub(f.w, newFakeGitHub(t))
	// Configured but no secrets key: the same answer (item 2 check 1).
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, path, `{}`)
	mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", "GitHub is not configured")
	// The configuration check comes before the reason check.
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, path, `{"reason":"`+strings.Repeat("r", 201)+`"}`)
	mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", "GitHub is not configured")
}

func TestConnectRequestCreate(t *testing.T) {
	f := newCRFixture(t)
	path := crCreatePath(f.project)

	// Who: owner and admin, console or delegated; a viewer is forbidden and a stranger not_found.
	status, env, _ := mustCall(t, f.w, f.viewer, http.MethodPost, path, `{}`)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	status, env, _ = mustCall(t, f.w, f.extra, http.MethodPost, path, `{}`)
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	id := f.create(t, f.del, f.project, crReason)
	f.create(t, f.admin, f.project, "")
	// An empty body is no reason.
	status, env, _ = mustCall(t, f.w, f.admD, http.MethodPost, path, "")
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")

	got := f.read(t, f.owner, id)
	if got["request_id"] != id || got["project_id"] != f.project || got["registry"] != "ghcr.io" || got["state"] != "pending" ||
		got["expires_at"] != f.w.now.Add(15*time.Minute).Format(time.RFC3339) || got["created_at"] != f.w.now.Format(time.RFC3339) || got["last_failure"] != nil || len(got) != 7 {
		t.Fatalf("read %v", got)
	}

	// Reason rules (v1.28 item 2 check 3).
	for _, bad := range []string{strings.Repeat("r", 201), "a\nb", "bidi‮override", "line sep", "para sep", "zero​width"} {
		raw, _ := json.Marshal(map[string]string{"reason": bad})
		status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, path, string(raw))
		mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "reason must be at most 200 characters of plain text")
	}
	f.create(t, f.owner, f.project, strings.Repeat("é", 200))
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, path, `{"reason":"x","extra":1}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid json")

	// Audit: the request ref and via, never the id or the reason.
	n := 0
	for _, row := range f.w.mem.AllAudits() {
		if strings.Contains(string(row.Detail), id) || strings.Contains(string(row.Detail), crReason) {
			t.Fatalf("audit %s holds the id or reason", row.Action)
		}
		if row.Action != audit.ActionRegistryGitHubConnectRequested {
			continue
		}
		n++
		var d map[string]any
		_ = json.Unmarshal(row.Detail, &d)
		if n == 1 && (d["request"] != auditRequestRef(secret.Hash(id)) || d["via"] != "mcp" || len(d) != 2) {
			t.Fatalf("detail %v", d)
		}
	}
	if n != 4 {
		t.Fatalf("requested rows %d", n)
	}
	if strings.Contains(f.log.String(), id) || strings.Contains(f.log.String(), crReason) {
		t.Fatal("log holds the id or reason")
	}
}

func TestConnectRequestReadAccess(t *testing.T) {
	f := newCRFixture(t)
	id := f.create(t, f.owner, f.project, crReason)
	// Another owner or admin of the same project, a stranger, a malformed
	// or unknown id: not_found.
	for _, tc := range []struct{ tok, id string }{
		{f.admin, id}, {f.admD, id}, {f.extra, id}, {f.owner, "short"}, {f.owner, strings.Repeat("B", 43)},
	} {
		status, env, _ := mustCall(t, f.w, tc.tok, http.MethodGet, crPath(tc.id), "")
		mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	}
	if got := f.read(t, f.del, id); got["state"] != "pending" {
		t.Fatalf("delegated read %v", got)
	}
	// The read never carries the reason.
	if _, ok := f.read(t, f.owner, id)["reason"]; ok {
		t.Fatal("reason on the read")
	}
	// A creator who no longer reaches the project gets not_found.
	adminID := f.create(t, f.admin, f.project, "")
	if err := f.w.mem.RemoveMember(context.Background(), f.sc.project.ID, f.admID); err != nil {
		t.Fatal(err)
	}
	status, env, _ := mustCall(t, f.w, f.admin, http.MethodGet, crPath(adminID), "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	// A deleted project's requests are gone.
	if err := f.w.mem.DeleteProject(context.Background(), f.sc.project.ID, f.w.now); err != nil {
		t.Fatal(err)
	}
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodGet, crPath(id), "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
}

func TestConnectRequestPage(t *testing.T) {
	f := newCRFixture(t)
	id := f.create(t, f.owner, f.project, crReason)
	page := func(tok string) map[string]any {
		t.Helper()
		status, env, _ := mustCall(t, f.w, tok, http.MethodGet, crPath(id)+"/page", "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		return objectPayload(t, env)
	}
	got := page(f.owner)
	if got["project_name"] != "Proj" || got["reason"] != crReason || got["state"] != "pending" || got["registry"] != "ghcr.io" ||
		got["current_login"] != nil || got["last_failure"] != nil || len(got) != 10 {
		t.Fatalf("page %v", got)
	}
	if _, ok := got["current_login"]; !ok {
		t.Fatal("current_login missing")
	}
	// Delegated sessions are refused; another admin and a viewer-demoted creator get not_found.
	status, env, _ := mustCall(t, f.w, f.del, http.MethodGet, crPath(id)+"/page", "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	status, env, _ = mustCall(t, f.w, f.admin, http.MethodGet, crPath(id)+"/page", "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")

	putRegistry(t, f.w, f.owner, f.project, "ghcr.io", "bot", "manual-token-value")
	if l, _ := page(f.owner)["current_login"].(map[string]any); l["source"] != "manual" || l["username"] != "bot" || len(l) != 2 {
		t.Fatalf("manual login %v", l)
	}
	state := startGitHub(t, f.w, f.owner, f.project)
	f.callback(t, f.owner, state)
	if l, _ := page(f.owner)["current_login"].(map[string]any); l["source"] != "github" || l["username"] != "octo-cat" {
		t.Fatalf("github login %v", l)
	}
	adminID := f.create(t, f.admin, f.project, "")
	if _, err := f.w.mem.UpdateMemberRole(context.Background(), f.sc.project.ID, f.admID, "viewer"); err != nil {
		t.Fatal(err)
	}
	status, env, _ = mustCall(t, f.w, f.admin, http.MethodGet, crPath(adminID)+"/page", "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
}

func TestConnectRequestContinueAndConnect(t *testing.T) {
	f := newCRFixture(t)
	id := f.create(t, f.del, f.project, crReason)
	// Another of the owner's requests, in another project, and the admin's
	// request in this one: neither is touched by the connect.
	other, err := f.w.mem.CreateProject(context.Background(), f.w.mem.PersonalOrg(f.ownID), f.ownID, "Other", "other", f.w.now)
	if err != nil {
		t.Fatal(err)
	}
	otherID := f.create(t, f.owner, other.ID.String(), "")
	adminID := f.create(t, f.admin, f.project, "")

	state := f.start(t, f.owner, f.project, id)
	// Still pending until the callback.
	if s := f.state(t, id); s != "pending" {
		t.Fatalf("after start %s", s)
	}
	out, raw := f.callback(t, f.owner, state)
	if out["connect_request_id"] != id || out["source"] != "github" || out["username"] != "octo-cat" || len(out) != 5 {
		t.Fatalf("callback %v", out)
	}
	if strings.Contains(raw, fakeGitHubToken) {
		t.Fatal("token in the callback response")
	}
	got := f.read(t, f.owner, id)
	if got["state"] != "connected" {
		t.Fatalf("not connected %v", got)
	}
	if f.state(t, otherID) != "pending" || f.read(t, f.admin, adminID)["state"] != "pending" {
		t.Fatal("another request was marked")
	}
	items := listRegistries(t, f.w, f.owner, f.project)
	if len(items) != 1 || items[0]["source"] != "github" {
		t.Fatalf("registries %v", items)
	}
	// Single use: a second continue is refused with the v1.28 text.
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, githubStartPath(f.project), `{"connect_request_id":"`+id+`"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request was already used")
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, crPath(id)+"/decline", "")
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request was already used")

	// The connected audit row names the request by its ref only.
	found := false
	for _, row := range f.w.mem.AllAudits() {
		if strings.Contains(string(row.Detail), fakeGitHubToken) || strings.Contains(string(row.Detail), id) {
			t.Fatalf("audit %s holds the token or id", row.Action)
		}
		if row.Action == audit.ActionRegistryGitHubConnected {
			var d map[string]any
			_ = json.Unmarshal(row.Detail, &d)
			if d["request"] == auditRequestRef(secret.Hash(id)) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("no connected row with the request ref")
	}
	if strings.Contains(f.log.String(), fakeGitHubToken) || strings.Contains(f.log.String(), id) {
		t.Fatal("log holds the token or id")
	}
}

func TestConnectRequestStartChecks(t *testing.T) {
	f := newCRFixture(t)
	path := githubStartPath(f.project)
	body := func(id string) string { return `{"connect_request_id":"` + id + `"}` }
	adminID := f.create(t, f.admin, f.project, "")
	other, err := f.w.mem.CreateProject(context.Background(), f.w.mem.PersonalOrg(f.ownID), f.ownID, "Other", "other", f.w.now)
	if err != nil {
		t.Fatal(err)
	}
	otherID := f.create(t, f.owner, other.ID.String(), "")

	for _, tc := range []struct{ name, body string }{
		{"another user's request", body(adminID)},
		{"another project's request", body(otherID)},
		{"malformed id", body("short")},
		{"unknown id", body(strings.Repeat("C", 43))},
	} {
		status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, path, tc.body)
		if status != http.StatusNotFound || env.Code != "not_found" || env.Msg != "not found" {
			t.Fatalf("%s: %d %v", tc.name, status, env)
		}
	}
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, path, `{"connect_request_id":`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid json")
	// Delegated sessions are still refused before anything is read.
	status, env, _ = mustCall(t, f.w, f.del, http.MethodPost, path, body(otherID))
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	// Without an id (no body, {} or null) the v1.29 start is unchanged and the callback has no request id.
	for _, b := range []string{"", "{}", `{"connect_request_id":null}`} {
		status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, path, b)
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		if p := objectPayload(t, env); len(p) != 1 {
			t.Fatalf("start payload %v", p)
		}
	}
	out, _ := f.callback(t, f.owner, startGitHub(t, f.w, f.owner, f.project))
	if _, ok := out["connect_request_id"]; ok || len(out) != 4 {
		t.Fatalf("v1.29 callback %v", out)
	}

	// Declined → used; expired → expired.
	declined := f.create(t, f.owner, f.project, "")
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, crPath(declined)+"/decline", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, path, body(declined))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request was already used")
	expiring := f.create(t, f.owner, f.project, "")
	f.setNow(f.w.now.Add(store.RequestTTL))
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, path, body(expiring))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request has expired; ask the agent for a new one")
	if f.state(t, expiring) != "expired" {
		t.Fatal("not expired")
	}
}

func TestConnectRequestCallbackNotPending(t *testing.T) {
	f := newCRFixture(t)

	// Declined between Continue and the callback: the connect applies, the
	// request stays declined, and the console still returns to its page.
	id := f.create(t, f.owner, f.project, "")
	state := f.start(t, f.owner, f.project, id)
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, crPath(id)+"/decline", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	out, _ := f.callback(t, f.owner, state)
	if out["connect_request_id"] != id || f.state(t, id) != "declined" {
		t.Fatalf("declined %v %s", out, f.state(t, id))
	}
	if items := listRegistries(t, f.w, f.owner, f.project); len(items) != 1 || items[0]["source"] != "github" {
		t.Fatalf("not connected %v", items)
	}

	// Expired between Continue and the callback (the OAuth state lives 10
	// minutes, the request 15): the connect applies, the request stays expired.
	start := f.w.now.Add(time.Hour)
	f.setNow(start)
	id = f.create(t, f.owner, f.project, "")
	f.setNow(start.Add(store.RequestTTL - time.Minute))
	state = f.start(t, f.owner, f.project, id)
	f.setNow(start.Add(store.RequestTTL + time.Minute))
	if github.StateTTL <= 2*time.Minute {
		t.Fatal("state TTL too short for this case")
	}
	out, _ = f.callback(t, f.owner, state)
	if out["connect_request_id"] != id || f.state(t, id) != "expired" {
		t.Fatalf("expired %v %s", out, f.state(t, id))
	}

	// Replaced between Continue and the callback: the old one is expired and stays so.
	f.setNow(start.Add(2 * time.Hour))
	old := f.create(t, f.owner, f.project, "")
	state = f.start(t, f.owner, f.project, old)
	fresh := f.create(t, f.owner, f.project, "")
	f.callback(t, f.owner, state)
	if f.state(t, old) != "expired" || f.state(t, fresh) != "pending" {
		t.Fatalf("replace %s %s", f.state(t, old), f.state(t, fresh))
	}
}

func TestConnectRequestReplaceAndCap(t *testing.T) {
	f := newCRFixture(t)
	first := f.create(t, f.owner, f.project, "")
	second := f.create(t, f.owner, f.project, "")
	if f.state(t, first) != "expired" || f.state(t, second) != "pending" {
		t.Fatal("not replaced")
	}
	status, env, _ := mustCall(t, f.w, f.owner, http.MethodPost, githubStartPath(f.project), `{"connect_request_id":"`+first+`"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request has expired; ask the agent for a new one")
	// Another user's request in the same project is not replaced.
	adminID := f.create(t, f.admin, f.project, "")
	f.create(t, f.owner, f.project, "")
	if f.read(t, f.admin, adminID)["state"] != "pending" {
		t.Fatal("another user's request was replaced")
	}

	// The cap: 20 open (one per project), the 21st refused, a replace at the cap allowed.
	projects := []string{f.project}
	for i := 1; i < store.MaxOpenRequests; i++ {
		p, err := f.w.mem.CreateProject(context.Background(), f.w.mem.PersonalOrg(f.ownID), f.ownID, fmt.Sprintf("P%d", i), fmt.Sprintf("p%d", i), f.w.now)
		if err != nil {
			t.Fatal(err)
		}
		projects = append(projects, p.ID.String())
		f.create(t, f.owner, p.ID.String(), "")
	}
	extra, err := f.w.mem.CreateProject(context.Background(), f.w.mem.PersonalOrg(f.ownID), f.ownID, "Over", "over", f.w.now)
	if err != nil {
		t.Fatal(err)
	}
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, crCreatePath(extra.ID.String()), `{}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "too many open connect requests; finish or wait for the ones you have")
	f.create(t, f.owner, projects[3], "")
	// After expiry the slots free up.
	f.setNow(f.w.now.Add(store.RequestTTL))
	f.create(t, f.owner, extra.ID.String(), "")
}

func TestConnectRequestDecline(t *testing.T) {
	f := newCRFixture(t)
	id := f.create(t, f.owner, f.project, crReason)
	status, env, _ := mustCall(t, f.w, f.admin, http.MethodPost, crPath(id)+"/decline", "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, crPath(id)+"/decline", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if p := objectPayload(t, env); p["state"] != "declined" || len(p) != 1 {
		t.Fatalf("decline %v", p)
	}
	if f.state(t, id) != "declined" {
		t.Fatal("not declined")
	}
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, crPath(id)+"/decline", "")
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request was already used")
	expiring := f.create(t, f.owner, f.project, "")
	f.setNow(f.w.now.Add(store.RequestTTL))
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodPost, crPath(expiring)+"/decline", "")
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request has expired; ask the agent for a new one")
	n := 0
	for _, row := range f.w.mem.AllAudits() {
		if row.Action != audit.ActionRegistryGitHubConnectDeclined {
			continue
		}
		n++
		var d map[string]any
		_ = json.Unmarshal(row.Detail, &d)
		if d["request"] != auditRequestRef(secret.Hash(id)) || len(d) != 1 {
			t.Fatalf("declined detail %v", d)
		}
	}
	if n != 1 {
		t.Fatalf("declined rows %d", n)
	}
	// Nothing was written to the registries.
	if items := listRegistries(t, f.w, f.owner, f.project); len(items) != 0 {
		t.Fatalf("registries %v", items)
	}
}

// failingConnect is a store whose connect transaction fails: nothing is
// written and the new token is revoked, as for a failed v1.29 write.
type failingConnect struct{ store.Store }

func (failingConnect) ConnectRegistry(context.Context, uuid.UUID, string, store.RegistryWrite, uuid.UUID, time.Time, []byte) (store.Registry, error) {
	return store.Registry{}, errors.New("write failed")
}

func TestConnectRequestCallbackStoreFailure(t *testing.T) {
	f := newCRFixture(t)
	id := f.create(t, f.owner, f.project, "")
	state := f.start(t, f.owner, f.project, id)
	real := f.w.srv.Store
	f.w.srv.Store = failingConnect{real}
	status, env, raw := mustCall(t, f.w, f.owner, http.MethodPost, "/v1/auth/github/callback", callbackBody("good-code", state))
	f.w.srv.Store = real
	if status != http.StatusInternalServerError || strings.Contains(raw, fakeGitHubToken) {
		t.Fatalf("store failure %d %v", status, env)
	}
	if got := f.gh.revokedTokens(); len(got) != 1 || !strings.HasPrefix(got[0], fakeGitHubToken) {
		t.Fatalf("new token not revoked: %v", got)
	}
	if f.state(t, id) != "pending" || len(listRegistries(t, f.w, f.owner, f.project)) != 0 {
		t.Fatal("a failed connect changed something")
	}
}
