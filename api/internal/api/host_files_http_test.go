package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.44 host config files (spec-host-config-files §5 items 10–11).

const (
	fakeSecret    = "s3cr3t-FAKE-VALUE-9f1c"
	fakeKeyValue  = "kv-FAKE-OTHER-77"
	agentSHA      = "d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4d4"
	defaultResult = `{"outcome":"succeeded","took_ms":1200,"sha256":"` + agentSHA + `"}`
)

// agentReply is the frame the fake agent sends for one task: nil sends
// nothing. The task_id is added.
type agentReply func(spec map[string]any) (frame map[string]any, delay time.Duration)

func succeed(payload string) agentReply {
	return func(map[string]any) (map[string]any, time.Duration) {
		return map[string]any{"type": "task_result", "state": "succeeded", "payload": json.RawMessage(payload)}, 0
	}
}

type hfCast struct {
	w         *world
	sc        scene
	owner     store.User
	ownerTok  string
	adminTok  string // org admin
	memberTok string // org member, no project
	projTok   string // project admin, not in the org
	viewTok   string // project viewer
	extraTok  string // no relation
	base      string
	conn      *websocket.Conn
	logs      *syncBuffer
	mu        sync.Mutex
	tasks     []map[string]any
	reply     agentReply
}

func hostFilesPrograms() map[string]any {
	p := allowPrograms(false)
	p["host_files"] = []map[string]any{
		{"name": "dd-policy", "path": "/etc/dd-worker/policy.toml", "secret": false, "unit": "dd-worker.service", "health": true},
		{"name": "dd-secrets", "path": "/etc/dd-worker/secrets.env", "secret": true, "unit": "dd-worker.service", "health": true},
	}
	return p
}

