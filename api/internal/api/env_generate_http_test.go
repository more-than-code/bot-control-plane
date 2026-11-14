package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// genKey PUTs a key with a generate object.
func genKey(t *testing.T, w *world, tok, project, set, key, generate string) (int, envelope, string) {
	t.Helper()
	return mustCall(t, w, tok, http.MethodPut, envKeyPath(project, set, key), `{"generate":`+generate+`}`)
}

func revealPath(project, set, key string) string {
	return envKeyPath(project, set, key) + "/reveal"
}

// revealRaw posts a reveal and returns the status, the Cache-Control header,
// and the decoded envelope.
func revealRaw(t *testing.T, w *world, tok, project, set, key string) (int, string, envelope, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, w.http.URL+revealPath(project, set, key), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf strings.Builder
	var env envelope
	dec := json.NewDecoder(resp.Body)
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		t.Fatal(err)
	}
	buf.Write(raw)
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header.Get("Cache-Control"), env, buf.String()
}

// reveal returns the value from a successful reveal.
func reveal(t *testing.T, w *world, tok, project, set, key string) string {
	t.Helper()
	status, cache, env, _ := revealRaw(t, w, tok, project, set, key)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if cache != "no-store" {
		t.Fatalf("Cache-Control %q", cache)
	}
	body := objectPayload(t, env)
	if body["key"] != key || len(body) != 2 {
		t.Fatalf("reveal body keys %v", mapKeys(body))
	}
	value, _ := body["value"].(string)
	return value
}

func revealConflictMsg(key string) string {
	return "the value of " + key + " was already shown or was not generated; edit it to set a new value"
}

// bullets is the four-bullet preview (U+2022).
const bullets = "\u2022\u2022\u2022\u2022"

func assertFlags(t *testing.T, row map[string]any, pending, needs bool) {
	t.Helper()
	if row["reveal_pending"] != pending || row["needs_value"] != needs {
		t.Fatalf("flags %#v want pending=%v needs=%v", row, pending, needs)
	}
}

