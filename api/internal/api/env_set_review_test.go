package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Phase B review follow-ups (api-v1 v1.27 items 2, 4, 5 and 6).

const smtpMsg = "env SMTP_PASSWORD in set mail has no value yet; set it in the console"

// A plain placeholder made secret still needs a value: make secret is not a
// value write. The deploy stays refused until a typed value or generate.
func TestEnvPlaceholderMakeSecret(t *testing.T) {
	w, sc, tok, log := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	status, env, _ := mustCall(t, w, tok, http.MethodPost, envSetsPath(project), `{"name":"mail","keys":[{"key":"SMTP_PASSWORD","is_secret":false},{"key":"SMTP_USER","is_secret":false}]}`)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	status, env, _ = mustCall(t, w, tok, http.MethodPost, envPath(project, "mail")+"/secret", `{"keys":["SMTP_PASSWORD","SMTP_USER"]}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	keys := keysByName(t, env)
	for _, k := range []string{"SMTP_PASSWORD", "SMTP_USER"} {
		if keys[k]["is_secret"] != true || keys[k]["needs_value"] != true {
			t.Fatalf("%s after make secret %v", k, keys[k])
		}
	}
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","env_sets":["mail"]}`)
	before := log.count()
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "ms-1", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", smtpMsg)
	// Generate fills the first; a typed value fills the second.
	status, env, _ = genKey(t, w, tok, project, "mail", "SMTP_PASSWORD", `{"kind":"password"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "ms-2", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "env SMTP_USER in set mail has no value yet; set it in the console")
	status, env, _ = putKey(t, w, tok, project, "mail", "SMTP_USER", "mailer", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if log.count() != before {
		t.Fatal("a refused deploy sent a task")
	}
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "ms-3", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
}

// Replace never empties an existing secret, placeholder or not (the v1.18
// guard, as for merge and the key PUT).
func TestEnvReplaceKeepsSecretGuard(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	status, env, _ := mustCall(t, w, tok, http.MethodPost, envSetsPath(project), `{"name":"mail","keys":[{"key":"SMTP_PASSWORD"}]}`)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	for _, set := range []string{"mail", "db"} {
		key := map[string]string{"mail": "SMTP_PASSWORD", "db": "POSTGRES_PASSWORD"}[set]
		status, env, _ = mustCall(t, w, tok, http.MethodPut, envPath(project, set), `{"dotenv":"`+key+`=\n"}`)
		mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", store.ErrSecretEmpty.Error())
	}
	mats, err := w.mem.ListEnvMaterial(t.Context(), sc.project.ID, "mail")
	if err != nil || len(mats) != 1 || !mats[0].NeedsValue || !mats[0].IsSecret {
		t.Fatalf("placeholder changed %+v %v", mats, err)
	}
}

// A delegated confirm answers the names-only shape, also on replay; a
// console confirm of the same kind of plan is audited via console.
func TestEnvSetConfirmShapeAndVia(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	delegated := tokenKind(t, w, sc.owner, store.KindDelegated)
	body := `{"name":"shape","keys":[{"key":"TOKEN","generate":{"kind":"hex"}},{"key":"HOST","is_secret":false}],"idempotency_key":"shape-1"}`
	status, env, _ := mustCall(t, w, delegated, http.MethodPost, envSetsPath(project), body)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	id, _ := objectPayload(t, env)["confirmation_id"].(string)
	status, env, raw := mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("shape-1"))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	status, env, replay := mustCall(t, w, delegated, http.MethodPost, envSetsPath(project), body)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	for _, text := range []string{raw, replay} {
		if strings.Contains(text, `"preview"`) || strings.Contains(text, `"value"`) {
			t.Fatalf("delegated confirm shape %s", text)
		}
	}

	body = `{"name":"byconsole","keys":[{"key":"HOST","is_secret":false}],"idempotency_key":"con-1"}`
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, envSetsPath(project), body)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	id, _ = objectPayload(t, env)["confirmation_id"].(string)
	status, env, _ = mustCall(t, w, tok, http.MethodPost, "/v1/confirmations/"+id, confirmBody("con-1"))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	var via string
	for _, row := range auditRows(t, w, sc.project.ID) {
		if row.Action == audit.ActionEnvSetCreate && detailMap(t, row.Detail)["set"] == "byconsole" {
			via = row.Via
		}
	}
	if via != audit.ViaConsole {
		t.Fatalf("console confirm audited via %q", via)
	}
}

// Without the secrets key: a generated or secret key is unavailable at the
// console and at a delegated issue; plain placeholders need no key.
func TestEnvSetCreateSecretsKey(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	w.srv.Secrets = nil
	project := sc.project.ID.String()
	delegated := tokenKind(t, w, sc.owner, store.KindDelegated)
	for i, keys := range []string{`[{"key":"A","generate":{"kind":"hex"}}]`, `[{"key":"A"}]`} {
		for j, tk := range []string{tok, delegated} {
			body := `{"name":"nokey","keys":` + keys + `,"idempotency_key":"nk-` + string(rune('a'+i*2+j)) + `"}`
			status, env, _ := mustCall(t, w, tk, http.MethodPost, envSetsPath(project), body)
			mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", errSecretsKey.Error())
		}
	}
	status, env, _ := mustCall(t, w, tok, http.MethodPost, envSetsPath(project), `{"name":"plainonly","keys":[{"key":"HOST","is_secret":false}]}`)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, envSetsPath(project), `{"name":"plaintoo","keys":[{"key":"HOST","is_secret":false}],"idempotency_key":"plain-1"}`)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	id, _ := objectPayload(t, env)["confirmation_id"].(string)
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("plain-1"))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
}

// Item 6: rollback is refused like deploy; a needs_value key overridden by
// inline env still refuses (decision 2026-11-15: "any key the entry
// resolves" means its env sets and secret_env sources).
func TestEnvNeedsValueRollbackAndOverride(t *testing.T) {
	w, sc, tok, log := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","env_sets":["plain"]}`)
	status, env, _ := postImage(t, w, tok, project, "web", "deploy", host, "rb-0", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	digest, _ := objectPayload(t, env)["deploy"].(map[string]any)["digest"].(string)
	if err := w.mem.SetEnvNeedsValue(sc.project.ID, "plain", "DB_HOST"); err != nil {
		t.Fatal(err)
	}
	const msg = "env DB_HOST in set plain has no value yet; set it in the console"
	before := log.count()
	status, env, _ = postImage(t, w, tok, project, "web", "rollback", host, "rb-1", map[string]any{"digest": digest})
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msg)
	createImage(t, w, tok, sc.project.ID, "over", `{"kind":"service","image":"nginx","env_sets":["plain"],"env":{"DB_HOST":"inline"}}`)
	status, env, _ = postImage(t, w, tok, project, "over", "deploy", host, "ov-1", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msg)
	if log.count() != before {
		t.Fatal("a refused action sent a task")
	}
}

