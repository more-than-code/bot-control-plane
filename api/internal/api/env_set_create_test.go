package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.27 items 4, 5 and 6 (phase B).

func envSetsPath(project string) string { return "/v1/projects/" + project + "/env-sets" }

// refsUser is a refsWorld member by email: a@ex.com is admin, v@ex.com viewer.
func refsUser(t *testing.T, w *world, email string) uuid.UUID {
	t.Helper()
	u, err := w.mem.UserByEmail(t.Context(), email)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

const createBody = `{"name":"daikou-db","keys":[` +
	`{"key":"POSTGRES_PASSWORD","generate":{"kind":"password","length":20}},` +
	`{"key":"SIGNING_KEY","generate":{"kind":"hex"}},` +
	`{"key":"SMTP_PASSWORD"},` +
	`{"key":"SMTP_HOST","is_secret":false}],"idempotency_key":"create-1"}`

const createPlan = "create env set daikou-db with POSTGRES_PASSWORD (generated password, 20 characters, secret), " +
	"SIGNING_KEY (generated hex, secret), SMTP_PASSWORD (empty; fill it in the console), SMTP_HOST (empty; fill it in the console)"

func TestEnvSetCreateConsole(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	status, env, raw := mustCall(t, w, tok, http.MethodPost, envSetsPath(project), createBody)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	keys := keysByName(t, env)
	bullets := catalog.SecretPreview("")
	for name, want := range map[string][3]bool{ // is_secret, reveal_pending, needs_value
		"POSTGRES_PASSWORD": {true, true, false},
		"SIGNING_KEY":       {true, true, false},
		"SMTP_PASSWORD":     {true, false, true},
		"SMTP_HOST":         {false, false, true},
	} {
		k := keys[name]
		if k == nil || k["is_secret"] != want[0] || k["reveal_pending"] != want[1] || k["needs_value"] != want[2] {
			t.Fatalf("%s: %v", name, k)
		}
		if want[0] && (k["preview"] != bullets || k["value"] != nil) {
			t.Fatalf("%s preview %v", name, k)
		}
	}
	if keys["SMTP_HOST"]["value"] != "" {
		t.Fatalf("plain placeholder %v", keys["SMTP_HOST"])
	}
	// The generated values are revealed once each, with the stated shape.
	if v := reveal(t, w, tok, project, "daikou-db", "POSTGRES_PASSWORD"); len(v) != 20 || strings.Contains(raw, v) {
		t.Fatalf("password %d", len(v))
	}
	if v := reveal(t, w, tok, project, "daikou-db", "SIGNING_KEY"); len(v) != 64 {
		t.Fatalf("hex %d", len(v))
	}
	// Placeholders are not revealable.
	status, env, _ = mustCall(t, w, tok, http.MethodPost, envPath(project, "daikou-db")+"/keys/SMTP_PASSWORD/reveal", "")
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "the value of SMTP_PASSWORD was already shown or was not generated; edit it to set a new value")

	var found bool
	for _, row := range auditRows(t, w, sc.project.ID) {
		if row.Action != audit.ActionEnvSetCreate {
			continue
		}
		found = true
		detail := detailMap(t, row.Detail)
		gen, _ := detail["generated"].(map[string]any)
		ph, _ := detail["placeholders"].([]any)
		if row.Via != audit.ViaConsole || detail["set"] != "daikou-db" || gen["POSTGRES_PASSWORD"] != "password" || gen["SIGNING_KEY"] != "hex" || len(ph) != 2 {
			t.Fatalf("audit %v via %s", detail, row.Via)
		}
	}
	if !found {
		t.Fatal("no env_set.create audit")
	}

	// An existing name is a conflict and changes nothing.
	status, env, _ = mustCall(t, w, tok, http.MethodPost, envSetsPath(project), createBody)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "env set daikou-db already exists")
}

