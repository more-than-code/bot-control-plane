package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

func regeneratePath(project, set, key string) string {
	return envKeyPath(project, set, key) + "/regenerate"
}

// TestEnvKeyRegenerate (api-v1 v1.54): a secret key, generated or typed,
// gets a new generated value that is shown once through reveal; the answer
// names the entries that use the set; the value is in no answer or audit
// row; plain, missing and delegated calls change nothing.
func TestEnvKeyRegenerate(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	admin := w.user("a@ex.com", "A")
	viewer := w.user("v@ex.com", "V")
	sc := w.scene(owner, admin, viewer, w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	genKey(t, w, tok, project, "svc", "PW", `{"kind":"password"}`)
	first := reveal(t, w, tok, project, "svc", "PW")
	putKey(t, w, tok, project, "svc", "TYPED", "typed-secret-value-0123", nil)
	putKey(t, w, tok, project, "svc", "PLAIN", "visible", boolPtr(false))
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","env_sets":["svc"]}`)
	createImage(t, w, tok, sc.project.ID, "api", `{"kind":"service","image":"nginx","secret_env":{"PW":"svc.PW"}}`)

	// Refusals: delegated, viewer, plain key, unknown set or key, no generate, bad kind.
	delegated := tokenKind(t, w, owner.ID, store.KindDelegated)
	status, env, _ := mustCall(t, w, delegated, http.MethodPost, regeneratePath(project, "svc", "PW"), `{"generate":{"kind":"hex"}}`)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	status, env, _ = mustCall(t, w, w.token(viewer.ID), http.MethodPost, regeneratePath(project, "svc", "PW"), `{"generate":{"kind":"hex"}}`)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	status, env, _ = mustCall(t, w, tok, http.MethodPost, regeneratePath(project, "svc", "PLAIN"), `{"generate":{"kind":"hex"}}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "PLAIN is not secret; edit it to change it")
	status, env, _ = mustCall(t, w, tok, http.MethodPost, regeneratePath(project, "svc", "NOPE"), `{"generate":{"kind":"hex"}}`)
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, env, _ = mustCall(t, w, tok, http.MethodPost, regeneratePath(project, "nope", "PW"), `{"generate":{"kind":"hex"}}`)
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, env, _ = mustCall(t, w, tok, http.MethodPost, regeneratePath(project, "svc", "PW"), `{}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "send generate")
	status, env, _ = mustCall(t, w, tok, http.MethodPost, regeneratePath(project, "svc", "PW"), `{"generate":{"kind":"rot13"}}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", `generate.kind must be "password", "hex" or "base64url"`)
	if n := countAction(w.mem.AllAudits(), audit.ActionEnvSetKeyRegenerate); n != 0 {
		t.Fatalf("refused regenerate audited %d", n)
	}
	if _, err := w.mem.GetEnvSet(t.Context(), sc.project.ID, "nope"); err == nil {
		t.Fatal("a refused regenerate created the set")
	}

	// An admin regenerates the forgotten generated value.
	status, env, raw := mustCall(t, w, w.token(admin.ID), http.MethodPost, regeneratePath(project, "svc", "PW"), `{"generate":{"kind":"hex"}}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	body := objectPayload(t, env)
	if body["key"] != "PW" || body["is_secret"] != true || body["preview"] != bullets || body["reveal_pending"] != true || body["generated"] != true {
		t.Fatalf("regenerate %s", raw)
	}
	used, _ := body["used_by"].([]any)
	if len(used) != 2 || used[0] != "api" || used[1] != "web" {
		t.Fatalf("used_by %v", body["used_by"])
	}
	second := reveal(t, w, tok, project, "svc", "PW")
	if len(second) != 64 || second == first || strings.Contains(raw, second) {
		t.Fatal("regenerated value wrong or leaked")
	}
	status, env, _ = mustCall(t, w, tok, http.MethodPost, revealPath(project, "svc", "PW"), "")
	mustHTTP(t, status, env, http.StatusConflict, "conflict", revealConflictMsg("PW"))

	// v1.56: a typed (hand-entered) secret is never regenerated: a random
	// value would destroy a provider's key. It stays as it was.
	status, env, _ = mustCall(t, w, tok, http.MethodPost, regeneratePath(project, "svc", "TYPED"), `{"generate":{"kind":"password","length":20}}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "TYPED was entered by hand, so it cannot be regenerated; edit it to paste a new value")
	set, err := w.mem.GetEnvSet(t.Context(), sc.project.ID, "svc")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range set.Keys {
		if k.Key == "TYPED" && (k.Generated || k.RevealPending || !k.IsSecret) {
			t.Fatalf("typed key changed: %+v", k)
		}
		if k.Key == "PW" && !k.Generated {
			t.Fatalf("generated key lost its flag: %+v", k)
		}
	}

	// v1.56: the key PUT answer says whether the value is generated, and a
	// typed edit of a generated key clears it, so it is no longer regenerable.
	status, env, _ = mustCall(t, w, tok, http.MethodPut, envKeyPath(project, "svc", "GEN2"), `{"generate":{"kind":"hex"}}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if objectPayload(t, env)["generated"] != true {
		t.Fatalf("generate PUT answer %v", objectPayload(t, env))
	}
	status, env, _ = mustCall(t, w, tok, http.MethodPut, envKeyPath(project, "svc", "GEN2"), `{"value":"typed-over-generated-0123"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if objectPayload(t, env)["generated"] != false {
		t.Fatalf("typed PUT answer %v", objectPayload(t, env))
	}
	status, env, _ = mustCall(t, w, tok, http.MethodPost, regeneratePath(project, "svc", "GEN2"), `{"generate":{"kind":"hex"}}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "GEN2 was entered by hand, so it cannot be regenerated; edit it to paste a new value")

	rows := 0
	for _, a := range w.mem.AllAudits() {
		if a.Action != audit.ActionEnvSetKeyRegenerate {
			continue
		}
		rows++
		detail := string(a.Detail)
		if strings.Contains(detail, second) || strings.Contains(detail, first) {
			t.Fatal("audit holds a value")
		}
		if !strings.Contains(detail, `"set":"svc"`) || !strings.Contains(detail, `"kind":`) {
			t.Fatalf("audit detail %s", detail)
		}
	}
	if rows != 1 {
		t.Fatalf("regenerate audits %d", rows)
	}
}

// TestPrivateKeyRegenerate: the private-keys route regenerates a key of the
// entry's own set, and the answer names that entry.
func TestPrivateKeyRegenerate(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","secret_env":{"KEY":{"generate":{"kind":"hex"}}}}`)
	base := catalogItem(sc.project.ID, "web") + "/private-keys/KEY"
	status, env, _ := mustCall(t, w, tok, http.MethodPost, base+"/reveal", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	del := tokenKind(t, w, sc.owner, store.KindDelegated)
	status, env, _ = mustCall(t, w, del, http.MethodPost, base+"/regenerate", `{"generate":{"kind":"hex"}}`)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "forbidden")
	status, env, raw := mustCall(t, w, tok, http.MethodPost, base+"/regenerate", `{"generate":{"kind":"base64url"}}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	body := objectPayload(t, env)
	used, _ := body["used_by"].([]any)
	if body["reveal_pending"] != true || len(used) != 1 || used[0] != "web" {
		t.Fatalf("private regenerate %s", raw)
	}
	status, env, raw = mustCall(t, w, tok, http.MethodPost, base+"/reveal", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if v, _ := objectPayload(t, env)["value"].(string); len(v) != 43 {
		t.Fatalf("reveal %s", raw)
	}
	status, env, _ = mustCall(t, w, tok, http.MethodPost, catalogItem(sc.project.ID, "web")+"/private-keys/NOPE/regenerate", `{"generate":{"kind":"hex"}}`)
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
}