// Item 6 at confirm: a placeholder that appears after the plan (as a v1.28
// value request would make one) refuses the confirm with the exact text, no
// task, and the plan unused, for an image deploy and a program run.
func TestEnvNeedsValueAtConfirm(t *testing.T) {
	programs := secretsPrograms(true, nil)
	programs["images"], programs["docker"] = true, true
	w, sc, tok, log := refsWorld(t, programs)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	createImage(t, w, tok, sc.project.ID, "api", `{"kind":"service","image":"nginx","secret_env":{"PW":"db.POSTGRES_PASSWORD"}}`)
	status, env := w.do(tok, http.MethodPost, "/v1/projects/"+project+"/catalog", progBody("gh", map[string]any{"env_sets": []string{"db"}}))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	delegated := tokenKind(t, w, sc.owner, store.KindDelegated)
	issue := func(entry, action, key string) string {
		t.Helper()
		status, env, _ := postImage(t, w, delegated, project, entry, action, host, key, nil)
		mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
		id, _ := objectPayload(t, env)["confirmation_id"].(string)
		return id
	}
	deployID := issue("api", "deploy", "c-dep")
	runID := issue("gh", "run", "c-run")
	if err := w.mem.SetEnvNeedsValue(sc.project.ID, "db", "POSTGRES_PASSWORD"); err != nil {
		t.Fatal(err)
	}
	const msg = "env POSTGRES_PASSWORD in set db has no value yet; set it in the console"
	before := log.count()
	for id, key := range map[string]string{deployID: "c-dep", runID: "c-run"} {
		status, env, _ := mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody(key))
		mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msg)
		c, err := w.mem.GetConfirmation(t.Context(), secret.Hash(id))
		if err != nil || c.UsedAt != nil {
			t.Fatalf("plan %s used after the refusal: %v", key, err)
		}
	}
	if log.count() != before {
		t.Fatal("a refused confirm sent a task")
	}
	// Filled, the same plans go through.
	status, env, _ = putKey(t, w, tok, project, "db", "POSTGRES_PASSWORD", "pg-secret-password-7", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	for id, key := range map[string]string{deployID: "c-dep", runID: "c-run"} {
		status, env, _ := mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody(key))
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	}
}

