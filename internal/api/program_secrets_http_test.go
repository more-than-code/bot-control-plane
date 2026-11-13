package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/more-than-code/bot-api-server/internal/audit"
	"github.com/more-than-code/bot-api-server/internal/catalog"
	"github.com/more-than-code/bot-api-server/internal/store"
)

// progSecret is a throwaway value for these tests; it is never a real token.
const progSecret = "gh-test-TOKEN-value-7c1e"

func progBody(name string, extra map[string]any) string {
	body := map[string]any{
		"name": name, "kind": "program", "command": "gh api user", "shell": false,
		"dir": "/home/joe/app", "run_as": "joe",
	}
	for k, v := range extra {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

// secretsPrograms is the hello programs object of an agent at agent-protocol
// v1.11/v1.12. projects nil leaves the field out (an older agent).
func secretsPrograms(secrets bool, projects []string) map[string]any {
	p := allowPrograms(false)
	if secrets {
		p["secrets"] = true
	}
	if projects != nil {
		p["projects"] = projects
	}
	return p
}

func runReply(lines ...string) func(map[string]any) map[string]any {
	return func(msg map[string]any) map[string]any {
		return map[string]any{
			"v": 1, "type": "task_result", "task_id": msg["task_id"], "state": "succeeded",
			"payload": map[string]any{
				"unit": "deploybot-proj-gh.service", "active_state": "active", "sub_state": "running",
				"main_pid": 1, "started_at": nil, "ports": []int{}, "lines": lines,
			},
		}
	}
}

// api-v1 v1.22 items 1 and 2: the write body takes env_sets and secret_env;
// inline env keeps "use secret_env"; the stored spec and the hash.
func TestProgramSecretsWrite(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	path := "/v1/projects/" + project + "/catalog"
	putEnv(t, w, tok, project, "gh", "GH_TOKEN="+progSecret+"\n")
	putEnvFlag(t, w, tok, project, "cfg", "GH_HOST=github.com\n", false)

	status, env := w.do(tok, http.MethodPost, path, progBody("bad", map[string]any{"env": map[string]string{"GH_TOKEN": "x"}}))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "use secret_env")
	status, env = w.do(tok, http.MethodPost, path, progBody("bad", map[string]any{"env_sets": []string{"nope"}}))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "unknown env set")
	status, env = w.do(tok, http.MethodPost, path, progBody("bad", map[string]any{"secret_env": map[string]string{"T": "gh.NOPE"}}))
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "unknown env key")
	status, env = w.do(tok, http.MethodPost, path, `{"kind":"image","name":"img","env_sets":["gh"],"spec":{"kind":"service","image":"nginx"}}`)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "invalid json")

	status, env = w.do(tok, http.MethodPost, path, progBody("gh", map[string]any{
		"env_sets": []string{"cfg"}, "secret_env": map[string]string{"GH_TOKEN": "gh.GH_TOKEN"},
	}))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	created := objectPayload(t, env)
	spec, _ := created["spec"].(map[string]any)
	if sets := stringList(spec["env_sets"]); len(sets) != 1 || sets[0] != "cfg" {
		t.Fatalf("env_sets %#v", spec["env_sets"])
	}
	if se, _ := spec["secret_env"].(map[string]any); se["GH_TOKEN"] != "gh.GH_TOKEN" {
		t.Fatalf("secret_env %#v", spec["secret_env"])
	}
	prog := catalog.Program{
		Dir: "/home/joe/app", Exec: "gh", Args: []string{"api", "user"}, RunAs: "joe",
		Env: map[string]string{}, EnvSets: []string{"cfg"}, SecretEnv: map[string]string{"GH_TOKEN": "gh.GH_TOKEN"},
		Restart: "on-failure",
	}
	want := catalog.ProgramHash(prog, map[string]string{
		"cfg": catalog.EnvKeyDigest([]string{"GH_HOST"}), "gh": catalog.EnvKeyDigest([]string{"GH_TOKEN"}),
	})
	if created["spec_hash"] != want {
		t.Fatalf("spec_hash %v want %s", created["spec_hash"], want)
	}

	plain := createProgram(t, w, tok, sc.project.ID, "plain", "pnpm run dev", false)
	plainSpec, _ := plain["spec"].(map[string]any)
	if sets, ok := plainSpec["env_sets"].([]any); !ok || len(sets) != 0 {
		t.Fatalf("plain env_sets %#v", plainSpec["env_sets"])
	}
	if se, ok := plainSpec["secret_env"].(map[string]any); !ok || len(se) != 0 {
		t.Fatalf("plain secret_env %#v", plainSpec["secret_env"])
	}
	plainProg := catalog.Program{Dir: "/home/joe/app", Exec: "pnpm", Args: []string{"run", "dev"}, RunAs: "joe", Env: map[string]string{}, Restart: "on-failure"}
	if plain["spec_hash"] != catalog.SpecHash(plainProg) {
		t.Fatal("an entry without sets must keep its v1.6 hash")
	}

	// v1.22 item 2: deleting a set a program uses is conflict.
	status, env, _ = mustCall(t, w, tok, http.MethodDelete, envPath(project, "gh"), "")
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "conflict")
}

