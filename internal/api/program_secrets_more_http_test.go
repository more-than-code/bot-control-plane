package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/more-than-code/bot-api-server/internal/agentws"
	"github.com/more-than-code/bot-api-server/internal/catalog"
)

// api-v1 v1.22 item 2 (spec criterion 6), deleting a key: an entry that
// uses a set only through env_sets re-hashes and loses its shared-host
// approval when a key is deleted from the set; the set cannot be deleted
// while the entry uses it.
func TestProgramEnvSetsOnlyKeyDelete(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	guestUser := w.user("guest@ex.com", "Guest")
	guest, err := w.mem.CreateProject(context.Background(), guestUser.ID, "Guest", "guest", w.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.mem.PutHostShare(context.Background(), sc.host.ID, guest.ID, "deploy", owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	gtok := w.token(guestUser.ID)
	gpath := guest.ID.String()
	putEnv(t, w, gtok, gpath, "gh", "GH_TOKEN="+progSecret+"\nGH_HOST=github.com\n")
	status, env := w.do(gtok, http.MethodPost, "/v1/projects/"+gpath+"/catalog", progBody("gh", map[string]any{"env_sets": []string{"gh"}}))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	created := objectPayload(t, env)
	hash, _ := created["spec_hash"].(string)
	entryID, _ := created["id"].(string)
	host := sc.host.ID.String()
	status, env, _ = mustCall(t, w, w.token(owner.ID), http.MethodPost, "/v1/hosts/"+host+"/catalog-approvals", `{"entry_id":"`+entryID+`","spec_hash":"`+hash+`"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")

	status, env, _ = mustCall(t, w, gtok, http.MethodDelete, envKeyPath(gpath, "gh", "GH_HOST"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env = w.do(gtok, http.MethodGet, "/v1/projects/"+gpath+"/catalog/gh", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	got := objectPayload(t, env)["spec_hash"]
	prog := catalog.Program{
		Dir: "/home/joe/app", Exec: "gh", Args: []string{"api", "user"}, RunAs: "joe",
		Env: map[string]string{}, EnvSets: []string{"gh"}, SecretEnv: map[string]string{}, Restart: "on-failure",
	}
	want := catalog.ProgramHash(prog, catalog.SetState{Keys: map[string][]string{"gh": {"GH_TOKEN"}}})
	if got == hash || got != want {
		t.Fatalf("hash after key delete %v, was %s, want %s", got, hash, want)
	}
	if n := len(listApprovals(t, w, owner.ID, sc.host.ID)); n != 0 {
		t.Fatalf("approvals after key delete %d", n)
	}
	status, env, _ = mustCall(t, w, gtok, http.MethodDelete, envPath(gpath, "gh"), "")
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "conflict")
}

// api-v1 v1.23 item 2 ordering: on a shared host the approval check comes
// first, so an unapproved guest Run waits for approval even when the host's
// list does not include the guest.
func TestProjectsPreCheckAfterApproval(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	guestUser := w.user("guest@ex.com", "Guest")
	guest, err := w.mem.CreateProject(context.Background(), guestUser.ID, "Guest", "guest", w.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.mem.PutHostShare(context.Background(), sc.host.ID, guest.ID, "deploy", owner.ID, w.now); err != nil {
		t.Fatal(err)
	}
	gtok := w.token(guestUser.ID)
	gpath := guest.ID.String()
	created := createProgram(t, w, gtok, guest.ID, "tra", "pnpm run dev", false)
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, secretsPrograms(false, []string{"proj"}), nil)
	log := watchTasks(t, conn, succeededTask)
	host := sc.host.ID.String()
	status, env := postAct(w, gtok, gpath, "tra", "run", host, "ord-1", nil)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "waiting for the host owner's approval")
	hash, _ := created["spec_hash"].(string)
	entryID, _ := created["id"].(string)
	status, env, _ = mustCall(t, w, w.token(owner.ID), http.MethodPost, "/v1/hosts/"+host+"/catalog-approvals", `{"entry_id":"`+entryID+`","spec_hash":"`+hash+`"}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env = postAct(w, gtok, gpath, "tra", "run", host, "ord-2", nil)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "this host's allowlist does not include project guest")
	if log.count() != 0 {
		t.Fatalf("tasks %d", log.count())
	}
}

// api-v1 v1.23: an image rollback is pre-checked like deploy, and an agent
// policy_denied on an image deploy reaches the client with the runner.toml
// message.
func TestProjectsImageRollbackAndDenied(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)

	unlisted := secretsPrograms(false, []string{"other"})
	for k, v := range allowImages() {
		unlisted[k] = v
	}
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, unlisted, nil)
	log := watchTasks(t, conn, succeededTask)
	digest := "sha256:" + strings.Repeat("a", 64)
	status, env, _ := postImage(t, w, tok, project, "web", "rollback", host, "rb-1", map[string]any{"digest": digest})
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "this host's allowlist does not include project proj")
	if log.count() != 0 {
		t.Fatalf("tasks %d", log.count())
	}
	_ = conn.CloseNow()

	listed := secretsPrograms(false, []string{"proj"})
	for k, v := range allowImages() {
		listed[k] = v
	}
	conn = helloAgent(t, w, sc.host, sc.credPlain, sc.credID, listed, nil)
	log = watchTasks(t, conn, func(msg map[string]any) map[string]any {
		return map[string]any{"v": 1, "type": "task_reject", "task_id": msg["task_id"], "error_code": "policy_denied"}
	})
	status, env, raw := postImage(t, w, tok, project, "web", "deploy", host, "pd-1", nil)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", agentws.PolicyDenied)
	if log.count() != 1 || strings.Contains(raw, "policy_denied") {
		t.Fatalf("tasks %d body %s", log.count(), raw)
	}
}

