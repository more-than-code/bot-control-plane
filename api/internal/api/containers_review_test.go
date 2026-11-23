package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/memstore"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Review changes of api-v1 v1.50 (item 9).

// failFinish is the memstore whose FinishImageDeployWith fails, for the
// store-error path of a live result.
type failFinish struct {
	*memstore.Mem
}

func (failFinish) FinishImageDeployWith(context.Context, uuid.UUID, store.DeployFinish) (store.ImageDeploy, error) {
	return store.ImageDeploy{}, errors.New("database: down")
}

// heldAgent answers each task with reply after release is closed (nil:
// at once), and records every frame it receives.
type heldAgent struct {
	conn   *websocket.Conn
	frames *frameLog
	tasks  chan map[string]any
}

func holdAgent(t *testing.T, w *world, sc scene, release chan struct{}) *heldAgent {
	t.Helper()
	a := &heldAgent{conn: helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil), frames: &frameLog{}, tasks: make(chan map[string]any, 4)}
	go func() {
		for {
			_, b, err := a.conn.Read(context.Background())
			if err != nil {
				return
			}
			var msg map[string]any
			if json.Unmarshal(b, &msg) != nil {
				continue
			}
			a.frames.mu.Lock()
			a.frames.frames = append(a.frames.frames, msg)
			a.frames.mu.Unlock()
			if msg["type"] != "task" {
				continue
			}
			a.tasks <- msg
			go func() {
				if release != nil {
					<-release
				}
				raw, _ := json.Marshal(imageDeployReply(msg, shaDigest(8)))
				_ = a.conn.Write(context.Background(), websocket.MessageText, raw)
			}()
		}
	}()
	return a
}

func catalogDeployAudits(t *testing.T, w *world, project uuid.UUID) int {
	t.Helper()
	n := 0
	for _, r := range auditRows(t, w, project) {
		if r.Action == "catalog.deploy" && r.Decision == "allowed" {
			n++
		}
	}
	return n
}

// Item 9: the live path acks only after the row is written; a store error
// keeps the row in flight and sends no ack.
func TestLiveAckAfterWrite(t *testing.T) {
	w, sc, tok, entryID := dropWorld(t)
	w.srv.Store = failFinish{w.mem}
	agent := holdAgent(t, w, sc, nil)
	status, env, _ := postImage(t, w, tok, sc.project.ID.String(), "web", "deploy", sc.host.ID.String(), "ack-1", nil)
	if status != http.StatusInternalServerError {
		t.Fatalf("store error answer %d %#v", status, env)
	}
	task := <-agent.tasks
	id, _ := task["task_id"].(string)
	agent.frames.waitFor(t, "task_ack", id)
	time.Sleep(100 * time.Millisecond)
	if agent.frames.count("task_result_ack", id) != 0 {
		t.Fatal("result acked although its row was not written")
	}
	if row, err := w.mem.RunningImageOp(context.Background(), entryID, sc.host.ID); err != nil || !row.InFlight {
		t.Fatalf("row not kept in flight %+v %v", row, err)
	}
	// With the store back, the agent's resend settles it through the late path.
	w.srv.Store = w.mem
	writeFrame(t, agent.conn, lateDeployResult(id, shaDigest(8)))
	agent.frames.waitFor(t, "task_result_ack", id)
	if row, err := w.mem.RunningImageOp(context.Background(), entryID, sc.host.ID); err == nil {
		t.Fatalf("resent result did not settle %+v", row)
	}
}