// api-v1 v1.22 items 3, 4 and 6: the frame, the gates and the redaction.
func TestProgramSecretsRun(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	path := "/v1/projects/" + project + "/catalog"
	putEnv(t, w, tok, project, "gh", "GH_TOKEN="+progSecret+"\nOVER=secret-but-overridden-1\n")
	putEnvFlag(t, w, tok, project, "cfg", "GH_HOST=github.com\n", false)
	status, env := w.do(tok, http.MethodPost, path, progBody("gh", map[string]any{
		"env_sets": []string{"cfg", "gh"}, "env": map[string]string{"OVER": "inline"},
	}))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	createProgram(t, w, tok, sc.project.ID, "plain", "FOO=bar pnpm run dev", false)

	// Older agent: no programs.secrets. Refused before any task; entries
	// without sets keep the v1.6 frame.
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowPrograms(false), nil)
	log := watchTasks(t, conn, succeededTask)
	status, env = postAct(w, tok, project, "gh", "run", host, "old-1", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "this host's agent does not support program secrets")
	if log.count() != 0 {
		t.Fatalf("older agent got %d tasks", log.count())
	}
	status, env = postAct(w, tok, project, "plain", "run", host, "old-2", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	frame, _ := log.raw(0)["spec"].(map[string]any)
	keys := make([]string, 0, len(frame))
	for k := range frame {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "args,dir,entry,env,exec,project,restart,run_as,shell" {
		t.Fatalf("v1.6 frame keys %v", keys)
	}
	_ = conn.CloseNow()

	// Agent with programs.secrets.
	conn = helloAgent(t, w, sc.host, sc.credPlain, sc.credID, secretsPrograms(true, nil), nil)
	// No keyword in the line, so only the exact-value redaction can hide it.
	echo := "got " + progSecret + " and " + progSecret
	log = watchTasks(t, conn, runReply(echo))
	status, env, raw := postImageLike(t, w, tok, project, "gh", "run", host, "new-1")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if strings.Contains(raw, progSecret) {
		t.Fatalf("run response leaked the value: %s", raw)
	}
	lines := stringList(objectPayload(t, env)["lines"])
	want := "got " + catalog.SecretPreview(progSecret) + " and " + catalog.SecretPreview(progSecret)
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("lines %#v", lines)
	}
	frame, _ = log.raw(0)["spec"].(map[string]any)
	fenv, _ := frame["env"].(map[string]any)
	if fenv["GH_TOKEN"] != progSecret || fenv["GH_HOST"] != "github.com" || fenv["OVER"] != "inline" {
		t.Fatalf("resolved env %#v", fenv)
	}
	if sk := stringList(frame["secret_keys"]); len(sk) != 1 || sk[0] != "GH_TOKEN" {
		t.Fatalf("secret_keys %#v (an inline override is not secret)", frame["secret_keys"])
	}
	for _, row := range auditRows(t, w, sc.project.ID) {
		if strings.Contains(string(row.Detail), progSecret) {
			t.Fatalf("audit leaked the value on %s", row.Action)
		}
	}

	// Logs are redacted with the same forms; without the key they are refused.
	status, env, raw = postImageLike(t, w, tok, project, "gh", "logs", host, "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if strings.Contains(raw, progSecret) {
		t.Fatalf("logs leaked the value: %s", raw)
	}
	w.srv.Secrets = nil
	before := log.count()
	status, env, _ = postImageLike(t, w, tok, project, "gh", "logs", host, "")
	mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", errSecretsKey.Error())
	status, env, _ = postImageLike(t, w, tok, project, "gh", "run", host, "new-2")
	mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", errSecretsKey.Error())
	if log.count() != before {
		t.Fatal("a refused request sent a task")
	}
	w.srv.Secrets = testBox(t)

	// Frame limit: a resolved spec over 64 KiB is refused before sending.
	big := strings.Repeat("v", 4000)
	var dotenv strings.Builder
	for i := 0; i < 20; i++ {
		dotenv.WriteString(envKey(i) + "=" + big + "\n")
	}
	putEnvFlag(t, w, tok, project, "big", dotenv.String(), false)
	status, env = w.do(tok, http.MethodPost, path, progBody("big", map[string]any{"env_sets": []string{"big"}}))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	before = log.count()
	status, env, _ = postImageLike(t, w, tok, project, "big", "run", host, "big-1")
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", "the resolved environment is too large for one task")
	if log.count() != before {
		t.Fatal("an oversized spec was sent")
	}
}

