package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// An entry-private secret ("@<entry>.KEY") reaches the agent as
// "<entry>.KEY", the shape every released agent accepts; shared references
// are sent as stored. An agent bad_spec refusal says what it means.
func TestDeployPrivateSecretRefsAndBadSpec(t *testing.T) {
	w := newWorld(t)
	w.srv.Secrets = testBox(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	putEnv(t, w, tok, project, "db", "POSTGRES_PASSWORD="+refPassword+"\n")
	created := createImage(t, w, tok, sc.project.ID, "app-dev",
		`{"kind":"service","image":"nginx","secret_env":{"SEED_PASSWORD":{"generate":{"kind":"password","length":24}},"DB":"db.POSTGRES_PASSWORD"}}`)
	entryID := uuid.MustParse(created["id"].(string))
	got := createImageGet(t, w, tok, sc.project.ID, "app-dev")
	stored := got["spec"].(map[string]any)["secret_env"].(map[string]any)
	if stored["SEED_PASSWORD"] != "@app-dev.SEED_PASSWORD" {
		t.Fatalf("stored %v", stored)
	}

	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	tasks := watchTasks(t, conn, func(msg map[string]any) map[string]any {
		return map[string]any{"v": 1, "type": "task_reject", "task_id": msg["task_id"], "error_code": "bad_spec"}
	})
	status, env, _ := postImage(t, w, tok, project, "app-dev", "deploy", host, "d-1", nil)
	mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", msgSpecRefused)

	frame := tasks.raw(0)
	spec := frame["spec"].(map[string]any)["spec"].(map[string]any)
	refs := spec["secret_env"].(map[string]any)
	if refs["SEED_PASSWORD"] != "app-dev.SEED_PASSWORD" || refs["DB"] != "db.POSTGRES_PASSWORD" {
		t.Fatalf("frame secret_env %v", refs)
	}
	envMap := frame["spec"].(map[string]any)["env"].(map[string]any)
	if v, _ := envMap["SEED_PASSWORD"].(string); len(v) != 24 {
		t.Fatalf("resolved private secret missing (len %d)", len(v))
	}

	rows, err := w.mem.ListImageDeploys(context.Background(), entryID, sc.host.ID)
	if err != nil || len(rows) != 1 || rows[0].Outcome != "refused" || rows[0].Reason == nil ||
		!strings.Contains(*rows[0].Reason, "bad_spec") {
		t.Fatalf("history %#v %v", rows, err)
	}
}

func TestAgentSecretRefs(t *testing.T) {
	spec := map[string]any{"secret_env": map[string]any{"A": "@web.A", "B": "shared.B", "C": "@.C", "D": 3}}
	agentSecretRefs(spec)
	refs := spec["secret_env"].(map[string]any)
	if refs["A"] != "web.A" || refs["B"] != "shared.B" || refs["C"] != "@.C" || refs["D"] != 3 {
		t.Fatalf("refs %v", refs)
	}
	agentSecretRefs(map[string]any{}) // no secret_env: nothing to do
}