// api-v1 v1.22 items 3 and 6: secret_env sits between env_sets and inline
// env; an entry without sets sends the v1.6 frame to a new agent too; stop,
// restart and status lines are redacted.
func TestProgramSecretsPrecedenceAndShortActions(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	path := "/v1/projects/" + project + "/catalog"
	// base: A plain from the set; B overridden by secret_env; C overridden by inline env.
	putEnvFlag(t, w, tok, project, "base", "A=from-set\nB=from-set\nC=from-set\n", false)
	putEnv(t, w, tok, project, "lock", "TOKEN="+progSecret+"\n")
	status, env := w.do(tok, http.MethodPost, path, progBody("mix", map[string]any{
		"env_sets":   []string{"base"},
		"secret_env": map[string]string{"B": "lock.TOKEN", "C": "lock.TOKEN"},
		"env":        map[string]string{"C": "inline"},
	}))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	createProgram(t, w, tok, sc.project.ID, "plain", "FOO=bar pnpm run dev", false)

	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, secretsPrograms(true, []string{"proj"}), nil)
	log := watchTasks(t, conn, runReply("got "+progSecret))
	status, env, _ = postImageLike(t, w, tok, project, "mix", "run", host, "mix-1")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	frame, _ := log.raw(0)["spec"].(map[string]any)
	fenv, _ := frame["env"].(map[string]any)
	if fenv["A"] != "from-set" || fenv["B"] != progSecret || fenv["C"] != "inline" {
		t.Fatalf("precedence %#v", fenv)
	}
	if sk := stringList(frame["secret_keys"]); len(sk) != 1 || sk[0] != "B" {
		t.Fatalf("secret_keys %#v", frame["secret_keys"])
	}

	status, env, _ = postImageLike(t, w, tok, project, "plain", "run", host, "plain-1")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	plain, _ := log.raw(1)["spec"].(map[string]any)
	if _, ok := plain["secret_keys"]; ok {
		t.Fatalf("an entry without sets sent secret_keys to a new agent: %#v", plain)
	}

	want := "got " + catalog.SecretPreview(progSecret)
	for i, action := range []string{"stop", "restart", "status"} {
		status, env, raw := postImageLike(t, w, tok, project, "mix", action, host, "short-"+action)
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		lines := stringList(objectPayload(t, env)["lines"])
		if strings.Contains(raw, progSecret) || len(lines) != 1 || lines[0] != want {
			t.Fatalf("%s (%d) lines %#v", action, i, lines)
		}
	}
}

// api-v1 v1.22 item 6: a failed Run of an entry with secrets is redacted in
// every string of the error envelope, not only in lines.
func TestProgramSecretsErrorEnvelope(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	putEnv(t, w, tok, project, "lock", "TOKEN="+progSecret+"\n")
	status, env := w.do(tok, http.MethodPost, "/v1/projects/"+project+"/catalog", progBody("gh", map[string]any{
		"secret_env": map[string]string{"GH_TOKEN": "lock.TOKEN"},
	}))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, secretsPrograms(true, nil), nil)
	watchTasks(t, conn, func(msg map[string]any) map[string]any {
		return map[string]any{
			"v": 1, "type": "task_result", "task_id": msg["task_id"], "state": "failed", "error_code": "start_failed",
			"payload": map[string]any{"unit": "u-" + progSecret, "lines": []string{"got " + progSecret}},
		}
	})
	status, env, raw := postImageLike(t, w, tok, project, "gh", "run", sc.host.ID.String(), "err-1")
	mustHTTP(t, status, env, http.StatusInternalServerError, "error", "start_failed")
	if strings.Contains(raw, progSecret) || !strings.Contains(raw, catalog.SecretPreview(progSecret)) {
		t.Fatalf("error envelope %s", raw)
	}
}