// Item 9: a result for a waiting task on another host's connection is
// acked and dropped; the waiter still gets its own host's result.
func TestResultFromOtherConnectionDropped(t *testing.T) {
	w, sc, tok, _ := dropWorld(t)
	release := make(chan struct{})
	agent := holdAgent(t, w, sc, release)
	otherHost, cred, credID := enrollAgentHost(t, w, sc.project, sc.project.CreatedBy, "pi9")
	other := helloAgent(t, w, otherHost, cred, credID, allowImages(), nil)
	otherFrames := readAll(other)
	done := make(chan callResult, 1)
	go func() {
		status, env, raw, err := callRaw(w, tok, http.MethodPost, imagePath(sc.project.ID.String(), "web", "deploy"), imageBody(sc.host.ID.String(), "foreign-1", nil))
		done <- callResult{status: status, env: env, raw: raw, err: err}
	}()
	task := <-agent.tasks
	id, _ := task["task_id"].(string)
	writeFrame(t, other, lateDeployResult(id, shaDigest(9)))
	otherFrames.waitFor(t, "task_result_ack", id)
	close(release)
	got := <-done
	if got.err != nil || got.status != http.StatusOK {
		t.Fatalf("deploy %d %v", got.status, got.err)
	}
	var body struct {
		Payload struct {
			Deploy store.ImageDeploy `json:"deploy"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(got.raw), &body); err != nil || body.Payload.Deploy.Digest == nil || *body.Payload.Deploy.Digest != shaDigest(8) {
		t.Fatalf("deploy took the foreign result %s", got.raw)
	}
	agent.frames.waitFor(t, "task_result_ack", id)
}

// Items 3 and 9: the live waiter and a late result (another API process)
// on one row, in both orders: one settle, one audit row, both answered.
func TestLiveAndLateRace(t *testing.T) {
	t.Run("live first", func(t *testing.T) {
		w, sc, tok, entryID := dropWorld(t)
		agent := holdAgent(t, w, sc, nil)
		status, env, _ := postImage(t, w, tok, sc.project.ID.String(), "web", "deploy", sc.host.ID.String(), "race-1", nil)
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
		id, _ := (<-agent.tasks)["task_id"].(string)
		w2 := restartWorld(t, w)
		conn := helloAgent(t, w2, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
		frames := readAll(conn)
		writeFrame(t, conn, lateDeployResult(id, shaDigest(5)))
		frames.waitFor(t, "task_result_ack", id)
		rows, _ := w.mem.ListImageDeploys(context.Background(), entryID, sc.host.ID)
		if len(rows) != 1 || *rows[0].Digest != shaDigest(8) || catalogDeployAudits(t, w, sc.project.ID) != 1 {
			t.Fatalf("rows %+v audits %d", rows, catalogDeployAudits(t, w, sc.project.ID))
		}
	})
	t.Run("late first", func(t *testing.T) {
		w, sc, tok, entryID := dropWorld(t)
		release := make(chan struct{})
		agent := holdAgent(t, w, sc, release)
		done := make(chan callResult, 1)
		go func() {
			status, env, raw, err := callRaw(w, tok, http.MethodPost, imagePath(sc.project.ID.String(), "web", "deploy"), imageBody(sc.host.ID.String(), "race-2", nil))
			done <- callResult{status: status, env: env, raw: raw, err: err}
		}()
		id, _ := (<-agent.tasks)["task_id"].(string)
		w2 := restartWorld(t, w)
		conn := helloAgent(t, w2, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
		frames := readAll(conn)
		writeFrame(t, conn, lateDeployResult(id, shaDigest(5)))
		frames.waitFor(t, "task_result_ack", id)
		close(release)
		got := <-done
		if got.err != nil || got.status != http.StatusOK {
			t.Fatalf("live answer %d %v %s", got.status, got.err, got.raw)
		}
		// Item 7: the live waiter answers from the row the late result wrote.
		if !strings.Contains(got.raw, shaDigest(5)) {
			t.Fatalf("live answer is not the stored row: %s", got.raw)
		}
		agent.frames.waitFor(t, "task_result_ack", id)
		rows, _ := w.mem.ListImageDeploys(context.Background(), entryID, sc.host.ID)
		if len(rows) != 1 || *rows[0].Digest != shaDigest(5) || catalogDeployAudits(t, w, sc.project.ID) != 1 {
			t.Fatalf("rows %+v audits %d", rows, catalogDeployAudits(t, w, sc.project.ID))
		}
	})
}

// Item 9: a late result whose entry's secrets cannot be resolved (here the
// receiving process has no secrets key) is stored with the tail withheld.
func TestLateTailWithheld(t *testing.T) {
	w, sc, tok, _ := refsWorld(t, allowImages())
	created := createImage(t, w, tok, sc.project.ID, "sec", `{"kind":"service","image":"nginx","env_sets":["db"]}`)
	entryID := uuid.MustParse(created["id"].(string))
	ctx := context.Background()
	row, err := w.mem.BeginImageOp(ctx, entryID, sc.host.ID, created["spec_hash"].(string), sc.owner, "console", w.now, w.now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	taskID := uuid.New()
	if err := w.mem.SetImageOpTask(ctx, row.ID, taskID, "deploy"); err != nil {
		t.Fatal(err)
	}
	w2 := restartWorld(t, w) // no secrets key
	conn := helloAgent(t, w2, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	frames := readAll(conn)
	result := lateDeployResult(taskID.String(), shaDigest(6))
	result["payload"].(map[string]any)["lines"] = []string{"password " + refPassword}
	writeFrame(t, conn, result)
	frames.waitFor(t, "task_result_ack", taskID.String())
	got, err := w.mem.GetImageDeploy(ctx, entryID, sc.host.ID, row.ID)
	if err != nil || got.Outcome != "succeeded" || len(got.LogTail) != 1 || got.LogTail[0] != lateTailWithheld {
		t.Fatalf("stored %+v %v", got, err)
	}
	// With the key, the same entry's tail is kept and redacted.
	w3 := restartWorld(t, w)
	w3.srv.Secrets = w.srv.Secrets
	row2, err := w.mem.BeginImageOp(ctx, entryID, sc.host.ID, created["spec_hash"].(string), sc.owner, "console", w.now, w.now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	task2 := uuid.New()
	if err := w.mem.SetImageOpTask(ctx, row2.ID, task2, "deploy"); err != nil {
		t.Fatal(err)
	}
	conn3 := helloAgent(t, w3, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	frames3 := readAll(conn3)
	result = lateDeployResult(task2.String(), shaDigest(6))
	result["payload"].(map[string]any)["lines"] = []string{"password " + refPassword}
	writeFrame(t, conn3, result)
	frames3.waitFor(t, "task_result_ack", task2.String())
	got, err = w.mem.GetImageDeploy(ctx, entryID, sc.host.ID, row2.ID)
	if err != nil || len(got.LogTail) != 1 || got.LogTail[0] == lateTailWithheld || strings.Contains(got.LogTail[0], refPassword) {
		t.Fatalf("redacted tail %+v %v", got.LogTail, err)
	}
}

// Item 9: readiness, delegated plans and the run plan name host networking.
func TestHostNetworkReadinessAndPlans(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx","network":"host"}`)
	setPrograms(t, w, sc.host.ID, hostNetPrograms(false))
	_, checks, _ := readinessOf(t, w, tok, project, "web", host)
	found := false
	for _, c := range checks {
		if c.Check == "networks" {
			found = c.Status == "missing" && c.Message == reasonNoHostNetwork
		}
	}
	if !found {
		t.Fatalf("readiness without host networking: %s", checkNames(checks))
	}
	setPrograms(t, w, sc.host.ID, hostNetPrograms(true))
	_, checks, _ = readinessOf(t, w, tok, project, "web", host)
	for _, c := range checks {
		if c.Check == "networks" && c.Status != "ok" {
			t.Fatalf("readiness with host networking: %s", checkNames(checks))
		}
	}
	helloAgent(t, w, sc.host, sc.credPlain, sc.credID, map[string]any{"enabled": true, "run_as": []string{"joe"}, "allow_shell": false, "policy_hash": "abc", "images": true, "docker": true, "host_network": true}, nil)
	delegated := tokenKind(t, w, owner.ID, store.KindDelegated)
	for _, action := range []string{"deploy", "run"} {
		status, env, _ := postImage(t, w, delegated, project, "web", action, host, "plan-"+action, nil)
		mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
		if plan, _ := objectPayload(t, env)["plan"].(string); !strings.HasSuffix(plan, "; runs on the host network (shares the host's loopback)") {
			t.Fatalf("%s plan %q", action, plan)
		}
	}
	if hostNetworkPlan("stop") != "" || hostNetworkPlan("rollback") == "" {
		t.Fatal("hostNetworkPlan actions")
	}
}