// Stop is not affected by a placeholder: it succeeds.
func TestEnvNeedsValueStopRuns(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	status, env, _ := mustCall(t, w, tok, http.MethodPost, envSetsPath(project), `{"name":"mail","keys":[{"key":"SMTP_PASSWORD"}]}`)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	status, env = w.do(tok, http.MethodPost, "/v1/projects/"+project+"/catalog", progBody("gh", map[string]any{"env_sets": []string{"mail"}}))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, secretsPrograms(true, nil), nil)
	log := watchTasks(t, conn, runReply("stopped"))
	status, env, _ = postImageLike(t, w, tok, project, "gh", "stop", sc.host.ID.String(), "stop-1")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if log.count() != 1 {
		t.Fatalf("stop sent %d tasks", log.count())
	}
}

// Request checks and plan lifecycle edges for POST …/env-sets.
func TestEnvSetCreateEdges(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	delegated := tokenKind(t, w, sc.owner, store.KindDelegated)
	for _, tc := range []struct{ tok, body, code, msg string }{
		{delegated, `{"name":"e","keys":[{"key":"A"}]}`, "bad_request", "invalid idempotency_key"},
		{delegated, `{"name":"e","keys":[{"key":"A"}],"idempotency_key":"bad key!"}`, "bad_request", "invalid idempotency_key"},
		{tok, `{"name":"e","keys":[{"key":"lower"}]}`, "bad_request", ""},
		{tok, `{"name":"e","keys":[{"key":"A","generate":{"kind":"password","length":8}}]}`, "bad_request", "generate.length must be 16 to 64"},
		{tok, `{"name":"e","keys":[{"key":"A","generate":{"kind":"password","length":65}}]}`, "bad_request", "generate.length must be 16 to 64"},
	} {
		status, env, _ := mustCall(t, w, tc.tok, http.MethodPost, envSetsPath(project), tc.body)
		if status != http.StatusBadRequest || env.Code != tc.code || (tc.msg != "" && env.Msg != tc.msg) {
			t.Fatalf("%s: %d %s %q", tc.body, status, env.Code, env.Msg)
		}
	}
	// A delegated viewer is forbidden; a delegated GET of an unknown set is not_found.
	viewer := tokenKind(t, w, refsUser(t, w, "v@ex.com"), store.KindDelegated)
	status, env, _ := mustCall(t, w, viewer, http.MethodPost, envSetsPath(project), `{"name":"e","keys":[{"key":"A"}],"idempotency_key":"v-1"}`)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	status, env, _ = mustCall(t, w, delegated, http.MethodGet, envPath(project, "nosuch"), "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")

	// The same key with a different body is a conflict.
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, envSetsPath(project), `{"name":"e","keys":[{"key":"A"}],"idempotency_key":"same"}`)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	expiring, _ := objectPayload(t, env)["confirmation_id"].(string)
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, envSetsPath(project), `{"name":"e","keys":[{"key":"B"}],"idempotency_key":"same"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", msgKeyReused)

	// Expired: conflict with the expired text, nothing created.
	w.srv.Now = func() time.Time { return time.Date(2026, 10, 13, 12, 6, 0, 0, time.UTC) }
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+expiring, confirmBody("same"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this confirmation expired; call the tool again for a new one")
	w.srv.Now = func() time.Time { return time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC) }
	if _, err := w.mem.GetEnvSet(t.Context(), sc.project.ID, "e"); err == nil {
		t.Fatal("an expired plan created the set")
	}

	// A role lost between issue and confirm: forbidden and audited denied.
	admin := refsUser(t, w, "a@ex.com")
	adminTok := tokenKind(t, w, admin, store.KindDelegated)
	status, env, _ = mustCall(t, w, adminTok, http.MethodPost, envSetsPath(project), `{"name":"demoted","keys":[{"key":"A"}],"idempotency_key":"dm-1"}`)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	planID, _ := objectPayload(t, env)["confirmation_id"].(string)
	if _, err := w.mem.UpdateMemberRole(t.Context(), sc.project.ID, admin, "viewer"); err != nil {
		t.Fatal(err)
	}
	status, env, _ = mustCall(t, w, adminTok, http.MethodPost, "/v1/confirmations/"+planID, confirmBody("dm-1"))
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	denied := false
	for _, row := range auditRows(t, w, sc.project.ID) {
		if row.Action == audit.ActionEnvSetCreate && row.Decision == audit.DecisionDenied {
			denied = true
		}
	}
	if !denied {
		t.Fatal("the refused confirm was not audited")
	}
}