func TestEnvSetCreateValidation(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	many := make([]string, 65)
	for i := range many {
		many[i] = fmt.Sprintf(`{"key":"K%02d"}`, i)
	}
	cases := []struct{ body, code, msg string }{
		// api-v1 v1.32 item 2: a value only with is_secret explicitly false and no generate.
		{`{"name":"s","keys":[{"key":"A","value":"x"}]}`, "bad_request", "a value can be given only for a plain key"},
		{`{"name":"s","keys":[{"key":"A","value":"x","is_secret":true}]}`, "bad_request", "a value can be given only for a plain key"},
		{`{"name":"s","keys":[{"key":"A","value":"x","is_secret":false,"generate":{"kind":"hex"}}]}`, "bad_request", "a value can be given only for a plain key"},
		{`{"name":"s","keys":[{"key":"A","value":"a\nb","is_secret":false}]}`, "bad_request", "invalid env value"},
		{`{"name":"s","keys":[{"key":"A","value":"` + strings.Repeat("x", 4097) + `","is_secret":false}]}`, "bad_request", "invalid env value"},
		{`{"name":"s","keys":[{"key":"A","valu":"x"}]}`, "bad_request", "invalid json"},
		{`{"name":"s","keys":[{"key":"A","generate":{"kind":"hex"},"is_secret":false}]}`, "bad_request", "a generated value is secret"},
		{`{"name":"s","keys":[]}`, "bad_request", "keys must list 1 to 64 keys"},
		{`{"name":"s","keys":[` + strings.Join(many, ",") + `]}`, "bad_request", "keys must list 1 to 64 keys"},
		{`{"name":"s","keys":[{"key":"A"},{"key":"A"}]}`, "bad_request", "duplicate key A"},
		{`{"name":"Bad Name","keys":[{"key":"A"}]}`, "bad_request", "invalid name"},
		{`{"name":"s","keys":[{"key":"A","generate":{"kind":"words"}}]}`, "bad_request", `generate.kind must be "password", "hex" or "base64url"`},
	}
	for _, tc := range cases {
		status, env, _ := mustCall(t, w, tok, http.MethodPost, envSetsPath(project), tc.body)
		if env.Code != tc.code || env.Msg != tc.msg || status != http.StatusBadRequest {
			t.Fatalf("%s: %d %s %q", tc.body, status, env.Code, env.Msg)
		}
	}
	if _, err := w.mem.GetEnvSet(t.Context(), sc.project.ID, "s"); err == nil {
		t.Fatal("a refused create wrote a set")
	}
	// A viewer may not create.
	viewer := w.token(refsUser(t, w, "v@ex.com"))
	status, env, _ := mustCall(t, w, viewer, http.MethodPost, envSetsPath(project), `{"name":"s","keys":[{"key":"A"}]}`)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
}

func TestEnvSetCreateDelegated(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	delegated := tokenKind(t, w, sc.owner, store.KindDelegated)
	status, env, raw := mustCall(t, w, delegated, http.MethodPost, envSetsPath(project), createBody)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	payload := objectPayload(t, env)
	if payload["plan"] != createPlan {
		t.Fatalf("plan %q", payload["plan"])
	}
	id, _ := payload["confirmation_id"].(string)
	if _, err := w.mem.GetEnvSet(t.Context(), sc.project.ID, "daikou-db"); err == nil {
		t.Fatal("the plan created the set")
	}
	// The same request and key replays the 202.
	status, env, again := mustCall(t, w, delegated, http.MethodPost, envSetsPath(project), createBody)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	if again != raw {
		t.Fatal("replay differs")
	}
	// A wrong key at confirm is a conflict and leaves the plan.
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("other-key"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "idempotency_key does not match the request that issued this confirmation")
	// Another user cannot confirm it.
	stranger := tokenKind(t, w, refsUser(t, w, "a@ex.com"), store.KindDelegated)
	status, env, _ = mustCall(t, w, stranger, http.MethodPost, "/v1/confirmations/"+id, confirmBody("create-1"))
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")

	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("create-1"))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	keys := keysByName(t, env)
	if keys["POSTGRES_PASSWORD"]["reveal_pending"] != true || keys["SMTP_PASSWORD"]["needs_value"] != true || keys["SMTP_HOST"]["is_secret"] != false {
		t.Fatalf("confirmed keys %v", keys)
	}
	// Used: a second confirm says so; the issue replays the stored 201.
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("create-1"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this confirmation was already used")
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, envSetsPath(project), createBody)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, envSetsPath(project), strings.Replace(createBody, "create-1", "create-2", 1))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "env set daikou-db already exists")

	// The generated value never left the API: only the console reveal has it.
	value := reveal(t, w, tok, project, "daikou-db", "POSTGRES_PASSWORD")
	blob := auditBlob(t, w, sc.project.ID)
	if strings.Contains(blob, value) || strings.Contains(raw, value) {
		t.Fatal("generated value in the plan or audit")
	}
	var issued, created bool
	for _, row := range auditRows(t, w, sc.project.ID) {
		switch row.Action {
		case audit.ActionEnvSetWriteConfirmation:
			issued = true
		case audit.ActionEnvSetCreate:
			created = row.Via == audit.ViaMCP
		}
	}
	if !issued || !created {
		t.Fatalf("audit issued %v created-via-mcp %v", issued, created)
	}
}

