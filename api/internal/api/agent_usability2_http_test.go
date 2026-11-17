package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.33 (agent usability round 2): env set update and delegated
// delete as plans, rotation by request, used_by, env_pairs, entry-private
// secrets, the shared-network snippet and the open-requests list.

const (
	u2PlainValue  = "plain-value-u2-zz"
	u2SecretValue = "secret-value-u2-zz"
)

// planOf issues a request that answers a plan and returns its id and text.
func planOf(t *testing.T, w *world, tok, method, path, body string) (string, string) {
	t.Helper()
	status, env, raw := mustCall(t, w, tok, method, path, body)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	out := objectPayload(t, env)
	id, _ := out["confirmation_id"].(string)
	plan, _ := out["plan"].(string)
	if id == "" || plan == "" || strings.Contains(raw, u2PlainValue) || strings.Contains(raw, u2SecretValue) {
		t.Fatalf("plan %s", raw)
	}
	return id, plan
}

func confirmOK(t *testing.T, w *world, tok, id, key string, wantStatus int) (envelope, string) {
	t.Helper()
	status, env, raw := mustCall(t, w, tok, http.MethodPost, "/v1/confirmations/"+id, confirmBody(key))
	mustHTTP(t, status, env, wantStatus, "ok", "ok")
	return env, raw
}

