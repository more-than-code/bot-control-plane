package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.47: a finished deploy keeps a reason, its final container state
// and the log tail; the single-deploy route returns the tail; the list and
// the deploy answer carry reason and final_status.
func TestDeployReasonStored(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	viewer := w.user("v@ex.com", "V")
	sc := w.scene(owner, w.user("a@ex.com", "A"), viewer, w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	created := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	entryID := uuid.MustParse(created["id"].(string))
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	var mu sync.Mutex
	reply := func(msg map[string]any) map[string]any { return imageDeployReply(msg, shaDigest(1)) }
	watchTasks(t, conn, func(msg map[string]any) map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return reply(msg)
	})
	set := func(f func(msg map[string]any) map[string]any) {
		mu.Lock()
		reply = f
		mu.Unlock()
	}
	failed := func(outcome string, state string, exit any, restarts int, reason any) func(map[string]any) map[string]any {
		return func(msg map[string]any) map[string]any {
			payload := map[string]any{
				"outcome": outcome, "digest": shaDigest(2), "exit_code": nil,
				"status": map[string]any{
					"container": "web", "state": state, "restart_count": restarts,
					"exit_code": exit, "ports": []int{},
				},
				"lines": []string{"starting", "panic: no DATABASE_URL"},
			}
			if reason != nil {
				payload["reason"] = reason
			}
			return map[string]any{
				"v": 1, "type": "task_result", "task_id": msg["task_id"], "state": "failed",
				"error_code": outcome, "payload": payload,
			}
		}
	}

	// A success has no reason and keeps its final state.
	status, env, _ := postImage(t, w, tok, project, "web", "deploy", host, "r-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	got := objectPayload(t, env)["deploy"].(map[string]any)
	if got["reason"] != nil || got["final_status"].(map[string]any)["state"] != "running" {
		t.Fatalf("success %#v", got)
	}

	// An older agent: the API works out the reason from the state.
	set(failed("failed", "exited", 1, 0, nil))
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "r-2", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	got = objectPayload(t, env)["deploy"].(map[string]any)
	if got["reason"] != "the container exited with code 1" {
		t.Fatalf("derived reason %#v", got)
	}
	failedID := got["id"].(string)

	// A newer agent's own reason wins and rolled_back adds what happened next.
	set(failed("rolled_back", "running", nil, 0, "health check GET :80/health answered 503"))
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "r-3", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	got = objectPayload(t, env)["deploy"].(map[string]any)
	if got["reason"] != "health check GET :80/health answered 503; rolled back; the previous version is running again" {
		t.Fatalf("agent reason %#v", got)
	}

	// A reason with a newline is not one line: dropped, the API's own is used.
	set(failed("failed", "restarting", nil, 3, "a\nb"))
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "r-4", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if got = objectPayload(t, env)["deploy"].(map[string]any); got["reason"] != "the container kept restarting (3 restarts)" {
		t.Fatalf("bad agent reason %#v", got)
	}

	// The list carries reasons (the test clock is frozen, so order is not checked).
	status, env = w.do(tok, http.MethodGet, "/v1/projects/"+project+"/catalog/web/deploys?host_id="+host, "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	items := objectPayload(t, env)["items"].([]any)
	reasons := map[any]bool{}
	for _, item := range items {
		row := item.(map[string]any)
		reasons[row["reason"]] = true
		if _, ok := row["lines"]; ok {
			t.Fatalf("list carries lines %#v", row)
		}
	}
	if len(items) != 4 || !reasons[nil] || !reasons["the container kept restarting (3 restarts)"] || !reasons["the container exited with code 1"] {
		t.Fatalf("list %#v", items)
	}

	// One deploy with its stored tail, for the owner and for a viewer (logs).
	path := "/v1/projects/" + project + "/catalog/web/deploys/" + failedID + "?host_id=" + host
	for _, who := range []string{tok, w.token(viewer.ID)} {
		status, env = w.do(who, http.MethodGet, path, "")
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		one := objectPayload(t, env)
		lines, _ := one["lines"].([]any)
		if len(lines) != 2 || lines[1] != "panic: no DATABASE_URL" || one["deploy"].(map[string]any)["final_status"].(map[string]any)["exit_code"] != float64(1) {
			t.Fatalf("one deploy %#v", one)
		}
	}
	status, env = w.do(tok, http.MethodGet, "/v1/projects/"+project+"/catalog/web/deploys/"+uuid.NewString()+"?host_id="+host, "")
	mustHTTP(t, status, env, http.StatusNotFound, "not_found", "not found")
	status, env = w.do(tok, http.MethodGet, "/v1/projects/"+project+"/catalog/web/deploys/nope?host_id="+host, "")
	if status != http.StatusBadRequest {
		t.Fatalf("bad id %d %#v", status, env)
	}

	row, err := w.mem.GetImageDeploy(context.Background(), entryID, sc.host.ID, uuid.MustParse(failedID))
	if err != nil || len(row.LogTail) != 2 {
		t.Fatalf("stored %#v %v", row, err)
	}
}

// api-v1 v1.47 item 2: a deploy the host refused stays in history as
// refused, with the host's message; it does not count as last deployed.
func TestDeployRefusedKept(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	created := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	entryID := uuid.MustParse(created["id"].(string))
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	watchTasks(t, conn, func(msg map[string]any) map[string]any {
		return map[string]any{
			"v": 1, "type": "task_reject", "task_id": msg["task_id"],
			"error_code": "forbidden", "message": "policy_denied",
		}
	})
	status, env, _ := postImage(t, w, tok, project, "web", "deploy", host, "x-1", nil)
	if status != http.StatusForbidden {
		t.Fatalf("refusal %d %#v", status, env)
	}
	rows, err := w.mem.ListImageDeploys(context.Background(), entryID, sc.host.ID)
	if err != nil || len(rows) != 1 || rows[0].Outcome != "refused" || rows[0].Digest != nil || rows[0].Reason == nil ||
		!strings.HasPrefix(*rows[0].Reason, "forbidden") {
		t.Fatalf("history %#v %v", rows, err)
	}
	last, err := w.mem.CatalogLastDeployed(context.Background(), sc.project.ID, []store.CatalogEntry{{ID: entryID, Kind: "image"}})
	if err != nil || len(last) != 0 {
		t.Fatalf("last deployed %#v %v", last, err)
	}
}

func TestDeployReasonUnit(t *testing.T) {
	seven := 7
	cases := []struct {
		outcome, agent string
		exit           *int
		final          *store.DeployFinalStatus
		want           string
	}{
		{"succeeded", "", nil, nil, ""},
		{"failed", "", &seven, nil, "the job exited with code 7"},
		{"failed", "", nil, &store.DeployFinalStatus{State: "running"}, "the container did not pass its health checks in time"},
		{"failed", "", nil, &store.DeployFinalStatus{State: "missing"}, "the container did not start"},
		{"failed", "", nil, nil, "the deploy failed"},
		{"image_unavailable", "pull: manifest unknown", nil, nil, "pull: manifest unknown"},
		{"timeout", "", nil, nil, "the host did not report a result in time"},
		{"rollback_failed", "", nil, nil, "the new container did not become healthy; the rollback failed too; the service may be down"},
	}
	for _, c := range cases {
		got := deployReason(c.outcome, c.agent, c.exit, c.final)
		if (got == nil) != (c.want == "") || (got != nil && *got != c.want) {
			t.Fatalf("%s: %v want %q", c.outcome, got, c.want)
		}
	}
	long := strings.Repeat("é", 600)
	if got := clipReason(long); len(*got) > maxStoredReason || !strings.HasSuffix(*got, "…") {
		t.Fatalf("clip %d", len(*got))
	}
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = strings.Repeat("x", 1000)
	}
	if got := storedTail(lines); len(got) != 16 || len(storedTail(nil)) != 0 {
		t.Fatalf("tail %d", len(got))
	}
}

// A host that drops after the task frame was sent did not refuse it: the
// deploy may have run, so the row is not kept as refused (deleted as before).
func TestDeployDroppedNotRefused(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	created := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	entryID := uuid.MustParse(created["id"].(string))
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	watchTasks(t, conn, func(msg map[string]any) map[string]any {
		_ = conn.CloseNow()
		return map[string]any{}
	})
	status, env, _ := postImage(t, w, tok, project, "web", "deploy", host, "d-1", nil)
	if status == http.StatusOK {
		t.Fatalf("dropped host answered ok %#v", env)
	}
	rows, err := w.mem.ListImageDeploys(context.Background(), entryID, sc.host.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("history %#v %v", rows, err)
	}
}
