package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// frContent is planted file content: no response, audit row or log line may hold it.
const frContent = "FILE-REQUEST-CONTENT<do-not-leak>-7c1e\nline two\n"

// frFixture is a project with one existing plain file, taken.txt. The
// server and audit logs go to logs.
type frFixture struct {
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

func newFRFixture(t *testing.T) frFixture {
	t.Helper()
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	w.srv.ConsoleOrigin = "http://console.test/"
	logs := captureLog(w)
	owner := w.user("owner@ex.com", "Owner")
	admin := w.user("admin@ex.com", "Admin")
	viewer := w.user("viewer@ex.com", "Viewer")
	sc := w.scene(owner, admin, viewer, w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	status, env, _ := putFile(t, w, tok, project, "taken.txt", "already here", boolPtr(false))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	return frFixture{
		w: w, sc: sc, project: project, owner: owner, admin: admin, viewer: viewer,
		tok: tok, mcp: tokenKind(t, w, owner.ID, store.KindDelegated),
		adminT: w.token(admin.ID), viewerT: w.token(viewer.ID), logs: logs,
	}
}

func frPath(project string) string { return "/v1/projects/" + project + "/file-requests" }

func frReqPath(id string) string { return "/v1/file-requests/" + id }

func (f frFixture) call(t *testing.T, tok, method, path, body string) (int, envelope) {
	t.Helper()
	status, env, raw := mustCall(t, f.w, tok, method, path, body)
	if strings.Contains(raw, "FILE-REQUEST-CONTENT") {
		t.Fatalf("%s %s answered with the content", method, path)
	}
	return status, env
}

// create opens a request and returns its id after checking the 201 body.
func (f frFixture) create(t *testing.T, tok, name, body string) string {
	t.Helper()
	if body == "" {
		body = `{"name":"` + name + `"}`
	}
	status, env := f.call(t, tok, http.MethodPost, frPath(f.project), body)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	out := objectPayload(t, env)
	id, _ := out["request_id"].(string)
	if len(id) != 43 || !requestIDPattern.MatchString(id) {
		t.Fatalf("request_id %q", id)
	}
	if out["url"] != "http://console.test/fill-file/"+id || len(out) != 3 {
		t.Fatalf("create body %v", out)
	}
	if out["expires_at"] != f.w.srv.Now().Add(15*time.Minute).UTC().Format(time.RFC3339) {
		t.Fatalf("expires %v", out["expires_at"])
	}
	return id
}

func (f frFixture) state(t *testing.T, id string) string {
	t.Helper()
	status, env := f.call(t, f.mcp, http.MethodGet, frReqPath(id), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	s, _ := objectPayload(t, env)["state"].(string)
	return s
}

func (f frFixture) fill(t *testing.T, tok, id, content string, secretFlag *bool) (int, envelope) {
	t.Helper()
	body := map[string]any{"content": content}
	if secretFlag != nil {
		body["secret"] = *secretFlag
	}
	raw, _ := json.Marshal(body)
	return f.call(t, tok, http.MethodPost, frReqPath(id)+"/fill", string(raw))
}

// assertNoContent fails when the content is in any audit row or log line.
func (f frFixture) assertNoContent(t *testing.T, content string) {
	t.Helper()
	for _, row := range f.w.mem.AllAudits() {
		if strings.Contains(string(row.Detail), content) || strings.Contains(string(row.Detail), "FILE-REQUEST-CONTENT") {
			t.Fatalf("audit %s holds the content", row.Action)
		}
	}
	if strings.Contains(f.logs.String(), "FILE-REQUEST-CONTENT") {
		t.Fatal("log holds the content")
	}
}

func (f frFixture) fileMeta(t *testing.T, name string) (store.ProjectFile, bool) {
	t.Helper()
	item, err := f.w.mem.GetProjectFile(context.Background(), f.sc.project.ID, name)
	if err != nil {
		return store.ProjectFile{}, false
	}
	return item, true
}

func TestFileRequestCreate(t *testing.T) {
	f := newFRFixture(t)
	// A delegated session may create; nothing is written.
	id := f.create(t, f.mcp, "conf.txt", `{"name":"conf.txt","reason":"nginx needs its config"}`)
	if _, ok := f.fileMeta(t, "conf.txt"); ok {
		t.Fatal("create wrote a file")
	}
	status, env := f.call(t, f.mcp, http.MethodGet, frReqPath(id), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	got := objectPayload(t, env)
	if got["state"] != "pending" || got["name"] != "conf.txt" || got["secret"] != true || got["project_id"] != f.project || got["request_id"] != id || len(got) != 7 {
		t.Fatalf("read %v", got)
	}
	if strings.Contains(fmt.Sprint(got), "nginx needs") {
		t.Fatal("read carries the reason")
	}
	plainID := f.create(t, f.tok, "plain.txt", `{"name":"plain.txt","secret":false}`)
	status, env = f.call(t, f.tok, http.MethodGet, frReqPath(plainID), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if objectPayload(t, env)["secret"] != false {
		t.Fatal("plain flag")
	}
	n := 0
	for _, row := range f.w.mem.AllAudits() {
		if row.Action != audit.ActionFileRequested {
			continue
		}
		n++
		var d map[string]any
		_ = json.Unmarshal(row.Detail, &d)
		if len(d) != 3 || len(fmt.Sprint(d["request"])) != 8 || strings.Contains(string(row.Detail), "nginx") {
			t.Fatalf("detail %s", row.Detail)
		}
		if n == 1 && (row.Via != audit.ViaMCP || d["name"] != "conf.txt" || d["secret"] != true || d["request"] != auditRequestRef(secret.Hash(id))) {
			t.Fatalf("first row %s via %s", row.Detail, row.Via)
		}
		if n == 2 && (row.Via != audit.ViaConsole || d["secret"] != false) {
			t.Fatalf("second row %s via %s", row.Detail, row.Via)
		}
	}
	if n != 2 {
		t.Fatalf("file.requested rows %d", n)
	}

	cases := []struct {
		name, body string
		status     int
		code, msg  string
	}{
		{"name rule", `{"name":"Bad Name"}`, 400, "bad_request", "invalid name"},
		{"name dot", `{"name":".."}`, 400, "bad_request", "invalid name"},
		{"name before reason", `{"name":"UP","reason":"` + strings.Repeat("a", 201) + `"}`, 400, "bad_request", "invalid name"},
		{"reason long", `{"name":"a.txt","reason":"` + strings.Repeat("a", 201) + `"}`, 400, "bad_request", msgReason},
		{"reason control", `{"name":"a.txt","reason":"line\nbreak"}`, 400, "bad_request", msgReason},
		{"reason bidi", `{"name":"a.txt","reason":"pay ‮evil"}`, 400, "bad_request", msgReason},
		{"reason zero width", `{"name":"a.txt","reason":"a​b"}`, 400, "bad_request", msgReason},
		{"reason line separator", `{"name":"a.txt","reason":"a b"}`, 400, "bad_request", msgReason},
		{"reason before exists", `{"name":"taken.txt","reason":"a b"}`, 400, "bad_request", msgReason},
		{"exists", `{"name":"taken.txt"}`, 409, "conflict", "file taken.txt already exists; replace it in the console"},
		{"unknown field", `{"name":"a.txt","content":"x"}`, 400, "bad_request", "invalid json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, env := f.call(t, f.mcp, http.MethodPost, frPath(f.project), tc.body)
			mustHTTP(t, status, env, tc.status, tc.code, tc.msg)
		})
	}
	// 200 characters, multibyte, is fine.
	f.create(t, f.tok, "long.txt", `{"name":"long.txt","reason":"`+strings.Repeat("é", 200)+`"}`)

	// Viewers cannot ask; strangers do not see the project.
	status, env = f.call(t, f.viewerT, http.MethodPost, frPath(f.project), `{"name":"a.txt"}`)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	stranger := f.w.token(f.w.user("x@ex.com", "X").ID)
	status, _ = f.call(t, stranger, http.MethodPost, frPath(f.project), `{"name":"a.txt"}`)
	if status != http.StatusNotFound {
		t.Fatalf("stranger %d", status)
	}
}

// TestFileRequestFileCap: the 64-file limit refuses a create (after the
// exists check) and a fill.
func TestFileRequestFileCap(t *testing.T) {
	f := newFRFixture(t)
	early := f.create(t, f.mcp, "last.txt", "")
	for i := 1; i < store.MaxProjectFiles; i++ {
		status, env, _ := putFile(t, f.w, f.tok, f.project, fmt.Sprintf("f%02d.txt", i), "x", boolPtr(false))
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	}
	status, env := f.call(t, f.mcp, http.MethodPost, frPath(f.project), `{"name":"one-more.txt"}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "too many files")
	status, env = f.call(t, f.mcp, http.MethodPost, frPath(f.project), `{"name":"taken.txt"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "file taken.txt already exists; replace it in the console")
	status, env = f.fill(t, f.tok, early, frContent, nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "too many files")
	if f.state(t, early) != "pending" {
		t.Fatal("refused fill used the request")
	}
}

func TestFileRequestOpenLimit(t *testing.T) {
	f := newFRFixture(t)
	names := make([]string, store.MaxOpenRequests)
	for i := range names {
		names[i] = fmt.Sprintf("n%d.txt", i)
		f.create(t, f.mcp, names[i], "")
	}
	status, env := f.call(t, f.mcp, http.MethodPost, frPath(f.project), `{"name":"one-more.txt"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "too many open file requests; fill or wait for the ones you have")
	// Value requests are counted separately.
	putEnvFlag(t, f.w, f.tok, f.project, "svc", "A=b\n", false)
	status, env = f.call(t, f.mcp, http.MethodPost, vrPath(f.project, "svc"), `{"key":"NEW_KEY"}`)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	// At the limit, asking again for an open name replaces it and is not counted.
	f.create(t, f.mcp, names[0], "")
	// Another user has their own count.
	f.create(t, f.adminT, "one-more.txt", "")
	// Expired requests no longer count.
	setClock(f.w, 15*time.Minute)
	f.create(t, f.mcp, "one-more.txt", "")
}

// TestFileRequestReplaces: one open request per user and name.
func TestFileRequestReplaces(t *testing.T) {
	f := newFRFixture(t)
	old := f.create(t, f.mcp, "conf.txt", "")
	theirs := f.create(t, f.adminT, "conf.txt", "")
	newer := f.create(t, f.mcp, "conf.txt", "")
	if f.state(t, old) != "expired" || f.state(t, newer) != "pending" {
		t.Fatal("old request not replaced")
	}
	status, env := f.call(t, f.adminT, http.MethodGet, frReqPath(theirs), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if objectPayload(t, env)["state"] != "pending" {
		t.Fatal("another user's request was replaced")
	}
	status, env = f.fill(t, f.tok, old, frContent, nil)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request has expired; ask the agent for a new one")
	status, env = f.fill(t, f.tok, newer, frContent, nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	// Now the file exists: the other user's request answers conflict and stays pending.
	status, env = f.fill(t, f.adminT, theirs, frContent, nil)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "file conf.txt already exists; replace it in the console")
	f.assertNoContent(t, frContent)
}

func TestFileRequestConsoleOriginAndKey(t *testing.T) {
	f := newFRFixture(t)
	f.w.srv.ConsoleOrigin = ""
	status, env := f.call(t, f.mcp, http.MethodPost, frPath(f.project), `{"name":"a.txt"}`)
	mustHTTP(t, status, env, http.StatusInternalServerError, "error", "internal error")
	f.w.srv.ConsoleOrigin = "http://console.test"
	f.w.srv.Secrets = nil
	status, env = f.call(t, f.mcp, http.MethodPost, frPath(f.project), `{"name":"a.txt"}`)
	mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", errSecretsKey.Error())
	// A plain request needs no key; ticking secret on fill then does.
	id := f.create(t, f.mcp, "a.txt", `{"name":"a.txt","secret":false}`)
	status, env = f.fill(t, f.tok, id, frContent, boolPtr(true))
	mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", errSecretsKey.Error())
	if f.state(t, id) != "pending" {
		t.Fatal("request used without a write")
	}
}

func TestFileRequestReadAccess(t *testing.T) {
	f := newFRFixture(t)
	id := f.create(t, f.mcp, "conf.txt", "")
	for _, path := range []string{frReqPath(strings.Repeat("A", 43)), frReqPath("short")} {
		status, env := f.call(t, f.mcp, http.MethodGet, path, "")
		mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	}
	// A value request id is not a file request id.
	vrID := func() string {
		putEnvFlag(t, f.w, f.tok, f.project, "svc", "A=b\n", false)
		status, env := f.call(t, f.mcp, http.MethodPost, vrPath(f.project, "svc"), `{"key":"NEW_KEY"}`)
		mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
		s, _ := objectPayload(t, env)["request_id"].(string)
		return s
	}()
	status, env := f.call(t, f.mcp, http.MethodGet, frReqPath(vrID), "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	// Another owner or admin of the same project gets not_found.
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, frReqPath(id), ""},
		{http.MethodGet, frReqPath(id) + "/fill", ""},
		{http.MethodPost, frReqPath(id) + "/fill", `{"content":"x"}`},
		{http.MethodPost, frReqPath(id) + "/decline", ""},
	} {
		status, env := f.call(t, f.adminT, c.method, c.path, c.body)
		mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	}
	// The fill page data is console only, refused before any lookup.
	before := countAction(f.w.mem.AllAudits(), audit.ActionDelegatedDenied)
	status, env = f.call(t, f.mcp, http.MethodGet, frReqPath(id)+"/fill", "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	for _, path := range []string{"/fill", "/decline"} {
		status, env = f.call(t, f.mcp, http.MethodPost, frReqPath(id)+path, `{"content":"x"}`)
		mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	}
	if countAction(f.w.mem.AllAudits(), audit.ActionDelegatedDenied) != before+3 {
		t.Fatal("no delegated.denied rows")
	}
	if f.state(t, id) != "pending" {
		t.Fatal("state")
	}
}

func TestFileRequestFillPage(t *testing.T) {
	f := newFRFixture(t)
	reason := `<a href="https://evil.test">click</a>`
	raw, _ := json.Marshal(map[string]any{"name": "conf.txt", "secret": false, "reason": reason})
	id := f.create(t, f.mcp, "conf.txt", string(raw))
	req, err := http.NewRequest(http.MethodGet, f.w.http.URL+frReqPath(id)+"/fill", nil)
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
	body, _ := io.ReadAll(resp.Body)
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	page := objectPayload(t, env)
	if page["project_name"] != "Proj" || page["reason"] != reason || page["state"] != "pending" || page["name"] != "conf.txt" || page["secret"] != false || len(page) != 9 {
		t.Fatalf("page %v", page)
	}
}

func TestFileRequestFillSecret(t *testing.T) {
	f := newFRFixture(t)
	id := f.create(t, f.mcp, "key.pem", "")
	// Content rules, each refused with v1.9 text and the request left pending.
	for name, body := range map[string]string{
		"empty":     `{"content":""}`,
		"NUL":       `{"content":"a\u0000b"}`,
		"too large": `{"content":"` + strings.Repeat("a", 65537) + `"}`,
	} {
		status, env := f.call(t, f.tok, http.MethodPost, frReqPath(id)+"/fill", body)
		if status != http.StatusBadRequest || env.Code != "bad_request" || env.Msg != "invalid content" {
			t.Fatalf("%s: %d %s %q", name, status, env.Code, env.Msg)
		}
	}
	// Bytes that are not UTF-8 are refused before decoding.
	status, env := f.call(t, f.tok, http.MethodPost, frReqPath(id)+"/fill", "{\"content\":\"\xff\xfe\"}")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid json")
	// A secret request cannot be filled as plain.
	status, env = f.fill(t, f.tok, id, frContent, boolPtr(false))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "a secret file cannot be made plain")
	if _, ok := f.fileMeta(t, "key.pem"); ok || f.state(t, id) != "pending" {
		t.Fatal("a refused fill wrote")
	}
	// The default flag is the request's: secret, sealed with the env-set key.
	status, env = f.fill(t, f.tok, id, frContent, nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if out := objectPayload(t, env); out["state"] != "filled" || out["secret"] != true || len(out) != 2 {
		t.Fatalf("fill %v", out)
	}
	meta, ok := f.fileMeta(t, "key.pem")
	if !ok || !meta.IsSecret || meta.SHA256 != nil || meta.Size != len(frContent) || meta.UpdatedBy != f.owner.ID {
		t.Fatalf("meta %#v", meta)
	}
	mats, err := f.w.mem.ListFileMaterial(context.Background(), f.sc.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	opened := false
	for _, m := range mats {
		if m.Name != "key.pem" {
			continue
		}
		if m.Plain != "" || bytes.Contains(m.Ciphertext, []byte("FILE-REQUEST")) {
			t.Fatal("stored in the clear")
		}
		plain, err := testBox(t).Open(seal.FileAAD(f.project, "key.pem"), m.Nonce, m.Ciphertext)
		if err != nil || string(plain) != frContent {
			t.Fatalf("sealed content does not open: %v", err)
		}
		opened = true
	}
	if !opened {
		t.Fatal("no material")
	}
	// The console GET refuses a secret file, as for v1.9.
	status, env = f.call(t, f.tok, http.MethodGet, "/v1/projects/"+f.project+"/files/key.pem", "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	if f.state(t, id) != "filled" {
		t.Fatal("state")
	}
	var put *store.AuditEntry
	for _, row := range f.w.mem.AllAudits() {
		if row.Action == audit.ActionFilePut {
			cp := row
			put = &cp
		}
	}
	var d map[string]any
	if put == nil || json.Unmarshal(put.Detail, &d) != nil || d["via"] != "file_request" || d["request"] != auditRequestRef(secret.Hash(id)) ||
		d["name"] != "key.pem" || d["secret"] != true || len(d) != 4 || put.Via != audit.ViaConsole {
		t.Fatalf("file.put %#v %v", put, d)
	}
	// Single use.
	status, env = f.fill(t, f.tok, id, "again", nil)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request was already used")
	status, env = f.call(t, f.tok, http.MethodPost, frReqPath(id)+"/decline", "")
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request was already used")
	f.assertNoContent(t, frContent)
}

// TestFileRequestFillPlain: a plain request stays plain by default and may be made secret.
func TestFileRequestFillPlain(t *testing.T) {
	f := newFRFixture(t)
	plain := f.create(t, f.mcp, "app.conf", `{"name":"app.conf","secret":false}`)
	status, env := f.fill(t, f.tok, plain, frContent, nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if objectPayload(t, env)["secret"] != false {
		t.Fatal("plain flag")
	}
	if meta, ok := f.fileMeta(t, "app.conf"); !ok || meta.IsSecret || meta.Plain != frContent {
		t.Fatalf("plain file %#v", meta)
	}
	upgraded := f.create(t, f.mcp, "made-secret.conf", `{"name":"made-secret.conf","secret":false}`)
	status, env = f.fill(t, f.tok, upgraded, frContent, boolPtr(true))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if objectPayload(t, env)["secret"] != true {
		t.Fatal("secret flag")
	}
	if meta, ok := f.fileMeta(t, "made-secret.conf"); !ok || !meta.IsSecret || meta.Plain != "" {
		t.Fatalf("upgraded file %#v", meta)
	}
	// 65536 bytes is the limit, and it fits.
	big := f.create(t, f.mcp, "big.txt", "")
	status, env = f.fill(t, f.tok, big, strings.Repeat("b", 65536), nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	for _, row := range f.w.mem.AllAudits() {
		if row.Action == audit.ActionFilePut && strings.Contains(string(row.Detail), "FILE-REQUEST") {
			t.Fatal("audit holds content")
		}
	}
	f.assertNoContent(t, frContent)
}

func TestFileRequestExpiry(t *testing.T) {
	f := newFRFixture(t)
	id := f.create(t, f.mcp, "conf.txt", "")
	setClock(f.w, 15*time.Minute)
	if f.state(t, id) != "expired" {
		t.Fatal("state")
	}
	status, env := f.fill(t, f.tok, id, frContent, nil)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request has expired; ask the agent for a new one")
	status, env = f.call(t, f.tok, http.MethodPost, frReqPath(id)+"/decline", "")
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request has expired; ask the agent for a new one")
}

func TestFileRequestDecline(t *testing.T) {
	f := newFRFixture(t)
	id := f.create(t, f.mcp, "conf.txt", "")
	status, env := f.call(t, f.tok, http.MethodPost, frReqPath(id)+"/decline", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if out := objectPayload(t, env); out["state"] != "declined" || len(out) != 1 {
		t.Fatalf("decline %v", out)
	}
	if f.state(t, id) != "declined" || countAction(f.w.mem.AllAudits(), audit.ActionFileRequestDeclined) != 1 {
		t.Fatal("declined state or audit")
	}
	for _, row := range f.w.mem.AllAudits() {
		if row.Action == audit.ActionFileRequestDeclined {
			var d map[string]any
			_ = json.Unmarshal(row.Detail, &d)
			if d["name"] != "conf.txt" || d["request"] != auditRequestRef(secret.Hash(id)) || len(d) != 2 {
				t.Fatalf("detail %s", row.Detail)
			}
		}
	}
	status, env = f.fill(t, f.tok, id, frContent, nil)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this request was already used")
	if _, ok := f.fileMeta(t, "conf.txt"); ok {
		t.Fatal("declined request wrote")
	}
}

// TestFileRequestFileCreatedMeanwhile: item 7. The file check runs before the body.
func TestFileRequestFileCreatedMeanwhile(t *testing.T) {
	f := newFRFixture(t)
	id := f.create(t, f.mcp, "later.txt", "")
	status, env, _ := putFile(t, f.w, f.tok, f.project, "later.txt", "typed-in-console", boolPtr(false))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	for _, body := range []string{`{"content":"` + frContent[:10] + `"}`, `{"content":""}`, `not json`} {
		status, env := f.call(t, f.tok, http.MethodPost, frReqPath(id)+"/fill", body)
		mustHTTP(t, status, env, http.StatusConflict, "conflict", "file later.txt already exists; replace it in the console")
	}
	if f.state(t, id) != "pending" {
		t.Fatal("request left pending")
	}
	if meta, _ := f.fileMeta(t, "later.txt"); meta.Plain != "typed-in-console" {
		t.Fatal("the console file was replaced")
	}
}

func TestFileRequestRoleAndProjectGone(t *testing.T) {
	ctx := context.Background()
	f := newFRFixture(t)
	// The admin asks, then is demoted: reads the state, cannot fill or decline.
	id := f.create(t, f.adminT, "conf.txt", "")
	if _, err := f.w.mem.UpdateMemberRole(ctx, f.sc.project.ID, f.admin.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	status, env := f.call(t, f.adminT, http.MethodGet, frReqPath(id), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env = f.fill(t, f.adminT, id, frContent, nil)
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, env = f.call(t, f.adminT, http.MethodPost, frReqPath(id)+"/decline", "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")

	// A deleted project hides its requests.
	other := f.create(t, f.mcp, "conf.txt", "")
	if err := f.w.mem.DeleteProject(ctx, f.sc.project.ID, f.w.now); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ tok, method, path, body string }{
		{f.mcp, http.MethodGet, frReqPath(other), ""},
		{f.tok, http.MethodGet, frReqPath(other) + "/fill", ""},
		{f.tok, http.MethodPost, frReqPath(other) + "/fill", `{"content":"x"}`},
		{f.tok, http.MethodPost, frReqPath(other) + "/decline", ""},
	} {
		status, env := f.call(t, c.tok, c.method, c.path, c.body)
		mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	}
}

// TestFileRequestAuditHoldsNoID: audit rows name a request by a hash prefix
// only; no row holds any 6-character run of an id.
func TestFileRequestAuditHoldsNoID(t *testing.T) {
	f := newFRFixture(t)
	filled := f.create(t, f.mcp, "a.txt", "")
	declined := f.create(t, f.mcp, "b.txt", "")
	status, env := f.fill(t, f.tok, filled, frContent, nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env = f.call(t, f.tok, http.MethodPost, frReqPath(declined)+"/decline", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	refs := 0
	for _, row := range f.w.mem.AllAudits() {
		detail := string(row.Detail)
		for _, id := range []string{filled, declined} {
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
	// file.requested x2, file.put x1, file.request_declined x1.
	if refs != 4 {
		t.Fatalf("request refs %d", refs)
	}
	if strings.Contains(f.logs.String(), filled[:6]) {
		t.Fatal("log holds part of an id")
	}
	f.assertNoContent(t, frContent)
}

// TestFileRequestUnblocksDeploy is spec-file-request AC 2: an entry that
// mounts a missing file fails readiness files with add_file; once the
// person fills a request for it, readiness passes and the entry deploys.
func TestFileRequestUnblocksDeploy(t *testing.T) {
	programs := allowImages()
	programs["files"] = true
	w, sc, tok, log := refsWorld(t, programs)
	w.srv.ConsoleOrigin = "http://console.test"
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	status, env, _ := putFile(t, w, tok, project, "site.conf", "placeholder", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","files":[{"file":"site.conf","target":"/etc/nginx/conf.d/site.conf"}]}`)
	w.mem.DropProjectFile(sc.project.ID, "site.conf")
	_, checks, _ := readinessOf(t, w, tok, project, "web", "")
	files := checkNamed(t, checks, "files")
	if files.Status != "missing" || files.Fix["action"] != "add_file" || files.Fix["name"] != "site.conf" {
		t.Fatalf("files check %#v", files)
	}

	agent := tokenKind(t, w, sc.owner, store.KindDelegated)
	status, env, _ = mustCall(t, w, agent, http.MethodPost, frPath(project), `{"name":"site.conf","reason":"nginx config"}`)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	id, _ := objectPayload(t, env)["request_id"].(string)
	raw, _ := json.Marshal(map[string]any{"content": frContent})
	status, env, _ = mustCall(t, w, tok, http.MethodPost, frReqPath(id)+"/fill", string(raw))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")

	_, checks, rawReady := readinessOf(t, w, agent, project, "web", "")
	if files := checkNamed(t, checks, "files"); files.Status != "ok" {
		t.Fatalf("files check after fill %#v", files)
	}
	if strings.Contains(rawReady, "FILE-REQUEST-CONTENT") {
		t.Fatal("readiness holds the content")
	}
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "fr-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if log.count() != 1 {
		t.Fatalf("tasks %d", log.count())
	}
	frame, _ := json.Marshal(log.raw(0))
	if !strings.Contains(string(frame), "site.conf") {
		t.Fatalf("deploy frame lacks the file: %s", frame)
	}
}

func checkNamed(t *testing.T, checks []readinessCheck, name string) readinessCheck {
	t.Helper()
	for _, c := range checks {
		if c.Check == name {
			return c
		}
	}
	t.Fatalf("no %s check", name)
	return readinessCheck{}
}

// TestFileRequestReadAfterRemoval: v1.30 item 3. A creator removed from
// the project no longer reads the request (not_found).
func TestFileRequestReadAfterRemoval(t *testing.T) {
	f := newFRFixture(t)
	id := f.create(t, f.adminT, "conf.txt", "")
	if err := f.w.mem.RemoveMember(context.Background(), f.sc.project.ID, f.admin.ID); err != nil {
		t.Fatal(err)
	}
	status, env := f.call(t, f.adminT, http.MethodGet, frReqPath(id), "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	delegated := tokenKind(t, f.w, f.admin.ID, store.KindDelegated)
	status, env = f.call(t, delegated, http.MethodGet, frReqPath(id), "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
}