// TestEnvKeyGenerate is spec §6 item 1: each kind gives the stated shape, the
// key is secret and pending, and the value is in no response other than the
// reveal, no log line, and no audit row.
func TestEnvKeyGenerate(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	logs := captureLog(w)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()

	cases := []struct {
		key, gen, kind string
		re             *regexp.Regexp
	}{
		{"PW", `{"kind":"password"}`, "password", regexp.MustCompile(`^[A-Za-z0-9]{32}$`)},
		{"PW20", `{"kind":"password","length":20}`, "password", regexp.MustCompile(`^[A-Za-z0-9]{20}$`)},
		{"HEX", `{"kind":"hex"}`, "hex", regexp.MustCompile(`^[0-9a-f]{64}$`)},
		{"B64", `{"kind":"base64url"}`, "base64url", regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)},
	}
	var bodies []string
	values := map[string]string{}
	for _, tc := range cases {
		status, env, raw := genKey(t, w, tok, project, "gen", tc.key, tc.gen)
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		row := objectPayload(t, env)
		// A generated value's preview is always four bullets (v1.27 item 2).
		assertSecretKey(t, row, bullets)
		if row["key"] != tc.key {
			t.Fatalf("put %#v", row)
		}
		assertFlags(t, row, true, false)
		bodies = append(bodies, raw)
		status, env, raw = mustCall(t, w, tok, http.MethodGet, envPath(project, "gen"), "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		assertFlags(t, keysByName(t, env)[tc.key], true, false)
		assertSecretKey(t, keysByName(t, env)[tc.key], bullets)
		bodies = append(bodies, raw)
		status, env, raw = mustCall(t, w, tok, http.MethodGet, "/v1/projects/"+project+"/env-sets", "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		bodies = append(bodies, raw)

		value := reveal(t, w, tok, project, "gen", tc.key)
		if !tc.re.MatchString(value) {
			t.Fatalf("%s value has the wrong shape (length %d)", tc.key, len(value))
		}
		values[tc.key] = value
		// After the reveal the preview stays four bullets.
		status, env, raw = mustCall(t, w, tok, http.MethodGet, envPath(project, "gen"), "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		assertFlags(t, keysByName(t, env)[tc.key], false, false)
		assertSecretKey(t, keysByName(t, env)[tc.key], bullets)
		bodies = append(bodies, raw)
		status, env, raw = mustCall(t, w, tok, http.MethodGet, "/v1/projects/"+project+"/env-sets", "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		bodies = append(bodies, raw)
	}
	blob := strings.Join(bodies, "\n") + auditBlob(t, w, sc.project.ID) + logs.String()
	for key, value := range values {
		// Neither the value nor its first four characters (the v1.13
		// preview) appear anywhere but in the reveal.
		if strings.Contains(blob, value) || strings.Contains(blob, catalog.SecretPreview(value)) {
			t.Fatalf("%s value leaked into a response, the audit, or the log", key)
		}
	}

	// A typed value returns the key to the normal preview rule.
	const typed = "typed-secret-value-0123"
	status, env, _ := putKey(t, w, tok, project, "gen", "PW", typed, nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	assertSecretKey(t, objectPayload(t, env), catalog.SecretPreview(typed))
	status, env, _ = mustCall(t, w, tok, http.MethodGet, envPath(project, "gen"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	assertSecretKey(t, keysByName(t, env)["PW"], catalog.SecretPreview(typed))
	assertSecretKey(t, keysByName(t, env)["HEX"], bullets)
	kinds := map[string]string{}
	for _, row := range auditRows(t, w, sc.project.ID) {
		if row.Action != audit.ActionEnvSetKeyPut {
			continue
		}
		detail := detailMap(t, row.Detail)
		if _, ok := detail["generated"]; !ok {
			continue // the typed edit at the end
		}
		if len(detail) != 4 || detail["set"] != "gen" || detail["created"] != true {
			t.Fatalf("key_put detail %#v", detail)
		}
		kinds[detail["key"].(string)], _ = detail["generated"].(string)
	}
	for _, tc := range cases {
		if kinds[tc.key] != tc.kind {
			t.Fatalf("audit generated %v", kinds)
		}
	}
	reveals := 0
	for _, row := range auditRows(t, w, sc.project.ID) {
		if row.Action == audit.ActionEnvSetKeyReveal {
			detail := detailMap(t, row.Detail)
			if len(detail) != 2 || detail["set"] != "gen" {
				t.Fatalf("reveal detail %#v", detail)
			}
			reveals++
		}
	}
	if reveals != len(cases) {
		t.Fatalf("reveal audits %d", reveals)
	}
}

func TestEnvKeyGenerateValidation(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	putEnvFlag(t, w, tok, project, "svc", "PLAIN=visible\n", false)

	for _, tc := range []struct {
		key, body, msg string
	}{
		{"NEW", `{"value":"x","generate":{"kind":"hex"}}`, "send value or generate, not both"},
		{"NEW", `{"value":"","generate":{"kind":"hex"}}`, "send value or generate, not both"},
		{"NEW", `{"generate":{"kind":"uuid"}}`, `generate.kind must be "password", "hex" or "base64url"`},
		{"NEW", `{"generate":{}}`, `generate.kind must be "password", "hex" or "base64url"`},
		{"NEW", `{"generate":{"kind":"password","length":15}}`, "generate.length must be 16 to 64"},
		{"NEW", `{"generate":{"kind":"password","length":65}}`, "generate.length must be 16 to 64"},
		{"NEW", `{"generate":{"kind":"hex","length":32}}`, "generate.length is only for kind password"},
		{"NEW", `{"generate":{"kind":"hex"},"is_secret":false}`, "a generated value is secret"},
		{"NEW", `{"generate":{"kind":"hex","extra":1}}`, "invalid json"},
		{"PLAIN", `{"generate":{"kind":"password"}}`, "PLAIN already has a value; edit it to change it"},
	} {
		status, env, _ := mustCall(t, w, tok, http.MethodPut, envKeyPath(project, "svc", tc.key), tc.body)
		mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", tc.msg)
	}
	status, env, _ := mustCall(t, w, tok, http.MethodGet, envPath(project, "svc"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	keys := keysByName(t, env)
	if len(keys) != 1 {
		t.Fatalf("refusals wrote keys %v", keys)
	}
	assertPlainKey(t, keys["PLAIN"], "visible")
	assertFlags(t, keys["PLAIN"], false, false)
	// is_secret true with generate is allowed.
	status, env, _ = mustCall(t, w, tok, http.MethodPut, envKeyPath(project, "svc", "OK"), `{"generate":{"kind":"hex"},"is_secret":true}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	assertFlags(t, objectPayload(t, env), true, false)
}

// TestEnvKeyReveal is spec §6 item 2.
func TestEnvKeyReveal(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	admin := w.user("a@ex.com", "A")
	viewer := w.user("v@ex.com", "V")
	sc := w.scene(owner, admin, viewer, w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	genKey(t, w, tok, project, "svc", "PW", `{"kind":"password"}`)

	// Delegated sessions and viewers are refused and the flag stays.
	delegated := tokenKind(t, w, owner.ID, store.KindDelegated)
	status, _, env, _ := revealRaw(t, w, delegated, project, "svc", "PW")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	status, _, env, _ = revealRaw(t, w, w.token(viewer.ID), project, "svc", "PW")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	if n := countAction(w.mem.AllAudits(), audit.ActionEnvSetKeyReveal); n != 0 {
		t.Fatalf("refused reveal audited %d", n)
	}

	// An admin may reveal; once.
	first := reveal(t, w, w.token(admin.ID), project, "svc", "PW")
	status, _, env, raw := revealRaw(t, w, tok, project, "svc", "PW")
	mustHTTP(t, status, env, http.StatusConflict, "conflict", revealConflictMsg("PW"))
	if strings.Contains(raw, first) {
		t.Fatal("conflict echoed the value")
	}

	// Unknown set or key.
	status, _, env, _ = revealRaw(t, w, tok, project, "nope", "PW")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, _, env, _ = revealRaw(t, w, tok, project, "svc", "GONE")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, _, env, _ = revealRaw(t, w, tok, project, "svc", "bad-key")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid env key")

	// A typed value is never revealable.
	putKey(t, w, tok, project, "svc", "TYPED", "typed-secret-value-123", nil)
	status, _, env, _ = revealRaw(t, w, tok, project, "svc", "TYPED")
	mustHTTP(t, status, env, http.StatusConflict, "conflict", revealConflictMsg("TYPED"))

	// Each other value write clears reveal_pending: key edit, merge, replace.
	clearers := []struct {
		name string
		do   func()
	}{
		{"edit", func() { putKey(t, w, tok, project, "svc", "PW", "typed-over-generated", nil) }},
		{"merge", func() {
			status, env, _ := mustCall(t, w, tok, http.MethodPut, envPath(project, "svc"), `{"dotenv":"PW=merged-value\n","mode":"merge"}`)
			mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
			assertFlags(t, keysByName(t, env)["PW"], false, false)
		}},
		{"replace", func() {
			status, env, _ := mustCall(t, w, tok, http.MethodPut, envPath(project, "svc"), `{"dotenv":"PW=replaced-value\nTYPED=x\n"}`)
			mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
			assertFlags(t, keysByName(t, env)["PW"], false, false)
		}},
	}
	for _, c := range clearers {
		// Generate makes new keys only, so drop PW first.
		status, env, _ := mustCall(t, w, tok, http.MethodDelete, envKeyPath(project, "svc", "PW"), "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		status, env, _ = genKey(t, w, tok, project, "svc", "PW", `{"kind":"hex"}`)
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		c.do()
		status, _, env, _ = revealRaw(t, w, tok, project, "svc", "PW")
		mustHTTP(t, status, env, http.StatusConflict, "conflict", revealConflictMsg("PW"))
	}
}

// TestEnvKeyRevealConcurrent: many reveals at once give exactly one 200, and
// that body holds a valid generated value. This exercises the memstore
// mutex; single use in Postgres is pinned by the db test assertEnvGenerate.
func TestEnvKeyRevealConcurrent(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	genKey(t, w, tok, project, "svc", "PW", `{"kind":"password"}`)
	const n = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, n)
	values := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			status, env, _, err := callRaw(w, tok, http.MethodPost, revealPath(project, "svc", "PW"), "")
			if err != nil {
				codes[i] = -1
				return
			}
			codes[i] = status
			if body, ok := env.Payload.(map[string]any); ok {
				values[i], _ = body["value"].(string)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	ok, conflict := 0, 0
	for i, code := range codes {
		switch code {
		case http.StatusOK:
			ok++
			if !regexp.MustCompile(`^[A-Za-z0-9]{32}$`).MatchString(values[i]) {
				t.Fatalf("the 200 body has no valid value (length %d)", len(values[i]))
			}
		case http.StatusConflict:
			conflict++
			if values[i] != "" {
				t.Fatal("a conflict carried a value")
			}
		}
	}
	if ok != 1 || conflict != n-1 {
		t.Fatalf("codes %v", codes)
	}
}

// TestEnvKeyRevealNoSecretsKey: without the secrets key a reveal is
// unavailable and the value stays pending for a later reveal.
func TestEnvKeyRevealNoSecretsKey(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	genKey(t, w, tok, project, "svc", "PW", `{"kind":"hex"}`)
	box := w.srv.Secrets
	w.srv.Secrets = nil
	status, _, env, _ := revealRaw(t, w, tok, project, "svc", "PW")
	mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", "secrets key not configured")
	set, err := w.mem.GetEnvSet(t.Context(), sc.project.ID, "svc")
	if err != nil || !set.Keys[0].RevealPending {
		t.Fatalf("flag cleared without a reveal %v", err)
	}
	w.srv.Secrets = box
	if v := reveal(t, w, tok, project, "svc", "PW"); len(v) != 64 {
		t.Fatal("later reveal failed")
	}
}

// TestEnvKeyGenerateExisting is v1.27 item 1 rev 3: there is no
// regenerate. Generate on an existing secret or plain key is refused, and
// the value, preview, flags, spec hash, and audit are unchanged.
func TestEnvKeyGenerateExisting(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	genKey(t, w, tok, project, "svc", "PW", `{"kind":"password"}`)
	putKey(t, w, tok, project, "svc", "TYPED", "typed-secret-value-0123", nil)
	putKey(t, w, tok, project, "svc", "PLAIN", "visible", boolPtr(false))
	created := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","env_sets":["svc"]}`)
	hash, _ := created["spec_hash"].(string)

	snapshot := func() string {
		t.Helper()
		status, env, raw := mustCall(t, w, tok, http.MethodGet, envPath(project, "svc"), "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		mats, err := w.mem.ListEnvMaterial(t.Context(), sc.project.ID, "svc")
		if err != nil {
			t.Fatal(err)
		}
		cipher := ""
		for _, m := range mats {
			cipher += m.Key + ":" + string(m.Ciphertext) + m.Plain + ";"
		}
		return raw + cipher
	}
	before := snapshot()
	puts := countAction(w.mem.AllAudits(), audit.ActionEnvSetKeyPut)
	for _, key := range []string{"PW", "TYPED", "PLAIN"} {
		for _, gen := range []string{`{"kind":"password"}`, `{"kind":"hex"}`} {
			status, env, _ := genKey(t, w, tok, project, "svc", key, gen)
			mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", key+" already has a value; edit it to change it")
		}
	}
	if snapshot() != before {
		t.Fatal("a refused generate changed the set")
	}
	if n := countAction(w.mem.AllAudits(), audit.ActionEnvSetKeyPut); n != puts {
		t.Fatalf("refused generates audited: %d key_put rows, want %d", n, puts)
	}
	if createImageGet(t, w, tok, sc.project.ID, "web")["spec_hash"] != hash {
		t.Fatal("a refused generate changed the spec hash")
	}
	// The original generated value is still pending and revealable.
	if v := reveal(t, w, tok, project, "svc", "PW"); len(v) != 32 {
		t.Fatal("original value lost")
	}
	// A typed edit is how a value is replaced.
	status, env, _ := putKey(t, w, tok, project, "svc", "PW", "edited-secret-value-0123", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	assertSecretKey(t, objectPayload(t, env), catalog.SecretPreview("edited-secret-value-0123"))
}

// refsWorld is a world with an agent on the scene host and an env set
// holding a secret password and a plain host name. The fake agent is leaky:
// every deploy, rollback, and logs reply echoes each env value of the last
// deploy or rollback task, so every test also checks redaction.
func refsWorld(t *testing.T, programs map[string]any) (*world, refsScene, string, *taskLog) {
	t.Helper()
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	logs := captureLog(w)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	putEnv(t, w, tok, project, "db", "POSTGRES_PASSWORD="+refPassword+"\n")
	putEnvFlag(t, w, tok, project, "plain", "DB_HOST=postgres\nLITERAL=$${NOT_A_REF}\n", false)
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, programs, nil)
	var mu sync.Mutex
	var echo []string
	log := watchTasks(t, conn, func(msg map[string]any) map[string]any {
		mu.Lock()
		defer mu.Unlock()
		if spec, ok := msg["spec"].(map[string]any); ok {
			if envMap, ok := spec["env"].(map[string]any); ok {
				echo = echo[:0]
				for k, v := range envMap {
					echo = append(echo, fmt.Sprintf("env %s=%v", k, v))
				}
			}
		}
		lines := append([]string{"pulled"}, echo...)
		if msg["kind"] == "image_logs" {
			return map[string]any{
				"v": 1, "type": "task_result", "task_id": msg["task_id"], "state": "succeeded",
				"payload": map[string]any{"container": "web", "lines": lines, "truncated": false},
			}
		}
		reply := imageDeployReply(msg, shaDigest(7))
		reply["payload"].(map[string]any)["lines"] = lines
		return reply
	})
	return w, refsScene{scene: sc, owner: owner.ID, logs: logs}, tok, log
}

const (
	refPassword = "pg-secret-password-1"
	refURL      = "postgres://app:" + refPassword + "@postgres:5432/app"
)

type refsScene struct {
	scene
	owner uuid.UUID
	logs  interface{ String() string }
}

// lastSpec is the spec of the newest task the agent received.
func lastSpec(t *testing.T, log *taskLog) map[string]any {
	t.Helper()
	if log.count() == 0 {
		t.Fatal("no task")
	}
	spec, _ := log.raw(log.count() - 1)["spec"].(map[string]any)
	return spec
}

// assertNoRefLeak fails when the password, the resolved URL, or the
// password's preview is in blob.
func assertNoRefLeak(t *testing.T, label, blob string) {
	t.Helper()
	for _, v := range []string{refPassword, refURL, catalog.SecretPreview(refPassword)} {
		if strings.Contains(blob, v) {
			t.Fatalf("%s holds a secret form", label)
		}
	}
}

// TestEnvReferences is spec §6 item 6 over HTTP.
func TestEnvReferences(t *testing.T) {
	programs := allowImages()
	programs["files"] = true
	w, sc, tok, log := refsWorld(t, programs)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","env_sets":["db","plain"],"env":{"DATABASE_URL":"postgres://app:${POSTGRES_PASSWORD}@${DB_HOST}:5432/app","HOST_COPY":"${DB_HOST}"}}`)

	status, env, raw := postImage(t, w, tok, project, "web", "deploy", host, "ref-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	assertNoRefLeak(t, "deploy response", raw)
	spec := lastSpec(t, log)
	envMap, _ := spec["env"].(map[string]any)
	if envMap["DATABASE_URL"] != refURL || envMap["HOST_COPY"] != "postgres" || envMap["LITERAL"] != "${NOT_A_REF}" {
		t.Fatalf("resolved env %v", mapKeys(envMap))
	}
	if got := strings.Join(stringList(spec["secret_keys"]), ","); got != "DATABASE_URL,POSTGRES_PASSWORD" {
		t.Fatalf("secret_keys %q", got)
	}
	// The echoed plain values stay visible.
	if !strings.Contains(raw, "HOST_COPY=postgres") {
		t.Fatal("plain value redacted")
	}
	// A replay answers from the stored body, which holds no secret either.
	status, env, raw = postImage(t, w, tok, project, "web", "deploy", host, "ref-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	assertNoRefLeak(t, "replayed deploy response", raw)
	// Logs echo the resolved URL too, and are redacted through redactValues.
	status, env, raw = postImage(t, w, tok, project, "web", "logs", host, "", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	assertNoRefLeak(t, "logs response", raw)
	if !strings.Contains(raw, "HOST_COPY=postgres") {
		t.Fatal("logs redacted a plain value")
	}
	status, env, raw = mustCall(t, w, tok, http.MethodGet, imagePath(project, "web", "deploys")+"?host_id="+host, "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	assertNoRefLeak(t, "deploys list", raw)

	// The stored spec in the frame and the catalog entry keep the unresolved text.
	nested, _ := spec["spec"].(map[string]any)
	nestedEnv, _ := nested["env"].(map[string]any)
	if nestedEnv["DATABASE_URL"] != "postgres://app:${POSTGRES_PASSWORD}@${DB_HOST}:5432/app" {
		t.Fatalf("stored spec %#v", nestedEnv)
	}
	if strings.Contains(fmt.Sprint(createImageGet(t, w, tok, sc.project.ID, "web")), refPassword) {
		t.Fatal("catalog entry resolved a reference")
	}

	// A secret key holding the unknown reference: the message names no NAME.
	putEnv(t, w, tok, project, "secretref", "SECRET_URL=postgres://${HIDDEN_HOST}/x\n")
	cases := []struct {
		name, spec, msg string
	}{
		{"unknown", `{"kind":"service","image":"nginx","env_sets":["db"],"env":{"DATABASE_URL":"x${SMTP_HOST}"}}`, "env DATABASE_URL references ${SMTP_HOST}, which is not set for this entry"},
		{"unknown in secret", `{"kind":"service","image":"nginx","env_sets":["secretref"]}`, "env SECRET_URL has a reference that is not set for this entry"},
		{"unterminated", `{"kind":"service","image":"nginx","env":{"A":"x${B"}}`, "env A has an unterminated ${"},
		{"long", `{"kind":"service","image":"nginx","env_sets":["db"],"env":{"FILLER":"` + strings.Repeat("f", 4090) + `","BIG":"${FILLER}${POSTGRES_PASSWORD}"}}`, "env BIG is longer than 4096 bytes after references"},
	}
	for i, tc := range cases {
		name := fmt.Sprintf("bad%d", i)
		createImage(t, w, tok, sc.project.ID, name, tc.spec)
		before := log.count()
		status, env, raw := postImage(t, w, tok, project, name, "deploy", host, "bad-"+name, nil)
		mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", tc.msg)
		if log.count() != before || strings.Contains(raw, "HIDDEN_HOST") {
			t.Fatalf("%s sent a task or leaked", tc.name)
		}
		assertNoRefLeak(t, tc.name, raw)
		if !auditHasReason(t, w, sc, name, tc.msg) {
			t.Fatalf("%s refusal not audited", tc.name)
		}
	}
	blob := auditBlob(t, w, sc.project.ID)
	assertNoRefLeak(t, "audit", blob)
	if strings.Contains(blob, "HIDDEN_HOST") {
		t.Fatal("audit names the hidden reference")
	}
	assertNoRefLeak(t, "server log", sc.logs.String())
}

func auditHasReason(t *testing.T, w *world, sc refsScene, entry, reason string) bool {
	t.Helper()
	for _, row := range auditRows(t, w, sc.project.ID) {
		detail := detailMap(t, row.Detail)
		if (row.Action == audit.ActionCatalogDeploy || row.Action == audit.ActionCatalogRollback) && detail["entry"] == entry && detail["reason"] == reason {
			return true
		}
	}
	return false
}

// TestEnvReferencesDelegated: a bad reference refuses the delegated issue,
// and a reference broken after the plan was issued refuses the confirm,
// both as bad_request with no task. Rollback is refused the same way.
func TestEnvReferencesDelegated(t *testing.T) {
	w, sc, tok, log := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	delegated := tokenKind(t, w, sc.owner, store.KindDelegated)
	const missing = "env A references ${MISSING}, which is not set for this entry"
	createImage(t, w, tok, sc.project.ID, "bad", `{"kind":"service","image":"nginx","env":{"A":"${MISSING}"}}`)
	status, env, _ := postImage(t, w, delegated, project, "bad", "deploy", host, "del-bad", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", missing)
	if !auditHasReason(t, w, sc, "bad", missing) {
		t.Fatal("delegated refusal not audited")
	}
	// The refusal is stored under the idempotency key.
	status, env, _ = postImage(t, w, delegated, project, "bad", "deploy", host, "del-bad", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", missing)

	// A secret key holding the unknown reference: no NAME at the issue either.
	putEnv(t, w, tok, project, "secretref", "SECRET_URL=postgres://${HIDDEN_HOST}/x\n")
	createImage(t, w, tok, sc.project.ID, "hidden", `{"kind":"service","image":"nginx","env_sets":["secretref"]}`)
	const hidden = "env SECRET_URL has a reference that is not set for this entry"
	status, env, raw := postImage(t, w, delegated, project, "hidden", "deploy", host, "del-hidden", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", hidden)
	if strings.Contains(raw, "HIDDEN_HOST") || !auditHasReason(t, w, sc, "hidden", hidden) {
		t.Fatal("delegated secret-key refusal leaked the name or was not audited")
	}

	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","env_sets":["plain"],"env":{"URL":"http://${DB_HOST}"}}`)
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "web-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	digest, _ := objectPayload(t, env)["deploy"].(map[string]any)["digest"].(string)
	status, env, _ = postImage(t, w, delegated, project, "web", "deploy", host, "del-web", nil)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	id, _ := objectPayload(t, env)["confirmation_id"].(string)

	// A value-only edit keeps the spec hash, so the plan stays valid, but the
	// env no longer resolves.
	putKey(t, w, tok, project, "plain", "LITERAL", "broken ${", nil)
	const broken = "env LITERAL has an unterminated ${"
	before := log.count()
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("del-web"))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", broken)
	status, env, _ = postImage(t, w, tok, project, "web", "rollback", host, "rb-1", map[string]any{"digest": digest})
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", broken)
	status, env, _ = postImage(t, w, delegated, project, "web", "rollback", host, "rb-del", map[string]any{"digest": digest})
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", broken)
	if log.count() != before {
		t.Fatal("a refusal sent a task")
	}
	// Fixed with $${, the same plan is still usable: the confirm-time
	// refusal ran before the confirmation was marked used.
	putKey(t, w, tok, project, "plain", "LITERAL", "fixed $${", nil)
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("del-web"))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if log.count() != before+1 || lastSpec(t, log)["env"].(map[string]any)["LITERAL"] != "fixed ${" {
		t.Fatal("the confirm after the fix did not deploy with the escape applied")
	}
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "web-2", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	assertNoRefLeak(t, "server log", sc.logs.String())
}

// TestEnvNeedsKeyAgreesWithMerge pins that logsNeedKey and imageNeedsKey
// agree with mergeImageEnv's secret_keys: logs need the key exactly when the
// deploy would list a secret key, and a deploy with secret keys always
// needs the key.
func TestEnvNeedsKeyAgreesWithMerge(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	putEnvFlag(t, w, tok, project, "plainref", "URL=u:${POSTGRES_PASSWORD}\n", false)
	putEnv(t, w, tok, project, "level", "LEVEL=secret-level-value\n")
	cases := []struct {
		name, spec, keys string
	}{
		{"set secret", `{"kind":"service","image":"nginx","env_sets":["db"]}`, "POSTGRES_PASSWORD"},
		{"set plain", `{"kind":"service","image":"nginx","env_sets":["plain"]}`, ""},
		{"secret env", `{"kind":"service","image":"nginx","secret_env":{"PW":"db.POSTGRES_PASSWORD"}}`, "PW"},
		{"secret env from plain", `{"kind":"service","image":"nginx","secret_env":{"H":"plain.DB_HOST"}}`, ""},
		{"inline override", `{"kind":"service","image":"nginx","env_sets":["level"],"env":{"LEVEL":"inline"}}`, ""},
		{"plain key references a secret", `{"kind":"service","image":"nginx","env_sets":["db","plainref"]}`, "POSTGRES_PASSWORD,URL"},
		{"inline references a secret", `{"kind":"service","image":"nginx","env_sets":["db"],"env":{"URL":"${POSTGRES_PASSWORD}"}}`, "POSTGRES_PASSWORD,URL"},
	}
	ctx := t.Context()
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := fmt.Sprintf("agree%d", i)
			createImage(t, w, tok, sc.project.ID, name, tc.spec)
			entry, err := w.mem.GetCatalogEntry(ctx, sc.project.ID, name)
			if err != nil {
				t.Fatal(err)
			}
			img, err := catalog.ParseStored(entry.ImageRaw)
			if err != nil {
				t.Fatal(err)
			}
			merged, err := w.srv.mergeImageEnv(ctx, sc.project.ID, img)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(merged.secretKeys, ","); got != tc.keys {
				t.Fatalf("secret_keys %q want %q", got, tc.keys)
			}
			logs, err := w.srv.logsNeedKey(ctx, sc.project.ID, img)
			if err != nil {
				t.Fatal(err)
			}
			needs, err := w.srv.imageNeedsKey(ctx, sc.project.ID, img)
			if err != nil {
				t.Fatal(err)
			}
			hasSecret := len(merged.secretKeys) > 0
			if logs != hasSecret || (hasSecret && !needs) {
				t.Fatalf("logsNeedKey %v imageNeedsKey %v secret_keys %v", logs, needs, merged.secretKeys)
			}
		})
	}
}

// TestEnvHashGate is api-v1 v1.27 item 8: env_hash goes only to an agent
// that advertised programs.env_hash, follows the resolved env, is stable
// when nothing changed, and is omitted without the secrets key.
func TestEnvHashGate(t *testing.T) {
	programs := allowImages()
	programs["env_hash"] = true
	w, sc, tok, log := refsWorld(t, programs)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","env_sets":["db"],"env":{"URL":"u:${POSTGRES_PASSWORD}"}}`)
	status, env, raw := mustCall(t, w, tok, http.MethodGet, "/v1/hosts/"+host, "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	hostPrograms, _ := objectPayload(t, env)["programs"].(map[string]any)
	if hostPrograms["env_hash"] != true {
		t.Fatalf("host programs %s", raw)
	}
	// lastHash checks the newest task's env_hash against the resolved env.
	lastHash := func() string {
		t.Helper()
		spec := lastSpec(t, log)
		hash, _ := spec["env_hash"].(string)
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(hash) {
			t.Fatalf("env_hash %q", hash)
		}
		envMap, _ := spec["env"].(map[string]any)
		resolved := map[string]string{}
		for k, v := range envMap {
			resolved[k], _ = v.(string)
		}
		want, err := w.srv.Secrets.EnvHash(resolved)
		if err != nil || want != hash {
			t.Fatalf("env_hash is not the hash of the resolved env")
		}
		return hash
	}
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "h-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	first := lastHash()
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "h-2", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if lastHash() != first {
		t.Fatal("same env changed the hash")
	}
	// The delegated confirm path carries it too.
	delegated := tokenKind(t, w, sc.owner, store.KindDelegated)
	status, env, _ = postImage(t, w, delegated, project, "web", "deploy", host, "h-del", nil)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	id, _ := objectPayload(t, env)["confirmation_id"].(string)
	before := log.count()
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("h-del"))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if log.count() != before+1 || lastHash() != first {
		t.Fatal("confirmed deploy has no or a different env_hash")
	}
	// A value-only change behind the reference changes the hash.
	putKey(t, w, tok, project, "db", "POSTGRES_PASSWORD", "pg-secret-password-2", nil)
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "h-3", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	changed := lastHash()
	if changed == first {
		t.Fatal("value change kept the hash")
	}
	digest, _ := objectPayload(t, env)["deploy"].(map[string]any)["digest"].(string)
	status, env, _ = postImage(t, w, tok, project, "web", "rollback", host, "h-rb", map[string]any{"digest": digest})
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if lastHash() != changed {
		t.Fatal("rollback hash differs from the current env")
	}
	if strings.Contains(auditBlob(t, w, sc.project.ID), changed) || strings.Contains(sc.logs.String(), changed) {
		t.Fatal("env_hash stored in the audit or the log")
	}

	// Without the secrets key a capable agent gets no env_hash. An entry
	// with inline env only does not need the key to deploy.
	createImage(t, w, tok, sc.project.ID, "inline", `{"kind":"service","image":"nginx","env":{"A":"a"}}`)
	box := w.srv.Secrets
	w.srv.Secrets = nil
	status, env, _ = postImage(t, w, tok, project, "inline", "deploy", host, "h-nokey", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if _, ok := lastSpec(t, log)["env_hash"]; ok {
		t.Fatal("env_hash sent without the secrets key")
	}
	w.srv.Secrets = box

	// An agent without the flag gets no env_hash.
	setPrograms(t, w, sc.host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, Images: true, Docker: true, PolicyHash: "abc"})
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "h-old", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if spec := lastSpec(t, log); spec["env_hash"] != nil {
		t.Fatalf("env_hash sent to an older agent: %v", mapKeys(spec))
	}
}

// TestGeneratedRedaction: a generated value echoed by the agent is replaced
// by four bullets, never by a preview that shows its first characters, and
// so is a value that references it (v1.27 item 2).
func TestGeneratedRedaction(t *testing.T) {
	w, sc, tok, log := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	genKey(t, w, tok, project, "gen", "API_KEY", `{"kind":"password"}`)
	value := reveal(t, w, tok, project, "gen", "API_KEY")
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","env_sets":["gen"],"env":{"URL":"${API_KEY}@host"}}`)
	status, env, raw := postImage(t, w, tok, project, "web", "deploy", host, "g-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if lastSpec(t, log)["env"].(map[string]any)["API_KEY"] != value {
		t.Fatal("task has the wrong value")
	}
	for _, label := range []string{"deploy"} {
		if strings.Contains(raw, value) || strings.Contains(raw, value[:4]) {
			t.Fatalf("%s response shows the generated value or its first characters", label)
		}
	}
	// The API_KEY line is caught by the keyword redaction first; URL is not.
	if !strings.Contains(raw, "env URL="+bullets) {
		t.Fatalf("generated values not bulleted: %s", raw)
	}
	status, env, raw = postImage(t, w, tok, project, "web", "logs", host, "", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if strings.Contains(raw, value[:4]) || !strings.Contains(raw, "env URL="+bullets) {
		t.Fatalf("logs show the generated value: %s", raw)
	}
}

// TestProgramEnvStaysLiteral: program entries get inline env as written
// (api-v1 v1.27 item 7); no reference is resolved for them.
func TestProgramEnvStaysLiteral(t *testing.T) {
	entry := store.CatalogEntry{Name: "app", Kind: "program", Spec: store.ProgramSpec{
		Exec: "app", Env: map[string]string{"A": "${B}", "B": "b", "C": "$${x"},
	}}
	verb, task := programTask("proj", entry, "run", 0)
	env, _ := task["env"].(map[string]string)
	if verb != "program_start" || env["A"] != "${B}" || env["C"] != "$${x" {
		t.Fatalf("program env %v %v", verb, env)
	}
}

// TestProgramEnvSetsStayLiteral is v1.27 item 7: program Runs share mergeEnv
// with image deploys (v1.22 item 3) but get no ${KEY} resolution, while the
// image path resolves the same sources. A generated secret is flagged in
// both, so its redaction is four bullets (item 2).
func TestProgramEnvSetsStayLiteral(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	genKey(t, w, tok, project, "svc", "PW", `{"kind":"password"}`)
	refs := catalog.EnvRefs{Sets: []string{"svc"}, Inline: map[string]string{"URL": "postgres://u:${PW}@db/x"}}
	ctx := t.Context()

	prog, err := w.srv.mergeEnv(ctx, sc.project.ID, refs)
	if err != nil {
		t.Fatal(err)
	}
	if prog.env["URL"] != "postgres://u:${PW}@db/x" {
		t.Fatalf("program env resolved a reference: %q", prog.env["URL"])
	}
	if strings.Join(prog.secretKeys, ",") != "PW" || !prog.generated["PW"] || prog.generated["URL"] {
		t.Fatalf("program flags %v %v", prog.secretKeys, prog.generated)
	}

	img, err := w.srv.mergeImageEnv(ctx, sc.project.ID, catalog.Image{EnvSets: refs.Sets, Env: refs.Inline})
	if err != nil {
		t.Fatal(err)
	}
	if img.env["URL"] != "postgres://u:"+img.env["PW"]+"@db/x" || len(img.env["PW"]) != 32 {
		t.Fatal("image env did not resolve the reference")
	}
	if strings.Join(img.secretKeys, ",") != "PW,URL" || !img.generated["URL"] {
		t.Fatalf("image flags %v %v", img.secretKeys, img.generated)
	}
	for _, f := range prog.forms() {
		if f.with != catalog.SecretPreview("") {
			t.Fatalf("generated program value redacted as %q", f.with)
		}
	}
}