// postImageLike posts a program action and returns the raw body as well.
func postImageLike(t *testing.T, w *world, tok, project, name, action, host, key string) (int, envelope, string) {
	t.Helper()
	body := map[string]any{"host_id": host}
	if action != "logs" && action != "status" {
		body["idempotency_key"] = key
	}
	raw, _ := json.Marshal(body)
	return mustCall(t, w, tok, http.MethodPost, "/v1/projects/"+project+"/catalog/"+name+"/"+action, string(raw))
}

// api-v1 v1.22 items 2 and 6: a key added to a set re-hashes the program
// entry and voids its shared-host approval; a value change does not; the
// approval view shows references, never a value.
func TestProgramSecretsRehashAndApproval(t *testing.T) {
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
	putEnv(t, w, gtok, gpath, "lock", "KEY="+progSecret+"\n")
	status, env := w.do(gtok, http.MethodPost, "/v1/projects/"+gpath+"/catalog", progBody("gh", map[string]any{
		"secret_env": map[string]string{"GH_TOKEN": "lock.KEY"}, "env": map[string]string{"MODE": "ci"},
	}))
	mustHTTP(t, status, env, http.StatusCreated, "ok", "ok")
	created := objectPayload(t, env)
	hash, _ := created["spec_hash"].(string)
	entryID, _ := created["id"].(string)
	host := sc.host.ID.String()
	status, env = postAct(w, gtok, gpath, "gh", "run", host, "ap-1", nil)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "waiting for the host owner's approval")
	status, env, raw := mustCall(t, w, w.token(owner.ID), http.MethodGet, "/v1/hosts/"+host+"/catalog-approvals", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if strings.Contains(raw, progSecret) || !strings.Contains(raw, `"secret_env":{"GH_TOKEN":"lock.KEY"}`) ||
		!strings.Contains(raw, `"env":{"MODE":"ci"}`) || !strings.Contains(raw, `"env_sets":[]`) {
		t.Fatalf("approval view %s", raw)
	}
	approve := func(specHash string) {
		t.Helper()
		status, env, _ := mustCall(t, w, w.token(owner.ID), http.MethodPost, "/v1/hosts/"+host+"/catalog-approvals", `{"entry_id":"`+entryID+`","spec_hash":"`+specHash+`"}`)
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	}
	get := func() string {
		t.Helper()
		status, env := w.do(gtok, http.MethodGet, "/v1/projects/"+gpath+"/catalog/gh", "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		h, _ := objectPayload(t, env)["spec_hash"].(string)
		return h
	}
	approve(hash)
	putEnv(t, w, gtok, gpath, "lock", "KEY="+progSecret+"-other\n")
	if get() != hash || len(listApprovals(t, w, owner.ID, sc.host.ID)) != 1 {
		t.Fatal("a value-only change re-hashed or voided the approval")
	}
	putEnv(t, w, gtok, gpath, "lock", "KEY="+progSecret+"\nBAR=1\n")
	if get() == hash || len(listApprovals(t, w, owner.ID, sc.host.ID)) != 0 {
		t.Fatal("adding a key kept the hash or the approval")
	}
}

// api-v1 v1.23 item 2: the projects pre-check for program Run and image
// deploy, and the older-agent path.
func TestProjectsPreCheck(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	createProgram(t, w, tok, sc.project.ID, "tra", "pnpm run dev", false)
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)

	unlisted := secretsPrograms(false, []string{"other"})
	for k, v := range allowImages() {
		unlisted[k] = v
	}
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, unlisted, nil)
	log := watchTasks(t, conn, succeededTask)
	status, env := postAct(w, tok, project, "tra", "run", host, "pc-1", nil)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "this host's allowlist does not include project proj")
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "pc-2", nil)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "this host's allowlist does not include project proj")
	status, env, _ = postImage(t, w, tok, project, "web", "restart", host, "pc-3", nil)
	mustHTTP(t, status, env, http.StatusForbidden, "forbidden", "this host's allowlist does not include project proj")
	// Stop, status and logs are not pre-checked.
	status, env = postAct(w, tok, project, "tra", "stop", host, "pc-4", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if log.count() != 1 {
		t.Fatalf("tasks %d, want only the stop", log.count())
	}
	denied := 0
	for _, row := range auditRows(t, w, sc.project.ID) {
		if row.Decision == audit.DecisionDenied && strings.Contains(string(row.Detail), "allowlist does not include project proj") {
			denied++
		}
	}
	// program.run and catalog.deploy; image restart is not audited (as today).
	if denied != 2 {
		t.Fatalf("denied audit rows %d", denied)
	}
	_ = conn.CloseNow()

	listed := secretsPrograms(false, []string{"proj"})
	conn = helloAgent(t, w, sc.host, sc.credPlain, sc.credID, listed, nil)
	log = watchTasks(t, conn, succeededTask)
	status, env = postAct(w, tok, project, "tra", "run", host, "pc-5", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	_ = conn.CloseNow()

	conn = helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowPrograms(false), nil)
	log = watchTasks(t, conn, succeededTask)
	status, env = postAct(w, tok, project, "tra", "run", host, "pc-6", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if log.count() != 1 {
		t.Fatal("older agent: the run was not sent")
	}
}

// api-v1 v1.23 item 1: the list only for the owning project's members,
// project_listed for every viewer, null for an older agent.
func TestHostProjectsExposure(t *testing.T) {
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
	setPrograms(t, w, sc.host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}, Projects: []string{"proj", "secret-other"}})

	programsOf := func(tok, path string) (map[string]any, string) {
		t.Helper()
		status, env, raw := mustCall(t, w, tok, http.MethodGet, path, "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		var host map[string]any
		if items, ok := objectPayload(t, env)["items"].([]any); ok {
			for _, item := range items {
				m, _ := item.(map[string]any)
				if m["id"] == sc.host.ID.String() {
					host = m
				}
			}
		} else {
			host = objectPayload(t, env)
		}
		p, _ := host["programs"].(map[string]any)
		return p, raw
	}
	own, _ := programsOf(w.token(owner.ID), "/v1/projects/"+sc.project.ID.String()+"/hosts")
	if l := stringList(own["projects"]); len(l) != 2 || own["project_listed"] != true {
		t.Fatalf("owner view %#v", own)
	}
	shared, raw := programsOf(w.token(guestUser.ID), "/v1/projects/"+guest.ID.String()+"/hosts")
	if shared["projects"] != nil || shared["project_listed"] != false || strings.Contains(raw, "secret-other") {
		t.Fatalf("sharer view %#v", shared)
	}
	shared, raw = programsOf(w.token(guestUser.ID), "/v1/hosts/"+sc.host.ID.String())
	if shared["projects"] != nil || shared["project_listed"] != false || strings.Contains(raw, "secret-other") {
		t.Fatalf("sharer host view %#v", shared)
	}
	own, _ = programsOf(w.token(owner.ID), "/v1/hosts/"+sc.host.ID.String())
	if len(stringList(own["projects"])) != 2 || own["project_listed"] != true {
		t.Fatalf("owner host view %#v", own)
	}
	setPrograms(t, w, sc.host.ID, &store.HostPrograms{Enabled: true, RunAs: []string{"joe"}})
	own, _ = programsOf(w.token(owner.ID), "/v1/hosts/"+sc.host.ID.String())
	if v, ok := own["project_listed"]; !ok || v != nil {
		t.Fatalf("older agent project_listed %#v", own)
	}
	if v, ok := own["projects"]; !ok || v != nil {
		t.Fatalf("older agent projects %#v", own)
	}
}
