package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// vrFixture is a project with set svc: HAS (secret, has a value), PLAIN
// (plain, has a value) and PH (a plain needs_value placeholder). The
// server log goes to logs so tests can show no value is logged.
type vrFixture struct {
	w       *world
	sc      scene
	project string
	owner   store.User
	admin   store.User
	viewer  store.User
	tok     string // owner, console
	mcp     string // owner, api_delegated
	adminT  string
	viewerT string
	logs    *bytes.Buffer
}

const vrSecret = "s3cr3t-value-for-fill-0123456789"

func newVRFixture(t *testing.T) vrFixture {
	t.Helper()
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	w.srv.ConsoleOrigin = "http://console.test/"
	logs := &bytes.Buffer{}
	w.srv.Log = slog.New(slog.NewTextHandler(logs, nil))
	owner := w.user("owner@ex.com", "Owner")
	admin := w.user("admin@ex.com", "Admin")
	viewer := w.user("viewer@ex.com", "Viewer")
	sc := w.scene(owner, admin, viewer, w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	putEnvFlag(t, w, tok, project, "svc", "HAS=already-there-1234\n", true)
	putEnvFlag(t, w, tok, project, "svc", "HAS=already-there-1234\nPLAIN=p\nPH=x\n", false)
	if !w.mem.SetNeedsValue(sc.project.ID, "svc", "PH") {
		t.Fatal("no PH")
	}
	return vrFixture{
		w: w, sc: sc, project: project, owner: owner, admin: admin, viewer: viewer,
		tok: tok, mcp: tokenKind(t, w, owner.ID, store.KindDelegated),
		adminT: w.token(admin.ID), viewerT: w.token(viewer.ID), logs: logs,
	}
}

func vrPath(project, set string) string { return envPath(project, set) + "/value-requests" }

// create opens a request and returns its id after checking the 201 body.
func (f vrFixture) create(t *testing.T, tok, key, reason string) string {
	t.Helper()
	body := map[string]any{"key": key}
	if reason != "" {
		body["reason"] = reason
	}
	raw, _ := json.Marshal(body)
	status, env, _ := mustCall(t, f.w, tok, http.MethodPost, vrPath(f.project, "svc"), string(raw))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	out := objectPayload(t, env)
	id, _ := out["request_id"].(string)
	if len(id) != 43 || !requestIDPattern.MatchString(id) {
		t.Fatalf("request_id %q", id)
	}
	if out["url"] != "http://console.test/fill/"+id || len(out) != 3 {
		t.Fatalf("create body %v", out)
	}
	return id
}

func (f vrFixture) call(t *testing.T, tok, method, path, body string) (int, envelope) {
	t.Helper()
	status, env, _ := mustCall(t, f.w, tok, method, path, body)
	return status, env
}

func reqPath(id string) string { return "/v1/value-requests/" + id }

func (f vrFixture) state(t *testing.T, id string) string {
	t.Helper()
	status, env := f.call(t, f.mcp, http.MethodGet, reqPath(id), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	s, _ := objectPayload(t, env)["state"].(string)
	return s
}

// assertNoValue fails when the plaintext is in any audit row or log line.
func (f vrFixture) assertNoValue(t *testing.T, plain string) {
	t.Helper()
	for _, row := range f.w.mem.AllAudits() {
		if strings.Contains(string(row.Detail), plain) {
			t.Fatalf("audit %s holds the value", row.Action)
		}
	}
	if strings.Contains(f.logs.String(), plain) {
		t.Fatal("log holds the value")
	}
}

func TestValueRequestCreate(t *testing.T) {
	f := newVRFixture(t)
	before := envSnap(t, f.w, f.sc.project.ID, "svc")

	// A delegated session may create; the url carries only the id.
	id := f.create(t, f.mcp, "PH", "Stripe key for checkout")
	status, env := f.call(t, f.mcp, http.MethodGet, reqPath(id), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	got := objectPayload(t, env)
	if got["state"] != "pending" || got["key"] != "PH" || got["set"] != "svc" || got["project_id"] != f.project || got["request_id"] != id {
		t.Fatalf("read %v", got)
	}
	if got["expires_at"] != f.w.now.Add(15*time.Minute).Format(time.RFC3339) || len(got) != 7 {
		t.Fatalf("read %v", got)
	}
	// A new key is not created by the request.
	f.create(t, f.tok, "NEW_KEY", "")
	if after := envSnap(t, f.w, f.sc.project.ID, "svc"); after != before {
		t.Fatalf("set changed\n%s\n%s", before, after)
	}
	rows := f.w.mem.AllAudits()
	n := 0
	for _, row := range rows {
		if row.Action != audit.ActionEnvSetValueRequested {
			continue
		}
		n++
		var d map[string]any
		_ = json.Unmarshal(row.Detail, &d)
		if d["set"] != "svc" || len(d) != 3 || len(fmt.Sprint(d["request"])) != 8 || strings.Contains(string(row.Detail), "Stripe") {
			t.Fatalf("detail %s", row.Detail)
		}
		if n == 1 && row.Via != audit.ViaMCP {
			t.Fatalf("via %s", row.Via)
		}
	}
	if n != 2 {
		t.Fatalf("value_requested rows %d", n)
	}

	cases := []struct {
		name, set, body string
		status          int
		code, msg       string
	}{
		{"set first", "nope", `{"key":"bad key"}`, 404, "not_found", "no env set nope"},
		{"key rule", "svc", `{"key":"bad key"}`, 400, "bad_request", ""},
		{"reason long", "svc", `{"key":"PH","reason":"` + strings.Repeat("a", 201) + `"}`, 400, "bad_request", msgReason},
		{"reason control", "svc", `{"key":"PH","reason":"line\nbreak"}`, 400, "bad_request", msgReason},
		{"reason bidi", "svc", `{"key":"PH","reason":"pay \u202eevil"}`, 400, "bad_request", msgReason},
		{"reason zero width", "svc", `{"key":"PH","reason":"a\u200bb"}`, 400, "bad_request", msgReason},
		{"reason line separator", "svc", `{"key":"PH","reason":"a\u2028b"}`, 400, "bad_request", msgReason},
		{"has value", "svc", `{"key":"HAS"}`, 409, "conflict", "HAS already has a value; edit it in the console"},
		{"plain has value", "svc", `{"key":"PLAIN"}`, 409, "conflict", "PLAIN already has a value; edit it in the console"},
		{"unknown field", "svc", `{"key":"PH","value":"x"}`, 400, "bad_request", "invalid json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, env := f.call(t, f.mcp, http.MethodPost, vrPath(f.project, tc.set), tc.body)
			if status != tc.status || env.Code != tc.code || (tc.msg != "" && env.Msg != tc.msg) {
				t.Fatalf("%d %s %q", status, env.Code, env.Msg)
			}
		})
	}
	// 200 characters, multibyte, is fine.
	f.create(t, f.tok, "PH", strings.Repeat("é", 200))

	// Viewers cannot ask; strangers do not see the project.
	status, env = f.call(t, f.viewerT, http.MethodPost, vrPath(f.project, "svc"), `{"key":"PH"}`)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	stranger := f.w.token(f.w.user("x@ex.com", "X").ID)
	status, env = f.call(t, stranger, http.MethodPost, vrPath(f.project, "svc"), `{"key":"PH"}`)
	if status != http.StatusNotFound {
		t.Fatalf("stranger %d", status)
	}
}

func TestValueRequestOpenLimit(t *testing.T) {
	f := newVRFixture(t)
	keys := make([]string, store.MaxOpenRequests)
	for i := range keys {
		keys[i] = fmt.Sprintf("KEY_%d", i)
		f.create(t, f.mcp, keys[i], "")
	}
	status, env := f.call(t, f.mcp, http.MethodPost, vrPath(f.project, "svc"), `{"key":"ONE_MORE"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "too many open value requests; fill or wait for the ones you have")
	// At the limit, asking again for an open key replaces that request and is not counted (rev 3).
	f.create(t, f.mcp, keys[0], "")
	// Another user has their own count.
	f.create(t, f.adminT, "ONE_MORE", "")
	// Expired requests no longer count.
	setClock(f.w, 15*time.Minute)
	f.create(t, f.mcp, "ONE_MORE", "")
}

// TestValueRequestReplaces: one open request per user and key (rev 3). The
// older link answers expired; the new one can be filled. Another user's
// request for the same key is not touched.
func TestValueRequestReplaces(t *testing.T) {
	f := newVRFixture(t)
	old := f.create(t, f.mcp, "PH", "")
	theirs := f.create(t, f.adminT, "PH", "")
	newer := f.create(t, f.mcp, "PH", "")
	if f.state(t, old) != "expired" || f.state(t, newer) != "pending" {
		t.Fatal("old request not replaced")
	}
	status, env := f.call(t, f.adminT, http.MethodGet, reqPath(theirs), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if objectPayload(t, env)["state"] != "pending" {
		t.Fatal("another user's request was replaced")
	}
	status, env = f.call(t, f.tok, http.MethodPost, reqPath(old)+"/fill", `{"value":"`+vrSecret+`"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request has expired; ask the agent for a new one")
	status, env = f.call(t, f.tok, http.MethodPost, reqPath(newer)+"/fill", `{"value":"`+vrSecret+`"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	// A refused create (limit reached) does not expire the open one.
	f2 := newVRFixture(t)
	keep := f2.create(t, f2.mcp, "PH", "")
	for i := 0; i < store.MaxOpenRequests-1; i++ {
		f2.create(t, f2.mcp, fmt.Sprintf("KEY_%d", i), "")
	}
	status, env = f2.call(t, f2.mcp, http.MethodPost, vrPath(f2.project, "svc"), `{"key":"OTHER"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "too many open value requests; fill or wait for the ones you have")
	if f2.state(t, keep) != "pending" {
		t.Fatal("a refused create changed an open request")
	}
}

// TestValueRequestAuditHoldsNoID: audit rows name a request by a hash
// prefix only (rev 3); no row holds any 6-character run of an id.
func TestValueRequestAuditHoldsNoID(t *testing.T) {
	f := newVRFixture(t)
	filled := f.create(t, f.mcp, "PH", "")
	declined := f.create(t, f.mcp, "OTHER", "")
	generated := f.create(t, f.mcp, "GEN_KEY", "")
	for _, c := range []struct{ id, path, body string }{
		{filled, "/fill", `{"value":"` + vrSecret + `"}`},
		{declined, "/decline", ""},
		{generated, "/fill", `{"generate":{"kind":"hex"}}`},
	} {
		status, env := f.call(t, f.tok, http.MethodPost, reqPath(c.id)+c.path, c.body)
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	}
	rows := f.w.mem.AllAudits()
	refs := 0
	for _, row := range rows {
		detail := string(row.Detail)
		for _, id := range []string{filled, declined, generated} {
			for i := 0; i+6 <= len(id); i++ {
				if strings.Contains(detail, id[i:i+6]) {
					t.Fatalf("%s holds part of an id: %s", row.Action, detail)
				}
			}
			if strings.Contains(detail, `"request":"`+auditRequestRef(secret.Hash(id))+`"`) {
				refs++
			}
		}
	}
	// value_requested x3, key_put x2 and value_request_declined x1 carry the reference.
	if refs != 6 {
		t.Fatalf("request refs %d", refs)
	}
	if strings.Contains(f.logs.String(), filled[:6]) {
		t.Fatal("log holds part of an id")
	}
}

func TestValueRequestConsoleOrigin(t *testing.T) {
	f := newVRFixture(t)
	f.w.srv.ConsoleOrigin = ""
	status, env := f.call(t, f.mcp, http.MethodPost, vrPath(f.project, "svc"), `{"key":"PH"}`)
	mustHTTP(t, status, env, http.StatusInternalServerError, "error", "internal error")
}

// TestValueRequestNoSecretsKey: without BOT_SECRETS_KEY_FILE no link is handed out.
func TestValueRequestNoSecretsKey(t *testing.T) {
	f := newVRFixture(t)
	f.w.srv.Secrets = nil
	status, env := f.call(t, f.mcp, http.MethodPost, vrPath(f.project, "svc"), `{"key":"NEW_KEY"}`)
	mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", errSecretsKey.Error())
}

func TestValueRequestReadAccess(t *testing.T) {
	f := newVRFixture(t)
	id := f.create(t, f.mcp, "PH", "")
	for name, path := range map[string]string{
		"unknown":   reqPath(strings.Repeat("A", 43)),
		"malformed": reqPath("short"),
	} {
		status, env := f.call(t, f.mcp, http.MethodGet, path, "")
		mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
		_ = name
	}
	// Another owner or admin of the same project gets not_found.
	status, env := f.call(t, f.adminT, http.MethodGet, reqPath(id), "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, env = f.call(t, f.adminT, http.MethodGet, reqPath(id)+"/fill", "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")

	// The fill page data is console only, refused before any lookup.
	before := countAction(f.w.mem.AllAudits(), audit.ActionDelegatedDenied)
	status, env = f.call(t, f.mcp, http.MethodGet, reqPath(id)+"/fill", "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	if countAction(f.w.mem.AllAudits(), audit.ActionDelegatedDenied) != before+1 {
		t.Fatal("no delegated.denied row")
	}
}

func TestValueRequestFillPage(t *testing.T) {
	f := newVRFixture(t)
	reason := `<a href="https://evil.test">click</a>`
	id := f.create(t, f.mcp, "PH", reason)
	req, err := http.NewRequest(http.MethodGet, f.w.http.URL+reqPath(id)+"/fill", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	raw, _ := io.ReadAll(resp.Body)
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	page := objectPayload(t, env)
	if page["project_name"] != "Proj" || page["reason"] != reason || page["state"] != "pending" || page["key"] != "PH" || len(page) != 9 {
		t.Fatalf("page %v", page)
	}
}

func TestValueRequestFillTyped(t *testing.T) {
	f := newVRFixture(t)
	id := f.create(t, f.mcp, "PH", "")
	fill := reqPath(id) + "/fill"
	before := envSnap(t, f.w, f.sc.project.ID, "svc")

	// Another user, even an owner-level admin of the project: not_found, nothing changes.
	status, env := f.call(t, f.adminT, http.MethodPost, fill, `{"value":"`+vrSecret+`"}`)
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	for _, body := range []string{`{}`, `{"value":"a","generate":{"kind":"hex"}}`} {
		status, env = f.call(t, f.tok, http.MethodPost, fill, body)
		mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "send value or generate, not both")
	}
	status, env = f.call(t, f.tok, http.MethodPost, fill, `{"value":""}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "value must not be empty")
	status, env = f.call(t, f.tok, http.MethodPost, fill, `{"value":"a\nb"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("line break %d", status)
	}
	if after := envSnap(t, f.w, f.sc.project.ID, "svc"); after != before {
		t.Fatalf("set changed\n%s\n%s", before, after)
	}

	status, env = f.call(t, f.tok, http.MethodPost, fill, `{"value":"`+vrSecret+`"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if out := objectPayload(t, env); out["state"] != "filled" || len(out) != 1 {
		t.Fatalf("fill %v", out)
	}
	// The placeholder is now a secret with a value and no flags.
	_, env, _ = mustCall(t, f.w, f.tok, http.MethodGet, envPath(f.project, "svc"), "")
	ph := keysByName(t, env)["PH"]
	if ph["is_secret"] != true || ph["value"] != nil || ph["needs_value"] != false || ph["reveal_pending"] != false || ph["preview"] != "s3cr…" {
		t.Fatalf("PH %v", ph)
	}
	mats, err := f.w.mem.ListEnvMaterial(context.Background(), f.sc.project.ID, "svc")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mats {
		if m.Key == "PH" {
			plain, err := f.w.srv.openEnv(f.sc.project.ID, "svc", m)
			if err != nil || plain != vrSecret {
				t.Fatalf("sealed value does not open: %v", err)
			}
		}
	}
	if f.state(t, id) != "filled" {
		t.Fatal("state")
	}
	var put *store.AuditEntry
	for _, row := range f.w.mem.AllAudits() {
		if row.Action == audit.ActionEnvSetKeyPut {
			cp := row
			put = &cp
		}
	}
	var d map[string]any
	if put == nil || json.Unmarshal(put.Detail, &d) != nil || d["via"] != "value_request" || d["request"] != auditRequestRef(secret.Hash(id)) || d["key"] != "PH" || d["generated"] != nil || put.Via != audit.ViaConsole {
		t.Fatalf("key_put %#v %v", put, d)
	}
	// Single use.
	for _, path := range []string{fill, reqPath(id) + "/decline"} {
		status, env = f.call(t, f.tok, http.MethodPost, path, `{"value":"again-and-again"}`)
		if path != fill {
			status, env = f.call(t, f.tok, http.MethodPost, path, "")
		}
		mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request was already used")
	}
	f.assertNoValue(t, vrSecret)
}

func TestValueRequestFillNewKey(t *testing.T) {
	f := newVRFixture(t)
	id := f.create(t, f.mcp, "BRAND_NEW", "")
	status, env := f.call(t, f.tok, http.MethodPost, reqPath(id)+"/fill", `{"value":"`+vrSecret+`"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	_, env, _ = mustCall(t, f.w, f.tok, http.MethodGet, envPath(f.project, "svc"), "")
	if k := keysByName(t, env)["BRAND_NEW"]; k == nil || k["is_secret"] != true {
		t.Fatalf("BRAND_NEW %v", k)
	}
}

func TestValueRequestFillGenerate(t *testing.T) {
	f := newVRFixture(t)
	id := f.create(t, f.mcp, "PH", "")
	status, env := f.call(t, f.tok, http.MethodPost, reqPath(id)+"/fill", `{"generate":{"kind":"hex"}}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if out := objectPayload(t, env); out["state"] != "filled" || out["reveal_pending"] != true || len(out) != 2 {
		t.Fatalf("fill %v", out)
	}
	value := reveal(t, f.w, f.tok, f.project, "svc", "PH")
	if len(value) != 64 {
		t.Fatalf("hex length %d", len(value))
	}
	found := false
	for _, row := range f.w.mem.AllAudits() {
		var d map[string]any
		if row.Action == audit.ActionEnvSetKeyPut && json.Unmarshal(row.Detail, &d) == nil && d["generated"] == "hex" && d["via"] == "value_request" {
			found = true
		}
	}
	if !found {
		t.Fatal("no generated key_put row")
	}
	f.assertNoValue(t, value)
	status, env = f.call(t, f.tok, http.MethodPost, reqPath(f.create(t, f.mcp, "OTHER", ""))+"/fill", `{"generate":{"kind":"nope"}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("bad kind %d %s", status, env.Msg)
	}
}

func TestValueRequestExpiry(t *testing.T) {
	f := newVRFixture(t)
	id := f.create(t, f.mcp, "PH", "")
	setClock(f.w, 15*time.Minute)
	if f.state(t, id) != "expired" {
		t.Fatal("state")
	}
	for _, path := range []string{"/fill", "/decline"} {
		status, env := f.call(t, f.tok, http.MethodPost, reqPath(id)+path, `{"value":"late-value-1234"}`)
		if path == "/decline" {
			status, env = f.call(t, f.tok, http.MethodPost, reqPath(id)+path, "")
		}
		mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request has expired; ask the agent for a new one")
	}
}

func TestValueRequestDecline(t *testing.T) {
	f := newVRFixture(t)
	id := f.create(t, f.mcp, "PH", "")
	status, env := f.call(t, f.adminT, http.MethodPost, reqPath(id)+"/decline", "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, env = f.call(t, f.tok, http.MethodPost, reqPath(id)+"/decline", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if out := objectPayload(t, env); out["state"] != "declined" {
		t.Fatalf("decline %v", out)
	}
	if f.state(t, id) != "declined" || countAction(f.w.mem.AllAudits(), audit.ActionEnvSetValueRequestDeclined) != 1 {
		t.Fatal("declined state or audit")
	}
	status, env = f.call(t, f.tok, http.MethodPost, reqPath(id)+"/fill", `{"value":"x-value-1234"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request was already used")
}

func TestValueRequestKeyFilledMeanwhile(t *testing.T) {
	f := newVRFixture(t)
	id := f.create(t, f.mcp, "LATER", "")
	status, env, _ := mustCall(t, f.w, f.tok, http.MethodPut, envKeyPath(f.project, "svc", "LATER"), `{"value":"typed-in-console"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env = f.call(t, f.tok, http.MethodPost, reqPath(id)+"/fill", `{"value":"`+vrSecret+`"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "LATER already has a value; edit it in the console")
	if f.state(t, id) != "pending" {
		t.Fatal("request left pending")
	}
}

func TestValueRequestRoleAndSetGone(t *testing.T) {
	f := newVRFixture(t)
	// The admin asks, then is demoted: their own request is not_found to fill.
	id := f.create(t, f.adminT, "PH", "")
	if _, err := f.w.mem.UpdateMemberRole(context.Background(), f.sc.project.ID, f.admin.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	status, env := f.call(t, f.adminT, http.MethodPost, reqPath(id)+"/fill", `{"value":"`+vrSecret+`"}`)
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")

	// Deleting the set removes its requests.
	other := f.create(t, f.mcp, "PH", "")
	status, env, _ = mustCall(t, f.w, f.tok, http.MethodDelete, envPath(f.project, "svc"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env = f.call(t, f.mcp, http.MethodGet, reqPath(other), "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	// A set of the same name made later does not bring it back.
	putEnvFlag(t, f.w, f.tok, f.project, "svc", "PH=x\n", false)
	status, env = f.call(t, f.mcp, http.MethodGet, reqPath(other), "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
}

// TestValueRequestDelegatedEnvStillRefused: v1.28 item 9. A delegated session
// reads key names and flags only (v1.27 item 5, phase B), never a value or a
// preview, before or after a fill, and key writes stay refused.
func TestValueRequestDelegatedEnvStillRefused(t *testing.T) {
	f := newVRFixture(t)
	id := f.create(t, f.mcp, "PH", "")
	status, env := f.call(t, f.tok, http.MethodPost, reqPath(id)+"/fill", `{"value":"`+vrSecret+`"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env = f.call(t, f.mcp, http.MethodGet, envPath(f.project, "svc"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	raw, _ := json.Marshal(env.Payload)
	for _, leak := range []string{vrSecret, "already-there", `"value"`, `"preview"`} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("delegated read holds %q: %s", leak, raw)
		}
	}
	ph := keysByName(t, env)["PH"]
	if ph == nil || ph["is_secret"] != true || ph["needs_value"] != false {
		t.Fatalf("PH %v", ph)
	}
	status, env = f.call(t, f.mcp, http.MethodPut, envKeyPath(f.project, "svc", "PH"), `{"value":"x"}`)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
}

// TestValueRequestReviewCases covers the review list of 2026-11-15.
func TestValueRequestReviewCases(t *testing.T) {
	ctx := context.Background()
	t.Run("viewer and demoted creator", func(t *testing.T) {
		f := newVRFixture(t)
		id := f.create(t, f.adminT, "PH", "")
		// A viewer of the project who did not ask: not_found everywhere.
		for _, c := range []struct{ method, path, body string }{
			{http.MethodGet, reqPath(id), ""},
			{http.MethodPost, reqPath(id) + "/decline", ""},
		} {
			status, env := f.call(t, f.viewerT, c.method, c.path, c.body)
			mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
		}
		// The creator demoted to viewer still reads the state but cannot decline.
		if _, err := f.w.mem.UpdateMemberRole(ctx, f.sc.project.ID, f.admin.ID, "viewer"); err != nil {
			t.Fatal(err)
		}
		status, env := f.call(t, f.adminT, http.MethodGet, reqPath(id), "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		status, env = f.call(t, f.adminT, http.MethodPost, reqPath(id)+"/decline", "")
		mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	})
	t.Run("soft-deleted project", func(t *testing.T) {
		f := newVRFixture(t)
		id := f.create(t, f.mcp, "PH", "")
		if err := f.w.mem.DeleteProject(ctx, f.sc.project.ID, f.w.now); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ tok, method, path, body string }{
			{f.mcp, http.MethodGet, reqPath(id), ""},
			{f.tok, http.MethodGet, reqPath(id) + "/fill", ""},
			{f.tok, http.MethodPost, reqPath(id) + "/fill", `{"value":"` + vrSecret + `"}`},
			{f.tok, http.MethodPost, reqPath(id) + "/decline", ""},
		} {
			status, env := f.call(t, c.tok, c.method, c.path, c.body)
			mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
		}
	})
	t.Run("fill without a secrets key", func(t *testing.T) {
		f := newVRFixture(t)
		id := f.create(t, f.mcp, "PH", "")
		f.w.srv.Secrets = nil
		status, env := f.call(t, f.tok, http.MethodPost, reqPath(id)+"/fill", `{"value":"`+vrSecret+`"}`)
		mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", errSecretsKey.Error())
		if f.state(t, id) != "pending" {
			t.Fatal("request used without a write")
		}
	})
	t.Run("unknown and malformed ids", func(t *testing.T) {
		f := newVRFixture(t)
		for _, id := range []string{strings.Repeat("B", 43), "short", strings.Repeat("A", 42) + "."} {
			for _, path := range []string{"/fill", "/decline"} {
				status, env := f.call(t, f.tok, http.MethodPost, reqPath(id)+path, `{"value":"x-value"}`)
				if path == "/decline" {
					status, env = f.call(t, f.tok, http.MethodPost, reqPath(id)+path, "")
				}
				mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
			}
		}
	})
	t.Run("value rules", func(t *testing.T) {
		f := newVRFixture(t)
		id := f.create(t, f.mcp, "PH", "")
		before := envSnap(t, f.w, f.sc.project.ID, "svc")
		for name, body := range map[string]string{
			"over length": `{"value":"` + strings.Repeat("a", 4097) + `"}`,
			"NUL":         `{"value":"a\u0000b"}`,
		} {
			status, env := f.call(t, f.tok, http.MethodPost, reqPath(id)+"/fill", body)
			if status != http.StatusBadRequest || env.Code != "bad_request" || strings.Contains(env.Msg, "aaaa") {
				t.Fatalf("%s: %d %s %q", name, status, env.Code, env.Msg)
			}
		}
		if envSnap(t, f.w, f.sc.project.ID, "svc") != before || f.state(t, id) != "pending" {
			t.Fatal("a refused value changed something")
		}
	})
	t.Run("key with a value is checked before the body", func(t *testing.T) {
		f := newVRFixture(t)
		id := f.create(t, f.mcp, "LATER", "")
		status, env, _ := mustCall(t, f.w, f.tok, http.MethodPut, envKeyPath(f.project, "svc", "LATER"), `{"value":"typed-in-console"}`)
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		for _, body := range []string{`{"value":"a","generate":{"kind":"hex"}}`, `{"value":""}`, `not json`} {
			status, env = f.call(t, f.tok, http.MethodPost, reqPath(id)+"/fill", body)
			mustHTTP(t, status, env, http.StatusConflict, "conflict", "LATER already has a value; edit it in the console")
		}
	})
}

// TestValueRequestUnblocksDeploy is spec-env-request-value AC 2: a deploy
// refused because a key needs a value proceeds once the person fills that
// key through a value request. Both a secret and a plain placeholder.
func TestValueRequestUnblocksDeploy(t *testing.T) {
	w, sc, tok, log := refsWorld(t, allowImages())
	w.srv.ConsoleOrigin = "http://console.test"
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	status, env, _ := mustCall(t, w, tok, http.MethodPost, envSetsPath(project), `{"name":"mail","keys":[{"key":"SMTP_PASSWORD"},{"key":"SMTP_HOST","is_secret":false}]}`)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","env_sets":["mail"]}`)
	before := log.count()
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "ac2-1", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "env SMTP_HOST in set mail has no value yet; set it in the console")

	agent := tokenKind(t, w, sc.owner, store.KindDelegated)
	fill := func(key, value string) {
		t.Helper()
		status, env, _ := mustCall(t, w, agent, http.MethodPost, envSetsPath(project)+"/mail/value-requests", `{"key":"`+key+`","reason":"deploy needs it"}`)
		mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
		id, _ := objectPayload(t, env)["request_id"].(string)
		status, env, _ = mustCall(t, w, tok, http.MethodPost, reqPath(id)+"/fill", `{"value":"`+value+`"}`)
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	}
	fill("SMTP_HOST", "smtp.example.test")
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "ac2-2", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "env SMTP_PASSWORD in set mail has no value yet; set it in the console")
	if log.count() != before {
		t.Fatal("a refused deploy sent a task")
	}
	fill("SMTP_PASSWORD", "pw-filled-through-a-request")
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "ac2-3", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	spec := lastSpec(t, log)
	envs, _ := spec["env"].(map[string]any)
	if envs["SMTP_HOST"] != "smtp.example.test" || envs["SMTP_PASSWORD"] != "pw-filled-through-a-request" {
		t.Fatalf("filled values not deployed: %v", envs)
	}
	// Both keys are now secrets (the plain placeholder became secret when filled), with no value left to set.
	_, env, _ = mustCall(t, w, tok, http.MethodGet, envPath(project, "mail"), "")
	for name, k := range keysByName(t, env) {
		if k["is_secret"] != true || k["needs_value"] != false || k["value"] != nil {
			t.Fatalf("%s after fill %v", name, k)
		}
	}
}

// TestValueRequestReadAfterRemoval: v1.28 item 3. A creator removed from
// the project no longer reads the request (not_found); a viewer still does.
func TestValueRequestReadAfterRemoval(t *testing.T) {
	f := newVRFixture(t)
	id := f.create(t, f.adminT, "PH", "")
	if err := f.w.mem.RemoveMember(context.Background(), f.sc.project.ID, f.admin.ID); err != nil {
		t.Fatal(err)
	}
	status, env := f.call(t, f.adminT, http.MethodGet, reqPath(id), "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	delegated := tokenKind(t, f.w, f.admin.ID, store.KindDelegated)
	status, env = f.call(t, delegated, http.MethodGet, reqPath(id), "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
}
