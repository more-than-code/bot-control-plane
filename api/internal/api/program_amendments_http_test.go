package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// amendSentinel is a throwaway secret value; it must never appear in an
// approval view.
const amendSentinel = "sentinel-SECRET-value-4b2d"

// sharedGuest is a guest project with a deploy share on sc.host.
func sharedGuest(t *testing.T, w *world, sc scene, owner store.User) (string, uuid.UUID) {
	t.Helper()
	guestUser := w.user("guest@ex.com", "Guest")
	guest, err := w.mem.CreateProject(context.Background(), w.mem.PersonalOrg(guestUser.ID), guestUser.ID, "Guest", "guest", w.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.share(context.Background(), sc.host.ID, guest.ID, "deploy", owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	return w.token(guestUser.ID), guest.ID
}

func entryHash(t *testing.T, w *world, tok, project, name string) string {
	t.Helper()
	status, env := w.do(tok, http.MethodGet, "/v1/projects/"+project+"/catalog/"+name, "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	h, _ := objectPayload(t, env)["spec_hash"].(string)
	return h
}

// api-v1 v1.22 amendments 2 and 3: the extended list for secret values, on
// write and at Run, and the plain secret-looking gate, before any task and
// before the idempotency claim.
func TestProgramSecretValueGates(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	path := "/v1/projects/" + project + "/catalog"
	putEnv(t, w, tok, project, "lock", "GIT_SSH_COMMAND=ssh -i /tmp/k\nHTTPS_PROXY=http://p:3128\nTOKEN_A="+progSecret+"\n")
	putEnvFlag(t, w, tok, project, "plain", "HTTPS_PROXY=http://plain:3128\nAPI_TOKEN=not-marked-123\n", false)

	// Write: a secret_env key in the extended list.
	status, env := w.do(tok, http.MethodPost, path, progBody("bad", map[string]any{"secret_env": map[string]string{"GIT_SSH_COMMAND": "lock.TOKEN_A"}}))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "this variable changes what the program runs")
	status, env = w.do(tok, http.MethodPost, path, progBody("bad", map[string]any{"secret_env": map[string]string{"https_proxy": "lock.TOKEN_A"}}))
	if status != http.StatusBadRequest {
		t.Fatalf("lowercase proxy key %d %s", status, env.Msg)
	}

	// Run: an extended-list key whose final value is a secret set key.
	status, env = w.do(tok, http.MethodPost, path, progBody("viaset", map[string]any{"env_sets": []string{"lock"}}))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	// A plain set key in the extended list is allowed (its value is approved).
	status, env = w.do(tok, http.MethodPost, path, progBody("proxy", map[string]any{"env_sets": []string{"plain"}, "env": map[string]string{"API_TOKEN_ID": "1"}}))
	if status != http.StatusBadRequest || env.Msg != "use secret_env" {
		t.Fatalf("inline secret-looking key %d %s", status, env.Msg)
	}
	putEnvFlag(t, w, tok, project, "proxyonly", "HTTPS_PROXY=http://plain:3128\n", false)
	status, env = w.do(tok, http.MethodPost, path, progBody("proxy", map[string]any{"env_sets": []string{"proxyonly"}}))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	status, env = w.do(tok, http.MethodPost, path, progBody("looks", map[string]any{"env_sets": []string{"plain"}}))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")

	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, secretsPrograms(true, []string{"proj"}), nil)
	log := watchTasks(t, conn, succeededTask)
	status, env, _ = postImageLike(t, w, tok, project, "viaset", "run", host, "g-1")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "this variable changes what the program runs")
	status, env, _ = postImageLike(t, w, tok, project, "proxy", "run", host, "g-2")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if log.count() != 1 {
		t.Fatalf("tasks %d, want only the plain proxy run", log.count())
	}

	// Plain secret-looking set key: refused, audited, nothing sent, no claim.
	status, env, _ = postImageLike(t, w, tok, project, "looks", "run", host, "g-3")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "API_TOKEN looks like a secret but is not marked secret in set plain; mark it secret")
	if log.count() != 1 {
		t.Fatal("the secret-looking gate sent a task")
	}
	denied := 0
	for _, row := range auditRows(t, w, sc.project.ID) {
		var detail map[string]any
		_ = json.Unmarshal(row.Detail, &detail)
		if row.Action == audit.ActionProgramRun && row.Decision == audit.DecisionDenied && strings.Contains(detail["reason"].(string), "mark it secret") {
			denied++
		}
	}
	if denied != 1 {
		t.Fatalf("denied program.run rows %d", denied)
	}
	// The same idempotency key works once the key is marked secret, so the
	// refusal claimed nothing.
	status, env, _ = markSecret(t, w, tok, project, "plain", "API_TOKEN")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env, _ = postImageLike(t, w, tok, project, "looks", "run", host, "g-3")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if log.count() != 2 {
		t.Fatalf("tasks %d after marking the key secret", log.count())
	}
}