func TestEnvSetPatchRules(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	del := tokenKind(t, w, sc.owner, store.KindDelegated)
	putEnvFlag(t, w, tok, project, "app", "API_URL=https://x\nOLD=o\nMODE=s\n", false)
	putEnvFlag(t, w, tok, project, "app", "API_URL=https://x\nOLD=o\nMODE=s\nSEC="+u2SecretValue+"\n", false)
	// SEC is made secret so the set has a secret key.
	status, env, _ := mustCall(t, w, tok, http.MethodPost, envPath(project, "app")+"/secret", `{"keys":["SEC"]}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	path := envPath(project, "app")
	for _, tc := range []struct {
		name, body string
		status     int
		code, msg  string
	}{
		{"empty", `{"idempotency_key":"k"}`, 400, "bad_request", msgPatchEmpty},
		{"secret set", `{"set":{"SEC":"x"},"idempotency_key":"k"}`, 400, "bad_request", msgChangeSecretValue},
		{"two lists", `{"set":{"OLD":"x"},"remove":["OLD"],"idempotency_key":"k"}`, 400, "bad_request", "key OLD is named more than once"},
		{"missing key", `{"remove":["NOPE"],"idempotency_key":"k"}`, 404, "not_found", "no key NOPE in env set app"},
		{"add existing", `{"add":[{"key":"OLD"}],"idempotency_key":"k"}`, 409, "conflict", "OLD is already in env set app"},
		{"add secret value", `{"add":[{"key":"NEW","value":"v"}],"idempotency_key":"k"}`, 400, "bad_request", errPlainValueOnly},
		{"bad key", `{"remove":["OLD"]}`, 400, "bad_request", "invalid idempotency_key"},
	} {
		status, env, _ := mustCall(t, w, del, http.MethodPatch, path, tc.body)
		if status != tc.status || env.Code != tc.code || env.Msg != tc.msg {
			t.Fatalf("%s: %d %s %q", tc.name, status, env.Code, env.Msg)
		}
	}
	status, env, _ = mustCall(t, w, del, http.MethodPatch, envPath(project, "nope"), `{"remove":["OLD"],"idempotency_key":"k"}`)
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "no env set nope")
	// A key an entry resolves cannot be removed.
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","env_sets":["app"],"env":{"MODE":"inline"}}`)
	status, env, _ = mustCall(t, w, del, http.MethodPatch, path, `{"remove":["OLD"],"idempotency_key":"k"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "OLD is used by entries web; update them first")
	// MODE is overridden inline, so web does not resolve it from app.
	if users := keyUsersOf(t, w, sc, "app", "MODE"); len(users) != 0 {
		t.Fatalf("overridden key used by %v", users)
	}
	createImage(t, w, tok, sc.project.ID, "api", `{"kind":"service","image":"nginx","secret_env":{"S":"app.SEC"}}`)
	status, env, _ = mustCall(t, w, del, http.MethodPatch, path, `{"remove":["SEC"],"idempotency_key":"k"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "SEC is used by entries api, web; update them first")
	// A viewer is refused.
	viewer := tokenKind(t, w, refsUser(t, w, "v@ex.com"), store.KindDelegated)
	status, env, _ = mustCall(t, w, viewer, http.MethodPatch, path, `{"remove":["OLD"],"idempotency_key":"k"}`)
	if status != http.StatusForbidden {
		t.Fatalf("viewer %d %s", status, env.Code)
	}
}

func keyUsersOf(t *testing.T, w *world, sc refsScene, set, key string) []string {
	t.Helper()
	entries, err := w.mem.ListCatalogEntries(t.Context(), sc.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	names, err := w.mem.ListEnvKeyNames(t.Context(), sc.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	return keyUsers(entries, names, set, key)
}

func TestEnvSetPatchConfirm(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	del := tokenKind(t, w, sc.owner, store.KindDelegated)
	putEnvFlag(t, w, tok, project, "app", "API_URL=https://old\nOLD=o\nMOVE=m\n", false)
	body := `{"set":{"API_URL":"https://new","API_TOKEN":"x"},"idempotency_key":"p1"}`
	status, env, _ := mustCall(t, w, del, http.MethodPatch, envPath(project, "app"), body)
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "no key API_TOKEN in env set app")

	body = `{"set":{"API_URL":"` + u2PlainValue + `"},"add":[{"key":"GEN","generate":{"kind":"hex"}},{"key":"PH"},{"key":"CLIENT_SECRET","is_secret":false,"value":"` + u2PlainValue + `"}],"remove":["OLD"],"make_secret":["MOVE"],"idempotency_key":"p1"}`
	id, plan := planOf(t, w, del, http.MethodPatch, envPath(project, "app"), body)
	want := "update env set app: set the plain value of API_URL; add GEN (generated hex, secret), PH (empty; fill it in the console), CLIENT_SECRET (plain, value given); remove OLD; make secret (one way) MOVE. CLIENT_SECRET" + plainSecretWarning
	if plan != want {
		t.Fatalf("plan\n%s\nwant\n%s", plan, want)
	}
	// Nothing changed yet.
	status, env, raw := mustCall(t, w, tok, http.MethodGet, envPath(project, "app"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if !strings.Contains(raw, "https://old") || strings.Contains(raw, "GEN") {
		t.Fatalf("changed before confirm %s", raw)
	}
	env, raw = confirmOK(t, w, del, id, "p1", http.StatusOK)
	if strings.Contains(raw, u2PlainValue) || strings.Contains(raw, "preview") || strings.Contains(raw, `"value"`) {
		t.Fatalf("delegated confirm leaks %s", raw)
	}
	keys := keysByName(t, env)
	if keys["GEN"]["reveal_pending"] != true || keys["PH"]["needs_value"] != true || keys["MOVE"]["is_secret"] != true || keys["OLD"] != nil {
		t.Fatalf("keys %v", keys)
	}
	status, env, _ = mustCall(t, w, tok, http.MethodGet, envPath(project, "app"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	keys = keysByName(t, env)
	assertPlainKey(t, keys["API_URL"], u2PlainValue)
	assertPlainKey(t, keys["CLIENT_SECRET"], u2PlainValue)
	if keys["MOVE"]["is_secret"] != true {
		t.Fatalf("MOVE %v", keys["MOVE"])
	}
	// Single use.
	status, env, _ = mustCall(t, w, del, http.MethodPost, "/v1/confirmations/"+id, confirmBody("p1"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this confirmation was already used")
	// The audit row names keys only.
	found := 0
	for _, row := range auditRows(t, w, sc.project.ID) {
		if strings.Contains(string(row.Detail), u2PlainValue) {
			t.Fatalf("audit leaks a value: %s", row.Detail)
		}
		if row.Action != audit.ActionEnvSetUpdate {
			continue
		}
		found++
		d := detailMap(t, row.Detail)
		if d["set"] != "app" || len(d["removed"].([]any)) != 1 || len(d["made_secret"].([]any)) != 1 || len(d["added"].([]any)) != 3 || row.Via != audit.ViaMCP {
			t.Fatalf("detail %v via %s", d, row.Via)
		}
	}
	if found != 1 {
		t.Fatalf("env_set.update rows %d", found)
	}
}

func TestEnvSetPatchChangedSincePlan(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	del := tokenKind(t, w, sc.owner, store.KindDelegated)
	putEnvFlag(t, w, tok, project, "app", "OLD=o\n", false)
	id, _ := planOf(t, w, del, http.MethodPatch, envPath(project, "app"), `{"set":{"OLD":"n"},"idempotency_key":"p2"}`)
	// The key turns secret before the confirm.
	status, env, _ := mustCall(t, w, tok, http.MethodPost, envPath(project, "app")+"/secret", `{"keys":["OLD"]}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env, _ = mustCall(t, w, del, http.MethodPost, "/v1/confirmations/"+id, confirmBody("p2"))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgChangeSecretValue)
}

func TestEnvSetDeleteDelegatedPlan(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	del := tokenKind(t, w, sc.owner, store.KindDelegated)
	putEnvFlag(t, w, tok, project, "app", "A=1\nB=2\n", false)
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","secret_env":{"A":"app.A"}}`)
	status, env, _ := mustCall(t, w, del, http.MethodDelete, envPath(project, "app"), `{"idempotency_key":"d1"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "env set app is used by entries web")
	// The console direct delete answers the same text.
	status, env, _ = mustCall(t, w, tok, http.MethodDelete, envPath(project, "app"), "")
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "env set app is used by entries web")

	putEnvFlag(t, w, tok, project, "spare", "X=1\nY=2\n", false)
	id, plan := planOf(t, w, del, http.MethodDelete, envPath(project, "spare"), `{"idempotency_key":"d2"}`)
	if plan != "delete env set spare and its 2 keys (X, Y); open value requests for it stop working" {
		t.Fatalf("plan %q", plan)
	}
	if _, err := w.mem.GetEnvSet(t.Context(), sc.project.ID, "spare"); err != nil {
		t.Fatal("deleted before confirm")
	}
	env, _ = confirmOK(t, w, del, id, "d2", http.StatusOK)
	if out := objectPayload(t, env); out["name"] != "spare" || out["deleted"] != true || len(out) != 2 {
		t.Fatalf("confirm %v", out)
	}
	if _, err := w.mem.GetEnvSet(t.Context(), sc.project.ID, "spare"); err == nil {
		t.Fatal("not deleted")
	}
	if countAction(auditRows(t, w, sc.project.ID), audit.ActionEnvSetDelete) != 1 {
		t.Fatal("env_set.delete audit")
	}
	// A plan whose set gained a user meanwhile is refused at confirm.
	putEnvFlag(t, w, tok, project, "late", "Z=1\n", false)
	id, _ = planOf(t, w, del, http.MethodDelete, envPath(project, "late"), `{"idempotency_key":"d3"}`)
	createImage(t, w, tok, sc.project.ID, "late-user", `{"kind":"service","image":"nginx","env_sets":["late"]}`)
	status, env, _ = mustCall(t, w, del, http.MethodPost, "/v1/confirmations/"+id, confirmBody("d3"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "env set late is used by entries late-user")
}

func TestEnvSetUsedBy(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	del := tokenKind(t, w, sc.owner, store.KindDelegated)
	createImage(t, w, tok, sc.project.ID, "zeta", `{"kind":"service","image":"nginx","env_sets":["plain"]}`)
	createImage(t, w, tok, sc.project.ID, "alpha", `{"kind":"service","image":"nginx","secret_env":{"P":"db.POSTGRES_PASSWORD"},"env_sets":["plain"]}`)
	for _, token := range []string{tok, del} {
		status, env, raw := mustCall(t, w, token, http.MethodGet, envSetsPath(project), "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		var body struct {
			Payload struct {
				Items []struct {
					Name   string   `json:"name"`
					UsedBy []string `json:"used_by"`
				} `json:"items"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, item := range body.Payload.Items {
			got[item.Name] = strings.Join(item.UsedBy, ",")
		}
		if got["plain"] != "alpha,zeta" || got["db"] != "alpha" {
			t.Fatalf("used_by %v", got)
		}
	}
	status, env, _ := mustCall(t, w, del, http.MethodGet, envPath(project, "db"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if used, _ := objectPayload(t, env)["used_by"].([]any); len(used) != 1 || used[0] != "alpha" {
		t.Fatalf("get used_by %v", objectPayload(t, env)["used_by"])
	}
	status, env, _ = mustCall(t, w, tok, http.MethodGet, envSetsPath(project)+"?include=everything", "")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", `include must be "private"`)
}

func TestValueRequestRotation(t *testing.T) {
	f := newVRFixture(t)
	// HAS is a secret key with a value: a request replaces it.
	id := f.create(t, f.mcp, "HAS", "")
	status, env := f.call(t, f.tok, http.MethodGet, reqPath(id)+"/fill", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if page := objectPayload(t, env); page["replace"] != true || page["entry"] != nil {
		t.Fatalf("page %v", page)
	}
	before, err := f.w.mem.ListEnvMaterial(t.Context(), f.sc.project.ID, "svc")
	if err != nil {
		t.Fatal(err)
	}
	status, env = f.call(t, f.tok, http.MethodPost, reqPath(id)+"/fill", `{"value":"`+u2SecretValue+`"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if out := objectPayload(t, env); out["state"] != "filled" || out["reveal_pending"] != nil {
		t.Fatalf("fill %v", out)
	}
	after, err := f.w.mem.ListEnvMaterial(t.Context(), f.sc.project.ID, "svc")
	if err != nil {
		t.Fatal(err)
	}
	if materialOf(before, "HAS") == materialOf(after, "HAS") {
		t.Fatal("value not replaced")
	}
	// A plain key with a value is still refused.
	status, env = f.call(t, f.mcp, http.MethodPost, vrPath(f.project, "svc"), `{"key":"PLAIN"}`)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "PLAIN already has a value; edit it in the console")
}

func materialOf(mats []store.EnvMaterial, key string) string {
	for _, m := range mats {
		if m.Key == key {
			return hex.EncodeToString(m.Ciphertext)
		}
	}
	return ""
}

func TestEntryPrivateSecrets(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	w.srv.ConsoleOrigin = "http://console.test"
	project := sc.project.ID.String()
	del := tokenKind(t, w, sc.owner, store.KindDelegated)

	// Console create: objects become "@web.KEY" refs in a private set.
	created := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","secret_env":{"TOKEN":{"fill":true},"SIGNING":{"generate":{"kind":"hex"}},"DB":"db.POSTGRES_PASSWORD"}}`)
	spec, _ := created["spec"].(map[string]any)
	secret, _ := spec["secret_env"].(map[string]any)
	if secret["TOKEN"] != "@web.TOKEN" || secret["SIGNING"] != "@web.SIGNING" || secret["DB"] != "db.POSTGRES_PASSWORD" {
		t.Fatalf("secret_env %v", secret)
	}
	status, env, raw := mustCall(t, w, tok, http.MethodGet, catalogItem(sc.project.ID, "web"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	var entry struct {
		Payload struct {
			PrivateKeys []map[string]any `json:"private_keys"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatal(err)
	}
	pk := entry.Payload.PrivateKeys
	if len(pk) != 2 || pk[0]["key"] != "SIGNING" || pk[0]["reveal_pending"] != true || pk[1]["key"] != "TOKEN" || pk[1]["needs_value"] != true || len(pk[0]) != 4 {
		t.Fatalf("private_keys %v", pk)
	}
	// Lists hide the private set unless asked.
	status, env, raw = mustCall(t, w, tok, http.MethodGet, envSetsPath(project), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if strings.Contains(raw, "@web") {
		t.Fatalf("private set listed %s", raw)
	}
	status, env, raw = mustCall(t, w, del, http.MethodGet, envSetsPath(project)+"?include=private", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if !strings.Contains(raw, `"name":"@web"`) || !strings.Contains(raw, `"entry":"web"`) {
		t.Fatalf("private set not listed %s", raw)
	}
	// No other entry may name it, and the set routes do not reach it.
	status, env, _ = mustCall(t, w, tok, http.MethodPost, "/v1/projects/"+project+"/catalog", `{"name":"other","kind":"image","spec":{"kind":"service","image":"nginx","secret_env":{"T":"@web.TOKEN"}}}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid secret_env")
	status, env, _ = mustCall(t, w, tok, http.MethodGet, envPath(project, "@web"), "")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid name")
	for _, bad := range []string{`{"fill":false}`, `{"fill":true,"generate":{"kind":"hex"}}`, `{"generate":{"kind":"rot13"}}`, `{"other":1}`} {
		status, env, _ = mustCall(t, w, tok, http.MethodPut, catalogItem(sc.project.ID, "web"), `{"kind":"image","spec":{"kind":"service","image":"nginx","secret_env":{"T":`+bad+`}}}`)
		if status != http.StatusBadRequest {
			t.Fatalf("%s: %d %s %q", bad, status, env.Code, env.Msg)
		}
	}

	// A value request for a private key goes through the entry.
	status, env, _ = mustCall(t, w, del, http.MethodPost, catalogItem(sc.project.ID, "web")+"/value-requests", `{"key":"TOKEN"}`)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	reqID, _ := objectPayload(t, env)["request_id"].(string)
	status, env, _ = mustCall(t, w, del, http.MethodGet, "/v1/value-requests/"+reqID, "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if objectPayload(t, env)["set"] != "@web" {
		t.Fatalf("request %v", objectPayload(t, env))
	}
	status, env, _ = mustCall(t, w, tok, http.MethodGet, "/v1/value-requests/"+reqID+"/fill", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if page := objectPayload(t, env); page["entry"] != "web" || page["replace"] != false {
		t.Fatalf("page %v", page)
	}
	status, env, _ = mustCall(t, w, tok, http.MethodPost, "/v1/value-requests/"+reqID+"/fill", `{"value":"`+u2SecretValue+`"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env, _ = mustCall(t, w, del, http.MethodPost, catalogItem(sc.project.ID, "web")+"/value-requests", `{"key":"NOPE"}`)
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", `entry web has no private key NOPE; add it to secret_env as {"fill": true} first`)
	status, env, _ = mustCall(t, w, del, http.MethodPost, catalogItem(sc.project.ID, "gone")+"/value-requests", `{"key":"TOKEN"}`)
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")

	// An update that drops TOKEN removes it; SIGNING keeps its value.
	sigBefore := privateMaterial(t, w, sc, "web", "SIGNING")
	status, env, _ = mustCall(t, w, tok, http.MethodPut, catalogItem(sc.project.ID, "web"), `{"kind":"image","spec":{"kind":"service","image":"nginx","secret_env":{"SIGNING":{"generate":{"kind":"hex"}}}}}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	names, _ := w.mem.ListEnvKeyNames(t.Context(), sc.project.ID)
	if strings.Join(names["@web"], ",") != "SIGNING" || privateMaterial(t, w, sc, "web", "SIGNING") != sigBefore {
		t.Fatalf("private keys after update %v", names["@web"])
	}
	// Deleting the entry deletes its private set.
	status, env, _ = mustCall(t, w, tok, http.MethodDelete, catalogItem(sc.project.ID, "web"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if _, err := w.mem.GetEnvSet(t.Context(), sc.project.ID, "@web"); err == nil {
		t.Fatal("private set left after delete")
	}

	// A program entry accepts the same forms (console).
	status, env, _ = mustCall(t, w, tok, http.MethodPost, "/v1/projects/"+project+"/catalog", `{"name":"prog","kind":"program","command":"pnpm run dev","dir":"/home/joe/app","run_as":"joe","secret_env":{"GH_TOKEN":{"fill":true}}}`)
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	if spec, _ := objectPayload(t, env)["spec"].(map[string]any); spec["secret_env"].(map[string]any)["GH_TOKEN"] != "@prog.GH_TOKEN" {
		t.Fatalf("program %v", objectPayload(t, env))
	}
}

func privateMaterial(t *testing.T, w *world, sc refsScene, entry, key string) string {
	t.Helper()
	mats, err := w.mem.ListEnvMaterial(t.Context(), sc.project.ID, catalog.PrivateSet(entry))
	if err != nil {
		t.Fatal(err)
	}
	return materialOf(mats, key)
}

func TestEntryPrivateSecretsDelegated(t *testing.T) {
	w, sc, _, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	del := tokenKind(t, w, sc.owner, store.KindDelegated)
	body := `{"name":"api","kind":"image","spec":{"kind":"service","image":"nginx","secret_env":{"TOKEN":{"fill":true},"KEY":{"generate":{"kind":"password","length":24}}}},"idempotency_key":"c1"}`
	id, plan := planOf(t, w, del, http.MethodPost, "/v1/projects/"+project+"/catalog", body)
	if !strings.HasSuffix(plan, "; new private secrets @api.KEY (generated password, 24 characters, secret), @api.TOKEN (empty; fill it with request_env_value)") {
		t.Fatalf("plan %q", plan)
	}
	if _, err := w.mem.GetEnvSet(t.Context(), sc.project.ID, "@api"); err == nil {
		t.Fatal("private set before confirm")
	}
	env, _ := confirmOK(t, w, del, id, "c1", http.StatusCreated)
	if objectPayload(t, env)["name"] != "api" {
		t.Fatalf("confirm %v", objectPayload(t, env))
	}
	set, err := w.mem.GetEnvSet(t.Context(), sc.project.ID, "@api")
	if err != nil || len(set.Keys) != 2 || set.Entry != "api" {
		t.Fatalf("private set %v %v", set, err)
	}
	// A delegated update that drops a key names it in the plan.
	upd := `{"kind":"image","spec":{"kind":"service","image":"nginx","secret_env":{"KEY":"@api.KEY"}},"idempotency_key":"u1"}`
	_, plan = planOf(t, w, del, http.MethodPut, catalogItem(sc.project.ID, "api"), upd)
	if !strings.HasSuffix(plan, "; removes private secrets @api.TOKEN") {
		t.Fatalf("update plan %q", plan)
	}
	// The delete plan says the private set goes too.
	_, plan = planOf(t, w, del, http.MethodDelete, catalogItem(sc.project.ID, "api"), `{"idempotency_key":"x1"}`)
	if !strings.HasSuffix(plan, "; its private env set @api (2 keys) is deleted") {
		t.Fatalf("delete plan %q", plan)
	}
}

func TestReadinessEnvPairs(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	putEnvFlag(t, w, tok, project, "google", "GOOGLE_CLIENT_SECRET=s\n", true)
	createImage(t, w, tok, sc.project.ID, "split", `{"kind":"service","image":"nginx","env":{"GOOGLE_CLIENT_ID":"id"},"env_sets":["google"]}`)
	createImage(t, w, tok, sc.project.ID, "together", `{"kind":"service","image":"nginx","env_sets":["google"]}`)
	_, checks, _ := readinessOf(t, w, tok, project, "split", "")
	c := findCheck(t, checks, "env_pairs")
	if c.Status != "warn" || c.Message != "GOOGLE_CLIENT_ID comes from the entry's env but GOOGLE_CLIENT_SECRET comes from env set google; keep a pair in one place" || c.Fix["action"] != "none" {
		t.Fatalf("split %+v", c)
	}
	_, checks, _ = readinessOf(t, w, tok, project, "together", "")
	if c := findCheck(t, checks, "env_pairs"); c.Status != "ok" {
		t.Fatalf("together %+v", c)
	}
	// Pairs never block a deploy.
	ready, _, _ := readinessOf(t, w, tok, project, "split", "")
	if !ready {
		t.Fatal("env_pairs made the entry not ready")
	}
}

func TestEnvPairSources(t *testing.T) {
	refs := catalog.EnvRefs{Sets: []string{"a", "b"}, Secret: map[string]string{"S_SECRET": "@e.S_SECRET"}, Inline: map[string]string{"I_ID": "x"}}
	src := envSources(refs, map[string][]string{"a": {"X_KEY_ID", "Y"}, "b": {"Y", "X_SECRET"}})
	if src["Y"] != "b" || src["X_KEY_ID"] != "a" || src["S_SECRET"] != "@e" || src["I_ID"] != "" {
		t.Fatalf("sources %v", src)
	}
}

func TestNetworkSnippet(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	path := "/v1/projects/" + project + "/hosts/" + sc.host.ID.String() + "/network-snippet"
	del := tokenKind(t, w, sc.owner, store.KindDelegated)
	status, env, _ := mustCall(t, w, del, http.MethodGet, path+"?network=dd&subnet=10.200.10.0/24", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	snippet, _ := objectPayload(t, env)["snippet"].(string)
	want := "shared_networks = [\"dd=10.200.10.0/24\"]\nshared_network_projects = [\"dd:" + sc.project.Slug + "\"]\n"
	if !strings.HasSuffix(snippet, want) || !strings.HasPrefix(snippet, "# ") || len(objectPayload(t, env)) != 1 {
		t.Fatalf("snippet %q", snippet)
	}
	for _, tc := range []struct{ query, msg string }{
		{"?network=DD&subnet=10.200.10.0/24", msgNetworkName},
		{"?subnet=10.200.10.0/24", msgNetworkName},
		{"?network=dd", msgNetworkSubnet},
		{"?network=dd&subnet=8.8.8.0/24", msgNetworkSubnet},
		{"?network=dd&subnet=10.200.10.1/24", msgNetworkSubnet},
		{"?network=dd&subnet=10.0.0.0/8", msgNetworkSubnet},
		{"?network=dd&subnet=10.200.10.0/29", msgNetworkSubnet},
	} {
		status, env, _ := mustCall(t, w, tok, http.MethodGet, path+tc.query, "")
		mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", tc.msg)
	}
	viewer := w.token(refsUser(t, w, "v@ex.com"))
	status, env, _ = mustCall(t, w, viewer, http.MethodGet, path+"?network=dd&subnet=10.200.10.0/24", "")
	if status != http.StatusForbidden {
		t.Fatalf("viewer %d %s", status, env.Code)
	}
	if !subnetOK("172.16.0.0/16") || !subnetOK("192.168.5.0/28") || subnetOK("172.32.0.0/16") || subnetOK("::1/128") {
		t.Fatal("subnet rule")
	}
}

func TestMyRequestsAndHandles(t *testing.T) {
	f := newVRFixture(t)
	id := f.create(t, f.mcp, "PH", "a note")
	status, env, raw := mustCall(t, f.w, f.tok, http.MethodGet, "/v1/me/requests", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	sum := sha256.Sum256([]byte(id))
	handle := hex.EncodeToString(sum[:])
	items, _ := objectPayload(t, env)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items %s", raw)
	}
	item, _ := items[0].(map[string]any)
	if item["type"] != "value" || item["handle"] != handle || item["subject"] != "svc.PH" || item["project_name"] != "Proj" || len(item) != 7 {
		t.Fatalf("item %v", item)
	}
	if strings.Contains(raw, id) || strings.Contains(raw, "a note") || strings.Contains(raw, "http") {
		t.Fatalf("list leaks the id, reason or url: %s", raw)
	}
	// Delegated sessions are refused.
	status, env, _ = mustCall(t, f.w, f.mcp, http.MethodGet, "/v1/me/requests", "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	// Another user sees nothing.
	status, env, _ = mustCall(t, f.w, f.adminT, http.MethodGet, "/v1/me/requests", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if items, _ := objectPayload(t, env)["items"].([]any); len(items) != 0 {
		t.Fatalf("admin items %v", items)
	}
	// The page routes take h-<handle> for the request's own user only.
	status, env = f.call(t, f.tok, http.MethodGet, reqPath("h-"+handle)+"/fill", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env = f.call(t, f.adminT, http.MethodGet, reqPath("h-"+handle)+"/fill", "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	// The agent read keeps ids only.
	status, env = f.call(t, f.mcp, http.MethodGet, reqPath("h-"+handle), "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, env = f.call(t, f.tok, http.MethodGet, reqPath("h-"+strings.ToUpper(handle))+"/fill", "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, env = f.call(t, f.tok, http.MethodPost, reqPath("h-"+handle)+"/fill", `{"value":"`+u2SecretValue+`"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	// A filled request leaves the list.
	status, env, _ = mustCall(t, f.w, f.tok, http.MethodGet, "/v1/me/requests", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if items, _ := objectPayload(t, env)["items"].([]any); len(items) != 0 {
		t.Fatalf("items after fill %v", items)
	}
}

// TestReadinessSpecPrivateForms is readiness from a spec with the v1.33
// item 6 secret_env objects: judged as the write path judges them, and a
// fill key shown as needing a value.
func TestReadinessSpecPrivateForms(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	del := tokenKind(t, w, sc.owner, store.KindDelegated)
	spec := `{"kind":"service","image":"nginx","env":{"URL":"http://${TOKEN}@x","G_CLIENT_ID":"id"},` +
		`"secret_env":{"TOKEN":{"fill":true},"KEY":{"generate":{"kind":"hex"}},"G_CLIENT_SECRET":{"fill":true},"DB":"db.POSTGRES_PASSWORD"}}`
	for _, token := range []string{tok, del} {
		status, env, raw := mustCall(t, w, token, http.MethodPost, specReadinessPath(project), specReadinessBody(spec, ""))
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		var body struct {
			Payload struct {
				Ready  bool             `json:"ready"`
				Checks []readinessCheck `json:"checks"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatal(err)
		}
		if body.Payload.Ready || !strings.HasPrefix(checkNames(body.Payload.Checks), "env_sets:ok,env_values:missing,env_values:missing,env_refs:ok,env_pairs:warn,files:ok,registry:") {
			t.Fatalf("checks %s", checkNames(body.Payload.Checks))
		}
		var keys []string
		for _, c := range body.Payload.Checks {
			if c.Check != "env_values" {
				continue
			}
			key, _ := c.Fix["key"].(string)
			keys = append(keys, key)
			if c.Message != draftPrivateMsg(key) || c.Fix["action"] != "fill_value" || c.Fix["set"] != nil {
				t.Fatalf("env_values %+v", c)
			}
		}
		if strings.Join(keys, ",") != "G_CLIENT_SECRET,TOKEN" {
			t.Fatalf("needs value %v", keys)
		}
		if c := findCheck(t, body.Payload.Checks, "env_pairs"); c.Message != "G_CLIENT_ID comes from the entry's env but G_CLIENT_SECRET comes from the entry's private set; keep a pair in one place" {
			t.Fatalf("pairs %+v", c)
		}
		if strings.Contains(raw, "@draft") || strings.Contains(raw, refPassword) {
			t.Fatalf("draft set name or a value leaked: %s", raw)
		}
	}
	// The same refusals as the write path.
	for _, tc := range []struct{ secret, msg string }{
		{`{"T":{"fill":false}}`, "invalid secret_env"},
		{`{"T":{"fill":true,"generate":{"kind":"hex"}}}`, "invalid secret_env"},
		{`{"T":{"other":1}}`, "invalid secret_env"},
		{`{"T":{"generate":{"kind":"rot13"}}}`, `generate.kind must be "password", "hex" or "base64url"`},
		{`{"T":{"generate":{"kind":"hex","length":20}}}`, "generate.length is only for kind password"},
		{`{"T":"@web.T"}`, "invalid secret_env"},
		{`{"T":"@draft.T"}`, "unknown env key"},
	} {
		spec := `{"kind":"service","image":"nginx","secret_env":` + tc.secret + `}`
		status, env, _ := mustCall(t, w, tok, http.MethodPost, specReadinessPath(project), specReadinessBody(spec, ""))
		mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", tc.msg)
		// The write path answers the same text (for an entry that is not web).
		status, env, _ = mustCall(t, w, tok, http.MethodPost, "/v1/projects/"+project+"/catalog", `{"name":"other","kind":"image","spec":`+spec+`}`)
		if tc.secret != `{"T":"@draft.T"}` {
			mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", tc.msg)
		}
	}
	// An entry named draft with a private set is never read for a spec.
	createImage(t, w, tok, sc.project.ID, "draft", `{"kind":"service","image":"nginx","secret_env":{"TOKEN":{"generate":{"kind":"hex"}}}}`)
	status, env, raw := mustCall(t, w, tok, http.MethodPost, specReadinessPath(project), specReadinessBody(`{"kind":"service","image":"nginx","secret_env":{"TOKEN":{"fill":true}}}`, ""))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if !strings.Contains(raw, draftPrivateMsg("TOKEN")) {
		t.Fatalf("stored @draft read: %s", raw)
	}
}

// Review A: a generated private key is shown once on the entry route.
func TestPrivateKeyReveal(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","secret_env":{"KEY":{"generate":{"kind":"hex"}},"TOKEN":{"fill":true}}}`)
	path := catalogItem(sc.project.ID, "web") + "/private-keys/KEY/reveal"
	del := tokenKind(t, w, sc.owner, store.KindDelegated)
	status, env, _ := mustCall(t, w, del, http.MethodPost, path, "")
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	status, env, raw := mustCall(t, w, tok, http.MethodPost, path, "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	body := objectPayload(t, env)
	if value, _ := body["value"].(string); body["key"] != "KEY" || len(value) != 64 || len(body) != 2 {
		t.Fatalf("reveal %s", raw)
	}
	status, env, _ = mustCall(t, w, tok, http.MethodPost, path, "")
	mustHTTP(t, status, env, http.StatusConflict, "conflict", revealConflictMsg("KEY"))
	status, env, _ = mustCall(t, w, tok, http.MethodPost, catalogItem(sc.project.ID, "web")+"/private-keys/TOKEN/reveal", "")
	mustHTTP(t, status, env, http.StatusConflict, "conflict", revealConflictMsg("TOKEN"))
	status, env, _ = mustCall(t, w, tok, http.MethodPost, catalogItem(sc.project.ID, "web")+"/private-keys/NOPE/reveal", "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, env, _ = mustCall(t, w, tok, http.MethodPost, catalogItem(sc.project.ID, "gone")+"/private-keys/KEY/reveal", "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, env, _ = mustCall(t, w, w.token(refsUser(t, w, "v@ex.com")), http.MethodPost, path, "")
	if status != http.StatusForbidden {
		t.Fatalf("viewer %d %s", status, env.Code)
	}
	// The private-keys view shows it is no longer pending.
	if pk := privateKeysOf(t, w, tok, sc, "web"); pk["KEY"]["reveal_pending"] != false {
		t.Fatalf("after reveal %v", pk)
	}
	found := 0
	for _, row := range auditRows(t, w, sc.project.ID) {
		if row.Action != audit.ActionEnvSetKeyReveal {
			continue
		}
		found++
		if d := detailMap(t, row.Detail); d["set"] != "@web" || d["key"] != "KEY" || len(d) != 2 {
			t.Fatalf("audit %v", d)
		}
	}
	if found != 1 {
		t.Fatalf("reveal audits %d", found)
	}
	_ = project
}

func privateKeysOf(t *testing.T, w *world, tok string, sc refsScene, entry string) map[string]map[string]any {
	t.Helper()
	status, env, raw := mustCall(t, w, tok, http.MethodGet, catalogItem(sc.project.ID, entry), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	var body struct {
		Payload struct {
			PrivateKeys []map[string]any `json:"private_keys"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, k := range body.Payload.PrivateKeys {
		out[k["key"].(string)] = k
	}
	return out
}

// Review C: a plan applies only to the set state it was made on.
func TestEnvSetPlanBoundToState(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	del := tokenKind(t, w, sc.owner, store.KindDelegated)
	putEnvFlag(t, w, tok, project, "app", "A=1\nB=2\n", false)
	edit := func(value string) {
		t.Helper()
		status, env, _ := mustCall(t, w, tok, http.MethodPut, envKeyPath(project, "app", "B"), `{"value":"`+value+`"}`)
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	}
	const changed = "env set app changed since the plan; ask for a new one"

	id, _ := planOf(t, w, del, http.MethodPatch, envPath(project, "app"), `{"set":{"A":"9"},"idempotency_key":"s1"}`)
	edit("3")
	status, env, _ := mustCall(t, w, del, http.MethodPost, "/v1/confirmations/"+id, confirmBody("s1"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", changed)
	if set, _ := w.mem.GetEnvSet(t.Context(), sc.project.ID, "app"); *set.Keys[0].Value != "1" {
		t.Fatal("refused plan wrote")
	}

	id, _ = planOf(t, w, del, http.MethodDelete, envPath(project, "app"), `{"idempotency_key":"s2"}`)
	edit("4")
	status, env, _ = mustCall(t, w, del, http.MethodPost, "/v1/confirmations/"+id, confirmBody("s2"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", changed)
	if _, err := w.mem.GetEnvSet(t.Context(), sc.project.ID, "app"); err != nil {
		t.Fatal("refused delete removed the set")
	}

	// An unchanged set still confirms.
	id, _ = planOf(t, w, del, http.MethodPatch, envPath(project, "app"), `{"set":{"A":"9"},"idempotency_key":"s3"}`)
	confirmOK(t, w, del, id, "s3", http.StatusOK)
}

// Review D: below deploy access the snippet is not_found.
func TestNetworkSnippetNeedsDeployAccess(t *testing.T) {
	w, sc, _, _ := refsWorld(t, allowImages())
	ctx := t.Context()
	outsider := w.user("m@ex.com", "M")
	other, err := w.mem.CreateProject(ctx, sc.project.OrgID, outsider.ID, "Metrics", "metrics", w.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.mem.PutHostAccess(ctx, sc.host.ID, other.ID, "metrics", sc.owner, w.now); err != nil {
		t.Fatal(err)
	}
	path := "/v1/projects/" + other.ID.String() + "/hosts/" + sc.host.ID.String() + "/network-snippet?network=dd"
	status, env, _ := mustCall(t, w, w.token(outsider.ID), http.MethodGet, path, "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	if _, _, err := w.mem.PutHostAccess(ctx, sc.host.ID, other.ID, "deploy", sc.owner, w.now); err != nil {
		t.Fatal(err)
	}
	status, env, _ = mustCall(t, w, w.token(outsider.ID), http.MethodGet, path, "")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgNetworkSubnet)
}

// Review E: a rotation is audited with replaced.
func TestValueRequestRotationAudit(t *testing.T) {
	f := newVRFixture(t)
	for _, key := range []string{"HAS", "PH"} {
		id := f.create(t, f.mcp, key, "")
		status, env := f.call(t, f.tok, http.MethodPost, reqPath(id)+"/fill", `{"value":"`+u2SecretValue+`"}`)
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	}
	got := map[string]any{}
	for _, row := range f.w.mem.AllAudits() {
		if row.Action != audit.ActionEnvSetKeyPut {
			continue
		}
		d := detailMap(t, row.Detail)
		if d["via"] == "value_request" {
			got[d["key"].(string)] = d["replaced"]
		}
	}
	if got["HAS"] != true || got["PH"] != nil || len(got) != 2 {
		t.Fatalf("replaced %v", got)
	}
}

// TestEnvSetPatchMakeSecretEmpty is spec-release-v0239 F6 on the v1.33
// update path: make_secret on a plain key whose value is empty is refused at
// plan time, naming the key, and nothing changes. A key with a value, and a
// needs_value placeholder (it stays a placeholder), may still be made secret.
func TestEnvSetPatchMakeSecretEmpty(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	del := tokenKind(t, w, sc.owner, store.KindDelegated)
	putEnvFlag(t, w, tok, project, "app", "EMPTY=\nFULL=f\n", false)

	status, env, _ := mustCall(t, w, del, http.MethodPatch, envPath(project, "app"), `{"make_secret":["FULL","EMPTY"],"idempotency_key":"e1"}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "a secret value cannot be empty; give EMPTY a value first")
	status, env, _ = mustCall(t, w, tok, http.MethodGet, envPath(project, "app"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if keys := keysByName(t, env); keys["EMPTY"]["is_secret"] != false || keys["FULL"]["is_secret"] != false {
		t.Fatalf("refused plan changed keys %v", keys)
	}

	id, _ := planOf(t, w, del, http.MethodPatch, envPath(project, "app"), `{"make_secret":["FULL"],"idempotency_key":"e2"}`)
	env, _ = confirmOK(t, w, del, id, "e2", http.StatusOK)
	if keysByName(t, env)["FULL"]["is_secret"] != true {
		t.Fatal("valued key not made secret")
	}

	// A plain placeholder may become a secret placeholder.
	status, env, _ = mustCall(t, w, del, http.MethodPost, envSetsPath(project), `{"name":"ph","keys":[{"key":"PH","is_secret":false}],"idempotency_key":"e3"}`)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	confirmOK(t, w, del, objectPayload(t, env)["confirmation_id"].(string), "e3", http.StatusCreated)
	id, _ = planOf(t, w, del, http.MethodPatch, envPath(project, "ph"), `{"make_secret":["PH"],"idempotency_key":"e4"}`)
	env, _ = confirmOK(t, w, del, id, "e4", http.StatusOK)
	if k := keysByName(t, env)["PH"]; k["is_secret"] != true || k["needs_value"] != true {
		t.Fatalf("placeholder %v", k)
	}
}

// emptySecretStore stages a value emptied between the confirm pre-check and
// the store's locked read: ApplyEnvSetChange refuses with EmptySecretKeyError.
type emptySecretStore struct {
	store.Store
}

func (emptySecretStore) ApplyEnvSetChange(context.Context, uuid.UUID, string, store.EnvSetChange, []byte, store.EnvSealer, uuid.UUID, time.Time) (store.EnvSet, error) {
	return store.EnvSet{}, &store.EmptySecretKeyError{Key: "FULL"}
}

// TestEnvSetPatchConfirmSecretEmpty: an ErrSecretEmpty from the store at
// confirm is bad_request with its message, not an internal error.
func TestEnvSetPatchConfirmSecretEmpty(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	project := sc.project.ID.String()
	del := tokenKind(t, w, sc.owner, store.KindDelegated)
	putEnvFlag(t, w, tok, project, "app", "FULL=f\n", false)
	id, _ := planOf(t, w, del, http.MethodPatch, envPath(project, "app"), `{"make_secret":["FULL"],"idempotency_key":"z1"}`)
	w.srv.Store = emptySecretStore{w.srv.Store}
	status, env, _ := mustCall(t, w, del, http.MethodPost, "/v1/confirmations/"+id, confirmBody("z1"))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "a secret value cannot be empty; give FULL a value first")
}