func newHFCast(t *testing.T, programs map[string]any) *hfCast {
	t.Helper()
	logs := &syncBuffer{}
	w := newWorldLog(t, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	orgAdmin := w.user("orgadmin@ex.com", "OrgAdmin")
	orgMember := w.user("orgmember@ex.com", "OrgMember")
	projAdmin := w.user("admin@ex.com", "Admin")
	viewer := w.user("viewer@ex.com", "Viewer")
	sc := w.scene(owner, projAdmin, viewer, w.user("spare@ex.com", "Spare"), w.user("extra@ex.com", "Extra"))
	ctx := context.Background()
	if _, err := w.mem.AddOrgMember(ctx, sc.project.OrgID, orgAdmin.ID, store.OrgRoleAdmin, owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	if _, err := w.mem.AddOrgMember(ctx, sc.project.OrgID, orgMember.ID, store.OrgRoleMember, owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	c := &hfCast{
		w: w, sc: sc, owner: owner, ownerTok: w.token(owner.ID), adminTok: w.token(orgAdmin.ID), memberTok: w.token(orgMember.ID),
		projTok: w.token(projAdmin.ID), viewTok: w.token(viewer.ID), extraTok: w.token(sc.extra.ID), logs: logs,
		base: "/v1/orgs/" + sc.project.OrgID.String() + "/hosts/" + sc.host.ID.String() + "/files",
	}
	if programs != nil {
		c.connect(t, programs)
	}
	return c
}

// connect says hello with programs and answers tasks until the test ends.
func (c *hfCast) connect(t *testing.T, programs map[string]any) {
	t.Helper()
	c.connectHello(t, map[string]any{"agent_version": "0.2.12", "programs": programs})
}

// connectHello is connect with the hello fields in extra (agent_version
// and programs at least).
func (c *hfCast) connectHello(t *testing.T, extra map[string]any) {
	t.Helper()
	conn := c.w.dial(c.sc.credPlain)
	// A 64 KiB file is an ~88 KiB task frame, above the library's 32 KiB default.
	conn.SetReadLimit(1 << 20)
	hello := map[string]any{
		"v": 1, "type": "hello", "host_id": c.sc.host.ID.String(), "credential_id": c.sc.credID.String(),
		"os": "linux", "arch": "arm64", "services": []any{},
	}
	for k, v := range extra {
		hello[k] = v
	}
	writeFrame(t, conn, hello)
	if hello := readFrame(t, conn); hello["type"] != "hello_ok" {
		t.Fatalf("hello %#v", hello)
	}
	c.conn = conn
	go func() {
		for {
			_, b, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var msg map[string]any
			if json.Unmarshal(b, &msg) != nil || msg["type"] != "task" {
				continue
			}
			c.mu.Lock()
			c.tasks = append(c.tasks, msg)
			reply := c.reply
			c.mu.Unlock()
			if reply == nil {
				reply = succeed(defaultResult)
			}
			spec, _ := msg["spec"].(map[string]any)
			frame, delay := reply(spec)
			if frame == nil {
				continue
			}
			frame["v"], frame["task_id"] = 1, msg["task_id"]
			go func() {
				time.Sleep(delay)
				raw, _ := json.Marshal(frame)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = conn.Write(ctx, websocket.MessageText, raw)
				cancel()
			}()
		}
	}()
}

func (c *hfCast) setReply(r agentReply) {
	c.mu.Lock()
	c.reply = r
	c.mu.Unlock()
}

func (c *hfCast) taskSpecs() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := []map[string]any{}
	for _, t := range c.tasks {
		spec, _ := t["spec"].(map[string]any)
		out = append(out, spec)
	}
	return out
}

func (c *hfCast) do(t *testing.T, tok, method, path, body string, want int, code string) envelope {
	t.Helper()
	status, env := c.w.do(tok, method, c.base+path, body)
	if status != want || env.Code != code {
		t.Fatalf("%s %s: %d %s %q, want %d %s", method, path, status, env.Code, env.Msg, want, code)
	}
	return env
}

func items(t *testing.T, env envelope) []map[string]any {
	t.Helper()
	raw, _ := objectPayload(t, env)["items"].([]any)
	out := make([]map[string]any, len(raw))
	for i, it := range raw {
		out[i] = it.(map[string]any)
	}
	return out
}

func envJSON(env envelope) string {
	b, _ := json.Marshal(env)
	return string(b)
}

func auditsOf(c *hfCast, action string) []store.AuditEntry {
	var out []store.AuditEntry
	for _, a := range c.w.mem.AllAudits() {
		if a.Action == action {
			out = append(out, a)
		}
	}
	return out
}

func detailKeys(t *testing.T, a store.AuditEntry) []string {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(a.Detail, &d); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// waitFor polls cond for up to 2 s.
func hfWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 200 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

var hex64 = regexp.MustCompile(`[0-9a-f]{64}`)

// noSecretAnywhere fails when a fake value or the agent's hash shows in an
// answer, the logs, an audit row, a stored apply, the stored status or a
// problem; and checks the secret file's versions hold only ciphertext.
func noSecretAnywhere(t *testing.T, c *hfCast, answers ...envelope) {
	t.Helper()
	bad := func(where, s string) {
		t.Helper()
		for _, needle := range []string{fakeSecret, fakeKeyValue, "FAKE", agentSHA} {
			if strings.Contains(s, needle) {
				t.Fatalf("%s carries %q: %s", where, needle, s)
			}
		}
	}
	for _, env := range answers {
		bad("answer", envJSON(env))
	}
	bad("logs", c.logs.String())
	if hex64.MatchString(c.logs.String()) {
		t.Fatalf("logs carry a hash: %s", c.logs.String())
	}
	for _, a := range c.w.mem.AllAudits() {
		bad("audit "+a.Action, string(a.Detail))
	}
	ctx := context.Background()
	states, _ := c.w.mem.HostFileStates(ctx, c.sc.host.ID)
	raw, _ := json.Marshal(states)
	bad("applies", string(raw))
	status, _ := c.w.mem.GetHostFilesStatus(ctx, c.sc.host.ID)
	raw, _ = json.Marshal(status)
	bad("status", string(raw))
	problems, _ := c.w.mem.ListProblems(ctx, store.ProblemFilter{All: true})
	raw, _ = json.Marshal(problems)
	bad("problems", string(raw))
	versions, _ := c.w.mem.ListHostFileVersions(ctx, c.sc.host.ID, "dd-secrets")
	for _, v := range versions {
		full, err := c.w.mem.GetHostFileVersion(ctx, c.sc.host.ID, "dd-secrets", v.Version)
		if err != nil {
			t.Fatal(err)
		}
		if v.Plain != "" || full.Plain != "" || full.SHA256 != "" || bytes.Contains(full.Ciphertext, []byte(fakeSecret)) || len(full.Ciphertext) != full.Size+16 {
			t.Fatalf("secret version %d stored in the clear", v.Version)
		}
	}
}

func TestHostFilesHelloField(t *testing.T) {
	c := newHFCast(t, hostFilesPrograms())
	h, err := c.w.mem.GetHost(context.Background(), c.sc.host.ID)
	if err != nil || h.Programs == nil || !h.Programs.Enabled || len(h.Programs.HostFiles) != 2 || !h.Programs.HostFiles[1].Secret {
		t.Fatalf("programs %+v %v", h.Programs, err)
	}
	// No Host answer carries host_files.
	for _, path := range []string{
		"/v1/hosts/" + c.sc.host.ID.String(),
		"/v1/projects/" + c.sc.project.ID.String() + "/hosts",
		"/v1/orgs/" + c.sc.project.OrgID.String() + "/hosts",
	} {
		status, env := c.w.do(c.ownerTok, http.MethodGet, path, "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		if strings.Contains(envJSON(env), "host_files") || strings.Contains(envJSON(env), "/etc/dd-worker") {
			t.Fatalf("%s: %s", path, envJSON(env))
		}
	}
}

func TestHostFilesWhoMay(t *testing.T) {
	c := newHFCast(t, hostFilesPrograms())
	// Project members with access see the list, without content.
	for _, tok := range []string{c.ownerTok, c.adminTok, c.projTok, c.viewTok} {
		got := items(t, c.do(t, tok, http.MethodGet, "", "", http.StatusOK, "ok"))
		if len(got) != 2 || got[0]["name"] != "dd-policy" || got[0]["status"] != "unknown" || got[1]["secret"] != true || got[0]["applied_version"] != nil {
			t.Fatalf("list %v", got)
		}
	}
	// An org member without project access and a stranger do not see the host.
	for _, tok := range []string{c.memberTok, c.extraTok} {
		c.do(t, tok, http.MethodGet, "", "", http.StatusNotFound, "not_found")
		c.do(t, tok, http.MethodPut, "/dd-policy", `{"content":"x"}`, http.StatusNotFound, "not_found")
	}
	c.do(t, "", http.MethodGet, "", "", http.StatusUnauthorized, "unauthorized")
	adminRoutes := [][3]string{
		{http.MethodGet, "/dd-policy", ""},
		{http.MethodGet, "/dd-policy/versions/1", ""},
		{http.MethodPut, "/dd-policy", `{"content":"a = 1\n"}`},
		{http.MethodPost, "/dd-policy/apply", `{"version":1}`},
	}
	for _, tok := range []string{c.projTok, c.viewTok} {
		for _, rt := range adminRoutes {
			c.do(t, tok, rt[0], rt[1], rt[2], http.StatusForbidden, "forbidden")
		}
	}
	// Delegated (MCP): forbidden on every route, no content, a denial audit row each.
	c.do(t, c.ownerTok, http.MethodPut, "/dd-policy", `{"content":"plain-content-marker\n"}`, http.StatusOK, "ok")
	tok := tokenKind(t, c.w, c.owner.ID, store.KindDelegated)
	before := len(auditsOf(c, audit.ActionDelegatedDenied))
	for _, rt := range append([][3]string{{http.MethodGet, "", ""}}, adminRoutes...) {
		env := c.do(t, tok, rt[0], rt[1], rt[2], http.StatusForbidden, "forbidden")
		if strings.Contains(envJSON(env), "plain-content-marker") || strings.Contains(envJSON(env), "dd-worker") {
			t.Fatalf("delegated got content: %s", envJSON(env))
		}
	}
	if got := len(auditsOf(c, audit.ActionDelegatedDenied)) - before; got != 5 {
		t.Fatalf("delegated denials %d", got)
	}
	// Legacy MCP and other non-console sessions too.
	legacy := tokenKind(t, c.w, c.owner.ID, store.KindLegacyMCP)
	for _, rt := range append([][3]string{{http.MethodGet, "", ""}}, adminRoutes...) {
		if status, env := c.w.do(legacy, rt[0], c.base+rt[1], rt[2]); status != http.StatusForbidden && status != http.StatusUnauthorized {
			t.Fatalf("legacy %s %s: %d %s", rt[0], rt[1], status, env.Code)
		}
	}
	// A host of another org under this org's path, and this host under another org's path.
	other, err := c.w.mem.CreateOrg(context.Background(), c.owner.ID, "Other", "other", c.w.now)
	if err != nil {
		t.Fatal(err)
	}
	otherProj, err := c.w.mem.CreateProject(context.Background(), other.ID, c.owner.ID, "OP", "op", c.w.now)
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, _ := enrollAgentHost(t, c.w, otherProj, c.owner.ID, "foreign")
	setPrograms(t, c.w, foreign.ID, &store.HostPrograms{Enabled: true, RunAs: []string{}, HostFiles: []store.HostFile{{Name: "dd-policy", Path: "/etc/d/p", Unit: "u.service"}}})
	for _, path := range []string{
		"/v1/orgs/" + c.sc.project.OrgID.String() + "/hosts/" + foreign.ID.String() + "/files/dd-policy",
		"/v1/orgs/" + other.ID.String() + "/hosts/" + c.sc.host.ID.String() + "/files/dd-policy",
	} {
		for _, rt := range [][2]string{{http.MethodPut, `{"content":"x"}`}, {http.MethodPost, `{"version":1}`}} {
			p := path
			if rt[0] == http.MethodPost {
				p += "/apply"
			}
			if status, env := c.w.do(c.ownerTok, rt[0], p, rt[1]); status != http.StatusNotFound || env.Code != "not_found" {
				t.Fatalf("%s %s: %d %s", rt[0], p, status, env.Code)
			}
		}
	}
	c.do(t, c.ownerTok, http.MethodGet, "/nope", "", http.StatusNotFound, "not_found")
	c.do(t, c.ownerTok, http.MethodGet, "/Bad", "", http.StatusBadRequest, "bad_request")
}

func TestHostFilesPlainVersions(t *testing.T) {
	c := newHFCast(t, hostFilesPrograms())
	if p := objectPayload(t, c.do(t, c.adminTok, http.MethodGet, "/dd-policy", "", http.StatusOK, "ok")); p["content"] != "" || len(p["versions"].([]any)) != 0 {
		t.Fatalf("empty %v", p)
	}
	body := "managed_url = \"x\"\r\n[repo]\nname = \"é\"\n"
	raw, _ := json.Marshal(map[string]string{"content": body})
	if v := objectPayload(t, c.do(t, c.adminTok, http.MethodPut, "/dd-policy", string(raw), http.StatusOK, "ok"))["version"]; v != float64(1) {
		t.Fatalf("version %v", v)
	}
	c.do(t, c.adminTok, http.MethodPut, "/dd-policy", `{"content":"v2\n"}`, http.StatusOK, "ok")
	p := objectPayload(t, c.do(t, c.adminTok, http.MethodGet, "/dd-policy", "", http.StatusOK, "ok"))
	vs := p["versions"].([]any)
	if p["content"] != "v2\n" || len(vs) != 2 || vs[0].(map[string]any)["version"] != float64(2) || len(vs[0].(map[string]any)["sha256"].(string)) != 64 {
		t.Fatalf("detail %v", p)
	}
	if objectPayload(t, c.do(t, c.adminTok, http.MethodGet, "/dd-policy/versions/1", "", http.StatusOK, "ok"))["content"] != body {
		t.Fatal("version 1 not saved exactly")
	}
	// Boundaries: exactly 65536 bytes is accepted; one more byte, multi-byte over the limit, NUL, empty are not.
	exact, _ := json.Marshal(map[string]string{"content": strings.Repeat("a", 65536)})
	c.do(t, c.adminTok, http.MethodPut, "/dd-policy", string(exact), http.StatusOK, "ok")
	over, _ := json.Marshal(map[string]string{"content": strings.Repeat("a", 65535) + "é"})
	nul, _ := json.Marshal(map[string]string{"content": "a\x00b"})
	for _, bad := range []string{`{"content":""}`, `{"content":"` + strings.Repeat("a", 65537) + `"}`, string(over), string(nul), `{}`, `{"content":"a","secret":true}`} {
		c.do(t, c.adminTok, http.MethodPut, "/dd-policy", bad, http.StatusBadRequest, "bad_request")
	}
	for _, v := range []string{"0", "x", "1073741825"} {
		c.do(t, c.adminTok, http.MethodGet, "/dd-policy/versions/"+v, "", http.StatusBadRequest, "bad_request")
	}
	saves := auditsOf(c, audit.ActionHostFileSave)
	if len(saves) != 3 {
		t.Fatalf("saves %d", len(saves))
	}
	if got := strings.Join(detailKeys(t, saves[0]), ","); got != "host,name,secret,size,version" {
		t.Fatalf("save audit keys %s", got)
	}
}

func TestHostFilesSecret(t *testing.T) {
	c := newHFCast(t, hostFilesPrograms())
	var answers []envelope
	do := func(method, path, body string, want int, code string) envelope {
		t.Helper()
		env := c.do(t, c.ownerTok, method, path, body, want, code)
		answers = append(answers, env)
		return env
	}
	do(http.MethodPut, "/dd-secrets", `{"base_version":0,"set":{"DD_TOKEN":"`+fakeSecret+`","B_KEY":"","EQ":"a=b c `+fakeKeyValue+`"}}`, http.StatusOK, "ok")
	p := objectPayload(t, do(http.MethodGet, "/dd-secrets", "", http.StatusOK, "ok"))
	keys := p["keys"].([]any)
	if len(keys) != 3 || keys[0].(map[string]any)["name"] != "B_KEY" || keys[0].(map[string]any)["set"] != false || keys[2].(map[string]any)["set"] != true {
		t.Fatalf("keys %v", keys)
	}
	if v := p["versions"].([]any)[0].(map[string]any); v["sha256"] != nil || v["version"] != float64(1) {
		t.Fatalf("secret version %v", v)
	}
	// One staged edit: set and remove together against the latest; a stale base is conflict.
	do(http.MethodPut, "/dd-secrets", `{"base_version":0,"set":{"X":"y"}}`, http.StatusConflict, "conflict")
	do(http.MethodPut, "/dd-secrets", `{"base_version":1,"set":{"C":"3"},"remove":["B_KEY"]}`, http.StatusOK, "ok")
	do(http.MethodPut, "/dd-secrets", `{"base_version":1,"set":{"X":"y"}}`, http.StatusConflict, "conflict")
	many := func(n int, value string) string {
		set := map[string]string{}
		for i := range n {
			set["K"+strings.Repeat("A", i/26)+string(rune('A'+i%26))] = value
		}
		raw, _ := json.Marshal(set)
		return string(raw)
	}
	for _, bad := range []string{
		`{"set":{"X":"y"}}`,
		`{"base_version":1073741825,"set":{"X":"y"}}`,
		`{"base_version":2,"set":{"bad":"y"}}`,
		`{"base_version":2,"set":{"` + "K" + strings.Repeat("A", 64) + `":"y"}}`,
		`{"base_version":2,"set":{"X":"two\nlines"}}`,
		`{"base_version":2,"set":{"X":"nul\u0000"}}`,
		`{"base_version":2,"set":{"X":"` + strings.Repeat("v", 4097) + `"}}`,
		`{"base_version":2,"set":{"X":"` + strings.Repeat("é", 2048) + `v"}}`,
		`{"base_version":2,"remove":["NOPE"]}`,
		`{"base_version":2,"set":{"C":"1"},"remove":["C"]}`,
		`{"base_version":2,"remove":["C","DD_TOKEN","EQ"]}`,
		`{"base_version":2}`,
		`{"base_version":2,"set":` + many(129, "v") + `}`,
		`{"base_version":2,"set":` + many(126, "v") + `}`, // 3 kept + 126 = 129
		`{"base_version":2,"set":` + many(100, strings.Repeat("v", 700)) + `}`,
		`{"content":"x"}`,
	} {
		do(http.MethodPut, "/dd-secrets", bad, http.StatusBadRequest, "bad_request")
	}
	// Accepted limits: a 64-character key, a 4096-byte value (also multi-byte), 128 keys in all.
	do(http.MethodPut, "/dd-secrets", `{"base_version":2,"set":{"`+"K"+strings.Repeat("A", 63)+`":"`+strings.Repeat("v", 4096)+`","MB":"`+strings.Repeat("é", 2048)+`"}}`, http.StatusOK, "ok")
	do(http.MethodPut, "/dd-secrets", `{"base_version":3,"remove":["`+"K"+strings.Repeat("A", 63)+`","MB"],"set":`+many(125, "v")+`}`, http.StatusOK, "ok")
	do(http.MethodPut, "/dd-secrets", `{"base_version":4,"remove":`+func() string {
		var names []string
		for i := range 125 {
			names = append(names, `"K`+strings.Repeat("A", i/26)+string(rune('A'+i%26))+`"`)
		}
		return "[" + strings.Join(names, ",") + "]"
	}()+`,"set":{"B_KEY":""}}`, http.StatusOK, "ok")
	do(http.MethodGet, "/dd-secrets/versions/1", "", http.StatusForbidden, "forbidden")
	// Apply sends the rendered file, KEY=value in key order; an empty value is KEY=.
	r := objectPayload(t, do(http.MethodPost, "/dd-secrets/apply", `{"version":5}`, http.StatusOK, "ok"))
	if r["outcome"] != "succeeded" || r["sha256"] != nil || r["took_ms"] != float64(1200) {
		t.Fatalf("result %v", r)
	}
	specs := c.taskSpecs()
	got, _ := base64.StdEncoding.DecodeString(specs[0]["content_b64"].(string))
	want := "B_KEY=\nC=3\nDD_TOKEN=" + fakeSecret + "\nEQ=a=b c " + fakeKeyValue + "\n"
	if _, sent := specs[0]["settle_sec"]; sent || string(got) != want || specs[0]["name"] != "dd-secrets" || specs[0]["force"] != false {
		t.Fatalf("spec content %q", got)
	}
	if it := items(t, do(http.MethodGet, "", "", http.StatusOK, "ok"))[1]; it["applied_version"] != float64(5) || it["latest_version"] != float64(5) {
		t.Fatalf("item %v", it)
	}
	if got := strings.Join(detailKeys(t, auditsOf(c, audit.ActionHostFileSave)[0]), ","); got != "host,name,secret,size,version" {
		t.Fatalf("secret save audit keys %s", got)
	}
	if got := strings.Join(detailKeys(t, auditsOf(c, audit.ActionHostFileApply)[0]), ","); got != "host,name,outcome,reason,stage,version" {
		t.Fatalf("apply audit keys %s", got)
	}
	noSecretAnywhere(t, c, answers...)
}

// failApplies fails RecordHostFileApply (criterion 10: the failure log line).
type failApplies struct{ store.Store }

func (failApplies) RecordHostFileApply(context.Context, store.HostFileApply) error {
	return errors.New("db down " + fakeSecret)
}

func TestHostFilesSecretLogs(t *testing.T) {
	c := newHFCast(t, hostFilesPrograms())
	var answers []envelope
	answers = append(answers, c.do(t, c.ownerTok, http.MethodPut, "/dd-secrets", `{"base_version":0,"set":{"DD_TOKEN":"`+fakeSecret+`","OTHER":"`+fakeKeyValue+`"}}`, http.StatusOK, "ok"))
	c.setReply(succeed(`{"outcome":"rolled_back","stage":"health","reason":"not_connected","took_ms":5,"sha256":"` + agentSHA + `"}`))
	answers = append(answers, c.do(t, c.ownerTok, http.MethodPost, "/dd-secrets/apply", `{"version":1}`, http.StatusOK, "ok"))
	// The apply record fails: the result still answers, one short log line.
	c.w.srv.Store = failApplies{c.w.mem}
	answers = append(answers, c.do(t, c.ownerTok, http.MethodPost, "/dd-secrets/apply", `{"version":1}`, http.StatusOK, "ok"))
	if !strings.Contains(c.logs.String(), "host file apply record failed") {
		t.Fatal("no record failure line")
	}
	c.w.srv.Store = c.w.mem
	// A status frame with a hash for the secret file.
	writeFrame(t, c.conn, map[string]any{"v": 1, "type": "host_files_status", "files": []map[string]any{
		{"name": "dd-secrets", "exists": true, "owner_ok": true, "matches_applied": false, "size": 40, "sha256": agentSHA},
	}})
	hfWait(t, "status", func() bool {
		st, _ := c.w.mem.GetHostFilesStatus(context.Background(), c.sc.host.ID)
		return st != nil
	})
	// Decrypt failure under another key.
	c.w.srv.Secrets = testBoxByte(t, 9)
	answers = append(answers, c.do(t, c.ownerTok, http.MethodGet, "/dd-secrets", "", http.StatusInternalServerError, "error"))
	answers = append(answers, c.do(t, c.ownerTok, http.MethodPost, "/dd-secrets/apply", `{"version":1}`, http.StatusInternalServerError, "error"))
	if !strings.Contains(c.logs.String(), "host file open failed") {
		t.Fatal("no decrypt failure line")
	}
	noSecretAnywhere(t, c, answers...)
}

func TestHostFilesRetentionAndInFlight(t *testing.T) {
	c := newHFCast(t, hostFilesPrograms())
	save := func() {
		t.Helper()
		c.do(t, c.ownerTok, http.MethodPut, "/dd-policy", `{"content":"x\n"}`, http.StatusOK, "ok")
	}
	save()
	c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":1}`, http.StatusOK, "ok")
	for range 51 {
		save() // v2..v52
	}
	rows, _ := c.w.mem.ListHostFileVersions(context.Background(), c.sc.host.ID, "dd-policy")
	if len(rows) != 51 || rows[0].Version != 52 || rows[49].Version != 3 || rows[50].Version != 1 {
		t.Fatalf("kept %d", len(rows))
	}
	// A version being applied is kept by saves during the apply.
	c.setReply(func(map[string]any) (map[string]any, time.Duration) {
		return map[string]any{"type": "task_result", "state": "succeeded", "payload": json.RawMessage(`{"outcome":"check_failed","reason":"bad_repo","took_ms":3}`)}, 600 * time.Millisecond
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.w.do(c.ownerTok, http.MethodPost, c.base+"/dd-policy/apply", `{"version":3}`)
	}()
	hfWait(t, "task", func() bool { return len(c.taskSpecs()) == 2 })
	save() // v53: v3 would leave the newest 50
	<-done
	if _, err := c.w.mem.GetHostFileVersion(context.Background(), c.sc.host.ID, "dd-policy", 3); err != nil {
		t.Fatalf("in-flight target pruned: %v", err)
	}
}

func TestHostFilesApplyOutcomes(t *testing.T) {
	c := newHFCast(t, hostFilesPrograms())
	c.do(t, c.ownerTok, http.MethodPut, "/dd-policy", `{"content":"x\n"}`, http.StatusOK, "ok")
	c.setReply(func(spec map[string]any) (map[string]any, time.Duration) {
		payload := `{"outcome":"worker_busy","stage":"idle","took_ms":5}`
		if spec["force"] == true {
			payload = `{"outcome":"succeeded","took_ms":5}`
		}
		return map[string]any{"type": "task_result", "state": "succeeded", "payload": json.RawMessage(payload)}, 0
	})
	env := c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":1}`, http.StatusConflict, "worker_busy")
	if objectPayload(t, env)["outcome"] != "worker_busy" {
		t.Fatalf("busy %v", env.Payload)
	}
	c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":1,"force":true,"settle_sec":30}`, http.StatusOK, "ok")
	c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":1,"force":true,"settle_sec":180}`, http.StatusOK, "ok")
	if s := c.taskSpecs(); s[1]["force"] != true || s[1]["settle_sec"] != float64(30) || s[2]["settle_sec"] != float64(180) {
		t.Fatalf("specs %v", s)
	}
	for _, bad := range []string{`{}`, `{"version":0}`, `{"version":1073741825}`, `{"version":1,"settle_sec":29}`, `{"version":1,"settle_sec":181}`} {
		c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", bad, http.StatusBadRequest, "bad_request")
	}
	c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":9}`, http.StatusNotFound, "not_found")
	// rolled_back with a reason; unknown stage and reason tokens are dropped from the answer and the row.
	c.setReply(succeed(`{"outcome":"rolled_back","stage":"health","reason":"not_connected","took_ms":61000}`))
	r := objectPayload(t, c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":1}`, http.StatusOK, "ok"))
	if r["outcome"] != "rolled_back" || r["stage"] != "health" || r["reason"] != "not_connected" {
		t.Fatalf("rolled back %v", r)
	}
	c.setReply(succeed(`{"outcome":"check_failed","stage":"Bad Stage","reason":"has space ` + fakeSecret + `","took_ms":99999999999}`))
	r = objectPayload(t, c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":1}`, http.StatusOK, "ok"))
	if r["outcome"] != "check_failed" || r["stage"] != nil || r["reason"] != nil || r["took_ms"] != float64(1<<30) {
		t.Fatalf("stripped %v", r)
	}
	states, _ := c.w.mem.HostFileStates(context.Background(), c.sc.host.ID)
	if last := states[0].LastApply; last.Stage != "" || last.Reason != "" || last.TookMS != 1<<30 || *states[0].AppliedVersion != 1 {
		t.Fatalf("stored %+v", last)
	}
	it := items(t, c.do(t, c.viewTok, http.MethodGet, "", "", http.StatusOK, "ok"))[0]
	if it["last_apply"].(map[string]any)["outcome"] != "check_failed" {
		t.Fatalf("item %v", it)
	}
	// The agent sends every outcome but succeeded and unchanged as a failed
	// task whose error_code is the outcome and whose payload is the result.
	for _, outcome := range []string{"check_failed", "rolled_back", "rollback_failed", "forbidden_spec"} {
		c.setReply(func(map[string]any) (map[string]any, time.Duration) {
			return map[string]any{"type": "task_result", "state": "failed", "error_code": outcome, "payload": json.RawMessage(`{"outcome":"` + outcome + `","stage":"target","reason":"bad_repo","took_ms":1}`)}, 0
		})
		if r := objectPayload(t, c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":1}`, http.StatusOK, "ok")); r["outcome"] != outcome || r["stage"] != "target" || r["reason"] != "bad_repo" {
			t.Fatalf("%s %v", outcome, r)
		}
	}
	c.setReply(func(map[string]any) (map[string]any, time.Duration) {
		return map[string]any{"type": "task_result", "state": "failed", "error_code": "worker_busy", "payload": json.RawMessage(`{"outcome":"worker_busy","stage":"idle","took_ms":1}`)}, 0
	})
	c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":1}`, http.StatusConflict, "worker_busy")
	if got := len(auditsOf(c, audit.ActionHostFileApply)); got != 10 {
		t.Fatalf("apply audits %d", got)
	}
}

func TestHostFilesApplyFailures(t *testing.T) {
	c := newHFCast(t, hostFilesPrograms())
	c.do(t, c.ownerTok, http.MethodPut, "/dd-policy", `{"content":"x\n"}`, http.StatusOK, "ok")
	applies := func() int {
		states, _ := c.w.mem.HostFileStates(context.Background(), c.sc.host.ID)
		for _, s := range states {
			if s.Name == "dd-policy" && s.LastApply != nil {
				return 1
			}
		}
		return 0
	}
	lastAuditOutcome := func() any {
		rows := auditsOf(c, audit.ActionHostFileApply)
		var d map[string]any
		_ = json.Unmarshal(rows[len(rows)-1].Detail, &d)
		return d["outcome"]
	}
	var answers []envelope
	// No answer: timeout, audited, no apply row, the lock released.
	c.w.srv.hostFileWaitTest = 200 * time.Millisecond
	c.setReply(func(map[string]any) (map[string]any, time.Duration) { return nil, 0 })
	for range 2 {
		env := c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":1}`, http.StatusGatewayTimeout, "timeout")
		answers = append(answers, env)
		if lastAuditOutcome() != "timeout" || applies() != 0 {
			t.Fatalf("timeout audit %v applies %d", lastAuditOutcome(), applies())
		}
	}
	c.w.srv.hostFileWaitTest = 0
	type failCase struct {
		name      string
		frame     map[string]any
		code, msg string
		outcome   string
	}
	var cases []failCase
	for _, tc := range []failCase{
		{"unknown outcome", map[string]any{"type": "task_result", "state": "succeeded", "payload": json.RawMessage(`{"outcome":"exploded"}`)}, "error", msgApplyNoResult, ""},
		{"null payload", map[string]any{"type": "task_result", "state": "succeeded", "payload": nil}, "error", msgApplyNoResult, ""},
		{"failed with a secret code", map[string]any{"type": "task_result", "state": "failed", "error_code": fakeSecret}, "error", msgApplyNoResult, ""},
		{"rejected", map[string]any{"type": "task_reject", "error_code": "forbidden"}, "forbidden", msgApplyForbidden, ""},
	} {
		tc := tc
		tc.outcome = tc.code
		cases = append(cases, tc)
	}
	for code, msg := range helperRefusals {
		cases = append(cases, failCase{"helper " + code, map[string]any{"type": "task_result", "state": "failed", "error_code": code,
			"payload": json.RawMessage(`{"stage":"write","took_ms":3}`)}, "error", msg, code})
	}
	for _, tc := range cases {
		c.setReply(func(map[string]any) (map[string]any, time.Duration) { return tc.frame, 0 })
		status, env := c.w.do(c.ownerTok, http.MethodPost, c.base+"/dd-policy/apply", `{"version":1}`)
		if env.Code != tc.code || status != statusFor(tc.code) || env.Msg != tc.msg {
			t.Fatalf("%s: %d %s %q", tc.name, status, env.Code, env.Msg)
		}
		answers = append(answers, env)
		if applies() != 0 || lastAuditOutcome() != tc.outcome {
			t.Fatalf("%s: applies %d audit %v", tc.name, applies(), lastAuditOutcome())
		}
	}
	// The socket closes mid-task: host_offline.
	c.setReply(func(map[string]any) (map[string]any, time.Duration) {
		go c.conn.Close(websocket.StatusNormalClosure, "")
		return nil, 0
	})
	answers = append(answers, c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":1}`, http.StatusServiceUnavailable, "host_offline"))
	noSecretAnywhere(t, c, answers...)
}

func TestHostFilesApplyCallerLeaves(t *testing.T) {
	c := newHFCast(t, hostFilesPrograms())
	c.do(t, c.ownerTok, http.MethodPut, "/dd-policy", `{"content":"x\n"}`, http.StatusOK, "ok")
	c.setReply(func(map[string]any) (map[string]any, time.Duration) {
		return map[string]any{"type": "task_result", "state": "succeeded", "payload": json.RawMessage(`{"outcome":"succeeded","took_ms":5}`)}, 400 * time.Millisecond
	})
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.w.http.URL+c.base+"/dd-policy/apply", strings.NewReader(`{"version":1}`))
	req.Header.Set("Authorization", "Bearer "+c.ownerTok)
	go func() {
		hfWait(t, "task", func() bool { return len(c.taskSpecs()) == 1 })
		cancel()
	}()
	if res, err := c.w.client.Do(req); err == nil {
		res.Body.Close()
		t.Fatal("request was not cancelled")
	}
	hfWait(t, "apply row and audit", func() bool {
		states, _ := c.w.mem.HostFileStates(context.Background(), c.sc.host.ID)
		return len(states) == 1 && states[0].AppliedVersion != nil && len(auditsOf(c, audit.ActionHostFileApply)) == 1
	})
}

func TestHostFilesApplyConflict(t *testing.T) {
	c := newHFCast(t, hostFilesPrograms())
	c.do(t, c.ownerTok, http.MethodPut, "/dd-policy", `{"content":"x\n"}`, http.StatusOK, "ok")
	c.setReply(func(map[string]any) (map[string]any, time.Duration) {
		return map[string]any{"type": "task_result", "state": "succeeded", "payload": json.RawMessage(`{"outcome":"succeeded","took_ms":5}`)}, 700 * time.Millisecond
	})
	codes := make(chan string, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, env := c.w.do(c.ownerTok, http.MethodPost, c.base+"/dd-policy/apply", `{"version":1}`)
			codes <- env.Code
			if env.Code == "conflict" {
				p := objectPayload(t, env)
				if p["started_at"] == nil || p["ends_by"] == nil || !strings.Contains(env.Msg, "An apply of dd-policy on pi4 has been running since") {
					t.Errorf("conflict %s %v", env.Msg, p)
				}
			}
		}()
	}
	wg.Wait()
	close(codes)
	got := map[string]int{}
	for code := range codes {
		got[code]++
	}
	if got["ok"] != 1 || got["conflict"] != 1 || len(c.taskSpecs()) != 1 {
		t.Fatalf("codes %v tasks %d", got, len(c.taskSpecs()))
	}
}

func TestHostFilesBigApplyAndSecretFlag(t *testing.T) {
	c := newHFCast(t, hostFilesPrograms())
	content := strings.Repeat("0123456789abcdef", 4096) // 65536 bytes
	raw, _ := json.Marshal(map[string]string{"content": content})
	c.do(t, c.ownerTok, http.MethodPut, "/dd-policy", string(raw), http.StatusOK, "ok")
	c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":1}`, http.StatusOK, "ok")
	got, err := base64.StdEncoding.DecodeString(c.taskSpecs()[0]["content_b64"].(string))
	if err != nil || string(got) != content {
		t.Fatalf("64 KiB content differs (%d bytes)", len(got))
	}
	// The host now declares dd-policy as secret: the plain version is a conflict, never a 500.
	setPrograms(t, c.w, c.sc.host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{}, HostFiles: []store.HostFile{{Name: "dd-policy", Path: "/etc/dd-worker/policy.toml", Secret: true, Unit: "dd-worker.service"}}})
	env := c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":1}`, http.StatusConflict, "conflict")
	if env.Msg != msgSecretFlag {
		t.Fatalf("msg %q", env.Msg)
	}
	c.do(t, c.ownerTok, http.MethodPut, "/dd-policy", `{"base_version":1,"set":{"A":"b"}}`, http.StatusConflict, "conflict")
	c.do(t, c.ownerTok, http.MethodGet, "/dd-policy", "", http.StatusOK, "ok")
}

func TestHostFilesUnsupportedAndOffline(t *testing.T) {
	c := newHFCast(t, allowPrograms(false))
	if len(items(t, c.do(t, c.ownerTok, http.MethodGet, "", "", http.StatusOK, "ok"))) != 0 {
		t.Fatal("items without host_files")
	}
	c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":1}`, http.StatusConflict, "unsupported_agent")
	if len(c.taskSpecs()) != 0 {
		t.Fatal("task sent")
	}
	off := newHFCast(t, nil)
	setPrograms(t, off.w, off.sc.host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{}, HostFiles: []store.HostFile{{Name: "dd-policy", Path: "/etc/dd-worker/policy.toml", Unit: "dd-worker.service"}}})
	off.do(t, off.ownerTok, http.MethodPut, "/dd-policy", `{"content":"x\n"}`, http.StatusOK, "ok")
	off.do(t, off.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":1}`, http.StatusServiceUnavailable, "host_offline")
}

// waitStatus polls until the stored status has n files.
func waitStatus(t *testing.T, c *hfCast, n int) *store.HostFilesStatus {
	t.Helper()
	var st *store.HostFilesStatus
	hfWait(t, "status", func() bool {
		st, _ = c.w.mem.GetHostFilesStatus(context.Background(), c.sc.host.ID)
		return st != nil && len(st.Files) == n
	})
	return st
}

func TestHostFilesStatusFrame(t *testing.T) {
	c := newHFCast(t, hostFilesPrograms())
	sha := strings.Repeat("b", 64)
	writeFrame(t, c.conn, map[string]any{"v": 1, "type": "host_files_status", "files": []map[string]any{
		{"name": "dd-policy", "exists": true, "owner_ok": true, "matches_applied": true, "size": 10, "sha256": sha},
		{"name": "dd-secrets", "exists": true, "owner_ok": true, "matches_applied": false, "size": 99, "sha256": sha},
		{"name": "undeclared", "exists": true, "owner_ok": true, "matches_applied": true},
	}})
	st := waitStatus(t, c, 2)
	if st.Files[0].SHA256 != sha || st.Files[1].SHA256 != "" || st.Files[1].Size != nil || st.Files[1].BadSince == nil || st.Files[0].BadSince != nil {
		t.Fatalf("stored %+v", st.Files)
	}
	env := c.do(t, c.viewTok, http.MethodGet, "", "", http.StatusOK, "ok")
	if got := items(t, env); got[0]["status"] != "in_sync" || got[1]["status"] != "different" || strings.Contains(envJSON(env), sha) {
		t.Fatalf("items %v", got)
	}
	first := *st.Files[1].BadSince
	writeFrame(t, c.conn, map[string]any{"v": 1, "type": "host_files_status", "files": []map[string]any{
		{"name": "dd-secrets", "exists": false, "owner_ok": false, "matches_applied": nil},
	}})
	st = waitStatus(t, c, 1)
	if st.Files[0].BadSince == nil || !st.Files[0].BadSince.Equal(first) {
		t.Fatalf("bad since %+v", st.Files[0])
	}
	writeFrame(t, c.conn, map[string]any{"v": 1, "type": "host_files_status", "files": "nope"})
	writeFrame(t, c.conn, map[string]any{"v": 1, "type": "host_files_status", "files": []map[string]any{
		{"name": "dd-policy", "exists": true, "owner_ok": true, "matches_applied": true},
		{"name": "dd-secrets", "exists": true, "owner_ok": true, "matches_applied": true},
	}})
	if st = waitStatus(t, c, 2); st.Files[1].BadSince != nil {
		t.Fatalf("in sync keeps bad_since %+v", st.Files[1])
	}
	// A status older than 25 minutes reads as unknown.
	tr := true
	if err := c.w.mem.SetHostFilesStatus(context.Background(), c.sc.host.ID, store.HostFilesStatus{At: c.w.now.Add(-26 * time.Minute), Files: []store.HostFileStatus{{Name: "dd-policy", Exists: true, OwnerOK: true, MatchesApplied: &tr}}}); err != nil {
		t.Fatal(err)
	}
	if got := items(t, c.do(t, c.viewTok, http.MethodGet, "", "", http.StatusOK, "ok")); got[0]["status"] != "unknown" {
		t.Fatalf("stale %v", got[0])
	}
	// Status words.
	fa := false
	for _, tc := range []struct {
		f    store.HostFileStatus
		want string
	}{
		{store.HostFileStatus{Exists: false}, "missing"},
		{store.HostFileStatus{Exists: true, OwnerOK: false, MatchesApplied: &tr}, "wrong_owner"},
		{store.HostFileStatus{Exists: true, OwnerOK: true}, "unknown"},
		{store.HostFileStatus{Exists: true, OwnerOK: true, MatchesApplied: &tr}, "in_sync"},
		{store.HostFileStatus{Exists: true, OwnerOK: true, MatchesApplied: &fa}, "different"},
	} {
		if got := tc.f.Word(); got != tc.want {
			t.Fatalf("%+v: %s want %s", tc.f, got, tc.want)
		}
	}
}

func TestHostFilesProblems(t *testing.T) {
	c := newHFCast(t, hostFilesPrograms())
	ctx := context.Background()
	c.do(t, c.ownerTok, http.MethodPut, "/dd-policy", `{"content":"x\n"}`, http.StatusOK, "ok")
	open := func() []store.Problem {
		t.Helper()
		rows, err := c.w.mem.ListProblems(ctx, store.ProblemFilter{All: true, Kind: store.ProblemHostFile})
		if err != nil {
			t.Fatal(err)
		}
		var out []store.Problem
		for _, p := range rows {
			if p.ResolvedAt == nil {
				out = append(out, p)
			}
		}
		return out
	}
	apply := func(outcome string) {
		t.Helper()
		c.setReply(succeed(`{"outcome":"` + outcome + `","stage":"health","reason":"not_running","took_ms":5}`))
		c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":1}`, http.StatusOK, "ok")
	}
	setStatus := func(at time.Time, files ...store.HostFileStatus) {
		t.Helper()
		if err := c.w.mem.SetHostFilesStatus(ctx, c.sc.host.ID, store.HostFilesStatus{At: at, Files: files}); err != nil {
			t.Fatal(err)
		}
	}
	tr, fa := true, false
	inSync := store.HostFileStatus{Name: "dd-policy", Exists: true, OwnerOK: true, MatchesApplied: &tr}

	apply(store.HostFileRollbackFailed)
	if got := open(); len(got) != 1 || got[0].Level != store.ProblemNeedsAction || got[0].Subject != c.sc.host.ID.String()+":dd-policy" {
		t.Fatalf("rollback_failed problem %+v", got)
	}
	// Org owners and admins see it with the file link; others do not.
	wantHref := "/hosts/" + c.sc.host.ID.String() + "?tab=files&files.name=dd-policy"
	for _, tok := range []string{c.ownerTok, c.adminTok} {
		status, env := c.w.do(tok, http.MethodGet, "/v1/orgs/"+c.sc.project.OrgID.String()+"/problems", "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		got := items(t, env)
		if len(got) != 1 || got[0]["kind"] != "host_file" || got[0]["href"] != wantHref {
			t.Fatalf("org problems %v", got)
		}
		u, err := url.Parse(got[0]["href"].(string))
		if err != nil || u.Query().Get("tab") != "files" || u.Query().Get("files.name") != "dd-policy" {
			t.Fatalf("href %v", got[0]["href"])
		}
	}
	status, env := c.w.do(c.memberTok, http.MethodGet, "/v1/orgs/"+c.sc.project.OrgID.String()+"/problems", "")
	if status != http.StatusForbidden || strings.Contains(envJSON(env), "host_file") {
		t.Fatalf("org member %d %s", status, envJSON(env))
	}
	for _, tok := range []string{c.projTok, c.viewTok} {
		_, env := c.w.do(tok, http.MethodGet, "/v1/projects/"+c.sc.project.ID.String()+"/problems", "")
		if strings.Contains(envJSON(env), "host_file") {
			t.Fatalf("project member sees it: %s", envJSON(env))
		}
	}
	// The sweep never resolves it, a check_failed apply does not, unchanged does.
	setStatus(c.w.now, inSync)
	c.w.srv.sweepHostFiles(ctx, c.w.now)
	apply(store.HostFileCheckFailed)
	if got := open(); len(got) != 1 || got[0].Level != store.ProblemNeedsAction {
		t.Fatalf("resolved early %+v", got)
	}
	apply(store.HostFileUnchanged)
	if len(open()) != 0 {
		t.Fatal("unchanged did not resolve")
	}
	// Drift: only after 30 minutes, for an applied, declared file, on a fresh status.
	since := c.w.now.Add(-29 * time.Minute)
	drift := func() {
		setStatus(c.w.now,
			store.HostFileStatus{Name: "dd-policy", Exists: true, OwnerOK: true, MatchesApplied: &fa, BadSince: &since},
			store.HostFileStatus{Name: "dd-secrets", Exists: false, BadSince: &since},
			store.HostFileStatus{Name: "gone", Exists: false, BadSince: &since})
	}
	drift()
	c.w.srv.sweepHostFiles(ctx, c.w.now)
	if len(open()) != 0 {
		t.Fatal("opened before 30 minutes")
	}
	since = c.w.now.Add(-31 * time.Minute)
	drift()
	c.w.srv.sweepHostFiles(ctx, c.w.now)
	got := open()
	if len(got) != 1 || got[0].Level != store.ProblemHeadsUp || got[0].Title != "dd-policy on pi4 differs from the applied version" {
		t.Fatalf("drift %+v", got)
	}
	// A stale status neither resolves nor opens.
	setStatus(c.w.now.Add(-26*time.Minute), inSync)
	c.w.srv.sweepHostFiles(ctx, c.w.now)
	if len(open()) != 1 {
		t.Fatal("stale status resolved the drift")
	}
	setStatus(c.w.now, inSync)
	c.w.srv.sweepHostFiles(ctx, c.w.now)
	if len(open()) != 0 {
		t.Fatal("drift not resolved")
	}
	// Drift on a file the host no longer declares resolves.
	drift()
	c.w.srv.sweepHostFiles(ctx, c.w.now)
	setPrograms(t, c.w, c.sc.host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{}, HostFiles: []store.HostFile{}})
	c.w.srv.sweepHostFiles(ctx, c.w.now)
	if len(open()) != 0 {
		t.Fatal("undeclared file drift still open")
	}
}

// Spec amendment A1: one apply per host and unit. dd-policy and dd-secrets
// both restart dd-worker.service, so the second is a conflict naming the
// running file; files on different units run side by side.
func TestHostFilesApplyLockPerUnit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		secretUnit string
		want       map[string]int
	}{
		{"same unit", "dd-worker.service", map[string]int{"ok": 1, "conflict": 1}},
		{"different units", "other.service", map[string]int{"ok": 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			programs := hostFilesPrograms()
			files := programs["host_files"].([]map[string]any)
			files[1]["unit"] = tc.secretUnit
			c := newHFCast(t, programs)
			c.do(t, c.ownerTok, http.MethodPut, "/dd-policy", `{"content":"x\n"}`, http.StatusOK, "ok")
			c.do(t, c.ownerTok, http.MethodPut, "/dd-secrets", `{"base_version":0,"set":{"A":"b"}}`, http.StatusOK, "ok")
			c.setReply(func(map[string]any) (map[string]any, time.Duration) {
				return map[string]any{"type": "task_result", "state": "succeeded", "payload": json.RawMessage(`{"outcome":"succeeded","took_ms":5}`)}, 500 * time.Millisecond
			})
			results := make(chan envelope, 2)
			go func() {
				_, env := c.w.do(c.ownerTok, http.MethodPost, c.base+"/dd-policy/apply", `{"version":1}`)
				results <- env
			}()
			hfWait(t, "first task", func() bool { return len(c.taskSpecs()) == 1 })
			go func() {
				_, env := c.w.do(c.ownerTok, http.MethodPost, c.base+"/dd-secrets/apply", `{"version":1}`)
				results <- env
			}()
			got := map[string]int{}
			for range 2 {
				env := <-results
				got[env.Code]++
				if env.Code == "conflict" && !strings.Contains(env.Msg, "An apply of dd-policy on pi4 has been running since") {
					t.Fatalf("conflict text %q", env.Msg)
				}
			}
			if len(got) != len(tc.want) || got["ok"] != tc.want["ok"] || got["conflict"] != tc.want["conflict"] {
				t.Fatalf("codes %v", got)
			}
		})
	}
}

// Spec amendment A5: status reasons decide the word; unknown reasons and
// stage tokens are dropped.
func TestHostFilesReasonsAndStages(t *testing.T) {
	tr := true
	for reason, want := range map[string]string{
		"missing": "missing", "symlink": "wrong_owner", "not_regular": "wrong_owner", "owner": "wrong_owner",
		"mode": "wrong_owner", "nlink": "wrong_owner", "dir": "wrong_owner", "too_large": "unknown", "io_error": "unknown",
	} {
		f := store.HostFileStatus{Name: "x", Exists: true, OwnerOK: true, MatchesApplied: &tr, Reason: reason}
		if got := f.Word(); got != want || !store.ValidHostFileStatusReason(reason) {
			t.Fatalf("%s: %s want %s", reason, got, want)
		}
	}
	if store.ValidHostFileStatusReason("weird") {
		t.Fatal("unknown reason accepted")
	}
	c := newHFCast(t, hostFilesPrograms())
	writeFrame(t, c.conn, map[string]any{"v": 1, "type": "host_files_status", "files": []map[string]any{
		{"name": "dd-policy", "exists": true, "owner_ok": false, "reason": "too_large", "matches_applied": nil},
		{"name": "dd-secrets", "exists": true, "owner_ok": true, "reason": "weird", "matches_applied": true},
	}})
	st := waitStatus(t, c, 2)
	if st.Files[0].Reason != "too_large" || st.Files[0].Word() != "unknown" || st.Files[1].Reason != "" || st.Files[1].Word() != "in_sync" {
		t.Fatalf("stored %+v", st.Files)
	}
	for stage, kept := range map[string]bool{"lookup": true, "settle": true, "exec": true, "target": true, "check": true, "idle": true,
		"restart": true, "health": true, "rollback": true, "write": false, "Exec": false} {
		res, ok := parseApplyResult(json.RawMessage(`{"outcome":"forbidden_spec","stage":"` + stage + `","took_ms":1}`))
		if !ok || (res.Stage != nil) != kept {
			t.Fatalf("stage %s kept %v", stage, res.Stage != nil)
		}
	}
}

// failGetHost fails GetHost for one host with a database error.
type failGetHost struct {
	store.Store
	host string
}

func (f failGetHost) GetHost(ctx context.Context, id uuid.UUID) (store.Host, error) {
	if id.String() == f.host {
		return store.Host{}, errors.New("database: down")
	}
	return f.Store.GetHost(ctx, id)
}

// Re-review R3 and R4: the sweep's leftover pass.
func TestHostFilesSweepLeftovers(t *testing.T) {
	c := newHFCast(t, hostFilesPrograms())
	ctx := context.Background()
	open := func() []store.Problem {
		t.Helper()
		rows, _ := c.w.mem.ListProblems(ctx, store.ProblemFilter{All: true, Kind: store.ProblemHostFile})
		var out []store.Problem
		for _, p := range rows {
			if p.ResolvedAt == nil {
				out = append(out, p)
			}
		}
		return out
	}
	c.do(t, c.ownerTok, http.MethodPut, "/dd-policy", `{"content":"x\n"}`, http.StatusOK, "ok")
	c.setReply(succeed(`{"outcome":"rollback_failed","stage":"rollback","took_ms":5}`))
	c.do(t, c.ownerTok, http.MethodPost, "/dd-policy/apply", `{"version":1}`, http.StatusOK, "ok")
	tr := true
	if err := c.w.mem.SetHostFilesStatus(ctx, c.sc.host.ID, store.HostFilesStatus{At: c.w.now, Files: []store.HostFileStatus{
		{Name: "dd-policy", Exists: true, OwnerOK: true, MatchesApplied: &tr},
	}}); err != nil {
		t.Fatal(err)
	}
	// A live, declared file: the needs_action row stays, with or without a status for it.
	c.w.srv.sweepHostFiles(ctx, c.w.now)
	if err := c.w.mem.SetHostFilesStatus(ctx, c.sc.host.ID, store.HostFilesStatus{At: c.w.now, Files: []store.HostFileStatus{}}); err != nil {
		t.Fatal(err)
	}
	c.w.srv.sweepHostFiles(ctx, c.w.now)
	if got := open(); len(got) != 1 || got[0].Level != store.ProblemNeedsAction {
		t.Fatalf("live declared %+v", got)
	}
	// R3: a host that cannot be read keeps its rows.
	c.w.srv.Store = failGetHost{Store: c.w.mem, host: c.sc.host.ID.String()}
	c.w.srv.sweepHostFiles(ctx, c.w.now)
	c.w.srv.Store = c.w.mem
	if len(open()) != 1 {
		t.Fatal("unreadable host resolved its row")
	}
	// R4: the host no longer declares the file: needs_action resolves too.
	setPrograms(t, c.w, c.sc.host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{}, HostFiles: []store.HostFile{
		{Name: "dd-secrets", Path: "/etc/dd-worker/secrets.env", Secret: true, Unit: "dd-worker.service"}}})
	c.w.srv.sweepHostFiles(ctx, c.w.now)
	if len(open()) != 0 {
		t.Fatal("undeclared file kept needs_action")
	}
	// R4: a revoked host resolves both levels.
	hid := c.sc.host.ID
	for _, level := range []string{store.ProblemNeedsAction, store.ProblemHeadsUp} {
		if _, err := c.w.mem.OpenProblem(ctx, store.ProblemOpen{Kind: store.ProblemHostFile, Subject: hid.String() + ":dd-secrets-" + level, OrgID: c.sc.project.OrgID, HostID: &hid, Level: level, Title: "t"}, c.w.now); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.w.mem.RevokeHost(ctx, hid, c.w.now); err != nil {
		t.Fatal(err)
	}
	c.w.srv.sweepHostFiles(ctx, c.w.now)
	if got := open(); len(got) != 0 {
		t.Fatalf("revoked host kept %+v", got)
	}
}