// A set created after the plan was issued makes the confirm a conflict; the
// plan stays usable once the name is free again.
func TestEnvSetCreateConfirmConflictKeepsPlan(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	delegated := tokenKind(t, w, sc.owner, store.KindDelegated)
	body := `{"name":"later","keys":[{"key":"A"}],"idempotency_key":"later-1"}`
	status, env, _ := mustCall(t, w, delegated, http.MethodPost, envSetsPath(project), body)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	id, _ := objectPayload(t, env)["confirmation_id"].(string)
	status, env, _ = mustCall(t, w, tok, http.MethodPost, envSetsPath(project), `{"name":"later","keys":[{"key":"B"}]}`)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("later-1"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "env set later already exists")
	status, env, _ = mustCall(t, w, tok, http.MethodDelete, envPath(project, "later"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("later-1"))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
}

func TestEnvSetDelegatedReadNamesOnly(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	status, env, _ := mustCall(t, w, tok, http.MethodPost, envSetsPath(project), createBody)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	delegated := tokenKind(t, w, sc.owner, store.KindDelegated)
	for _, path := range []string{envSetsPath(project), envPath(project, "db"), envPath(project, "plain"), envPath(project, "daikou-db")} {
		status, env, raw := mustCall(t, w, delegated, http.MethodGet, path, "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		if strings.Contains(raw, `"value"`) || strings.Contains(raw, `"preview"`) || strings.Contains(raw, refPassword) || strings.Contains(raw, "postgres") {
			t.Fatalf("GET %s: %s", path, raw)
		}
	}
	status, env, _ = mustCall(t, w, delegated, http.MethodGet, envPath(project, "daikou-db"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	keys := keysByName(t, env)
	for name, k := range keys {
		var fields []string
		for f := range k {
			fields = append(fields, f)
		}
		if len(k) != 4 || k["is_secret"] == nil || k["reveal_pending"] == nil || k["needs_value"] == nil {
			t.Fatalf("%s fields %v", name, fields)
		}
	}
	if keys["SMTP_PASSWORD"]["needs_value"] != true || keys["POSTGRES_PASSWORD"]["reveal_pending"] != true {
		t.Fatalf("flags %v", keys)
	}
	// A viewer's delegated read is refused, as the console read is.
	viewer := tokenKind(t, w, refsUser(t, w, "v@ex.com"), store.KindDelegated)
	status, env, _ = mustCall(t, w, viewer, http.MethodGet, envSetsPath(project), "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
}

// Item 6: a deploy or rollback that resolves a placeholder is refused, at
// the console, at a delegated issue, and at confirm, with no task. Filling
// the key (typed, or generate on a placeholder) clears it.
func TestEnvNeedsValueDeploy(t *testing.T) {
	w, sc, tok, log := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	status, env, _ := mustCall(t, w, tok, http.MethodPost, envSetsPath(project), `{"name":"mail","keys":[{"key":"SMTP_PASSWORD"},{"key":"SMTP_HOST","is_secret":false}]}`)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","env_sets":["plain","mail"]}`)
	createImage(t, w, tok, sc.project.ID, "api", `{"kind":"service","image":"nginx","secret_env":{"PW":"mail.SMTP_PASSWORD"}}`)
	const hostMsg = "env SMTP_HOST in set mail has no value yet; set it in the console"
	const pwMsg = "env SMTP_PASSWORD in set mail has no value yet; set it in the console"
	before := log.count()
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "nv-1", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", hostMsg)
	status, env, _ = postImage(t, w, tok, project, "api", "deploy", host, "nv-2", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", pwMsg)
	delegated := tokenKind(t, w, sc.owner, store.KindDelegated)
	status, env, _ = postImage(t, w, delegated, project, "api", "deploy", host, "nv-del", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", pwMsg)
	if log.count() != before {
		t.Fatal("a refused deploy sent a task")
	}

	// Generate fills a secret placeholder; a typed value fills a plain one.
	status, env, _ = genKey(t, w, tok, project, "mail", "SMTP_PASSWORD", `{"kind":"password"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if k := objectPayload(t, env); k["reveal_pending"] != true || k["needs_value"] != false {
		t.Fatalf("generated placeholder %v", k)
	}
	// Generate on a filled key is refused again (rev 3).
	status, env, _ = genKey(t, w, tok, project, "mail", "SMTP_PASSWORD", `{"kind":"password"}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "SMTP_PASSWORD already has a value; edit it to change it")
	// A generated value is secret, so a plain placeholder takes a typed value.
	status, env, _ = genKey(t, w, tok, project, "mail", "SMTP_HOST", `{"kind":"hex"}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", store.ErrSecretFixed.Error())

	status, env, _ = postImage(t, w, tok, project, "api", "deploy", host, "nv-3", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env, _ = putKey(t, w, tok, project, "mail", "SMTP_HOST", "smtp.example", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "nv-4", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if lastSpec(t, log)["env"].(map[string]any)["SMTP_HOST"] != "smtp.example" {
		t.Fatal("filled value not deployed")
	}
}

// Item 6 for programs (contract amended 2026-11-15): Run and Restart are
// refused while a resolved key needs a value, at the console and at a
// delegated issue, with no task; Stop is not affected.
func TestEnvNeedsValueProgram(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	status, env, _ := mustCall(t, w, tok, http.MethodPost, envSetsPath(project), `{"name":"mail","keys":[{"key":"SMTP_PASSWORD"}]}`)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	status, env = w.do(tok, http.MethodPost, "/v1/projects/"+project+"/catalog", progBody("gh", map[string]any{"env_sets": []string{"mail"}}))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	status, env = w.do(tok, http.MethodPost, "/v1/projects/"+project+"/catalog", progBody("other", map[string]any{"secret_env": map[string]string{"PW": "mail.SMTP_PASSWORD"}}))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, secretsPrograms(true, nil), nil)
	log := watchTasks(t, conn, runReply("ok"))

	const msg = "env SMTP_PASSWORD in set mail has no value yet; set it in the console"
	for i, tc := range []struct{ entry, action string }{{"gh", "run"}, {"gh", "restart"}, {"other", "run"}, {"other", "restart"}} {
		status, env, _ = postImageLike(t, w, tok, project, tc.entry, tc.action, host, fmt.Sprintf("p-%d", i))
		mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msg)
	}
	delegated := tokenKind(t, w, owner.ID, store.KindDelegated)
	status, env, _ = postImageLike(t, w, delegated, project, "gh", "run", host, "p-del")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msg)
	if log.count() != 0 {
		t.Fatalf("a refused program action sent %d tasks", log.count())
	}
	// Stop is not affected.
	status, env, _ = postImageLike(t, w, tok, project, "gh", "stop", host, "p-stop")
	if env.Msg == msg {
		t.Fatal("stop was refused for a placeholder")
	}
	denied := 0
	for _, row := range auditRows(t, w, sc.project.ID) {
		if row.Decision == audit.DecisionDenied && detailMap(t, row.Detail)["reason"] == msg {
			denied++
		}
	}
	if denied != 5 {
		t.Fatalf("denials audited %d, want 5", denied)
	}

	// Filled, Run goes through.
	status, env, _ = putKey(t, w, tok, project, "mail", "SMTP_PASSWORD", "smtp-secret-value-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	before := log.count()
	status, env, _ = postImageLike(t, w, tok, project, "gh", "run", host, "p-ok")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if log.count() != before+1 {
		t.Fatal("run after fill sent no task")
	}
}

// TestEnvSetCreatePlainValues: a plain key may carry its value (api-v1
// v1.32 item 2). It is stored as a plain value with needs_value false, and
// the plan, the audit and the delegated answers name the key only.
func TestEnvSetCreatePlainValues(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	const dsn = "postgres://app:${DB_PASSWORD}@db:5432/app-plain-zz"
	body := `{"name":"dd","keys":[{"key":"DB_PASSWORD","generate":{"kind":"password"}},` +
		`{"key":"DATABASE_URL","is_secret":false,"value":"` + dsn + `"},` +
		`{"key":"LISTEN","is_secret":false,"value":""},{"key":"SMTP_PASSWORD"}],"idempotency_key":"plain-1"}`

	delegated := tokenKind(t, w, sc.owner, store.KindDelegated)
	status, env, raw := mustCall(t, w, delegated, http.MethodPost, envSetsPath(project), body)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	plan, _ := objectPayload(t, env)["plan"].(string)
	want := "create env set dd with DB_PASSWORD (generated password, 32 characters, secret), DATABASE_URL (plain, value given), LISTEN (plain, value given), SMTP_PASSWORD (empty; fill it in the console)"
	if plan != want {
		t.Fatalf("plan %q", plan)
	}
	id, _ := objectPayload(t, env)["confirmation_id"].(string)
	status, env, confirmed := mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("plain-1"))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	keys := keysByName(t, env)
	if keys["DATABASE_URL"]["is_secret"] != false || keys["DATABASE_URL"]["needs_value"] != false || keys["LISTEN"]["needs_value"] != false ||
		keys["SMTP_PASSWORD"]["needs_value"] != true || keys["DB_PASSWORD"]["reveal_pending"] != true {
		t.Fatalf("keys %v", keys)
	}
	for _, text := range []string{raw, confirmed, auditBlob(t, w, sc.project.ID)} {
		if strings.Contains(text, "app-plain-zz") {
			t.Fatalf("plain value outside the set: %s", text)
		}
	}
	// The stored plain value is the given text, and the console reads it.
	mats, err := w.mem.ListEnvMaterial(t.Context(), sc.project.ID, "dd")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range mats {
		if m.Key == "DATABASE_URL" {
			found = !m.IsSecret && m.Plain == dsn && !m.NeedsValue
		}
	}
	if !found {
		t.Fatalf("stored %+v", mats)
	}
	status, env, got := mustCall(t, w, tok, http.MethodGet, envSetsPath(project)+"/dd", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if !strings.Contains(got, "app-plain-zz") {
		t.Fatal("console read lacks the plain value")
	}
	var detail map[string]any
	for _, row := range auditRows(t, w, sc.project.ID) {
		if row.Action == audit.ActionEnvSetCreate {
			detail = detailMap(t, row.Detail)
		}
	}
	plain, _ := detail["plain_values"].([]any)
	ph, _ := detail["placeholders"].([]any)
	if len(plain) != 2 || plain[0] != "DATABASE_URL" || plain[1] != "LISTEN" || len(ph) != 1 {
		t.Fatalf("audit %v", detail)
	}

	// A console create with a plain value applies at once.
	status, env, _ = mustCall(t, w, tok, http.MethodPost, envSetsPath(project), `{"name":"web","keys":[{"key":"PORT","is_secret":false,"value":"8080"}]}`)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	if k := keysByName(t, env)["PORT"]; k["value"] != "8080" || k["needs_value"] != false {
		t.Fatalf("console key %v", k)
	}
}

// TestEnvSetCreatePlainSecretReminder: a plain value under a key name that
// looks secret is accepted, and the plan adds one reminder per such key,
// naming the key and never the value (Joe 2026-11-16: a reminder, not a
// refusal).
func TestEnvSetCreatePlainSecretReminder(t *testing.T) {
	w, sc, _, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	delegated := tokenKind(t, w, sc.owner, store.KindDelegated)
	body := `{"name":"warn","keys":[{"key":"DB_PASSWORD","is_secret":false,"value":"hunter2-plain-zz"},` +
		`{"key":"HOST","is_secret":false,"value":"db-host-zz"}],"idempotency_key":"warn-1"}`
	status, env, raw := mustCall(t, w, delegated, http.MethodPost, envSetsPath(project), body)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	plan, _ := objectPayload(t, env)["plan"].(string)
	want := "create env set warn with DB_PASSWORD (plain, value given), HOST (plain, value given). " +
		"DB_PASSWORD looks like a secret but will be stored as plain text, visible in the console; use a placeholder and request_env_value instead."
	if plan != want {
		t.Fatalf("plan %q", plan)
	}
	if strings.Contains(plan, "HOST looks like a secret") {
		t.Fatal("HOST warned")
	}
	for _, leak := range []string{"hunter2-plain-zz", "db-host-zz"} {
		if strings.Contains(raw, leak) || strings.Contains(auditBlob(t, w, sc.project.ID), leak) {
			t.Fatalf("value %s in the plan or audit", leak)
		}
	}
	// Accepted: the confirm creates both keys as plain.
	id, _ := objectPayload(t, env)["confirmation_id"].(string)
	status, env, confirmed := mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("warn-1"))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	if k := keysByName(t, env)["DB_PASSWORD"]; k["is_secret"] != false || k["needs_value"] != false || strings.Contains(confirmed, "hunter2-plain-zz") {
		t.Fatalf("created %v", k)
	}
}
