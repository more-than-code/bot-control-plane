package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/agentws"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/auth"
	"github.com/more-than-code/bot-control-plane/api/internal/config"
)

// restartWorld is a second API process over the same store: a new hub and
// Server with nothing in memory, wired like Run (the late hook set).
func restartWorld(t *testing.T, w *world) *world {
	t.Helper()
	return restartWorldClock(t, w, func() time.Time { return w.now })
}

// restartWorldClock is restartWorld whose hub reads hubNow; the Server keeps
// w.now.
func restartWorldClock(t *testing.T, w *world, hubNow func() time.Time) *world {
	t.Helper()
	log := w.srv.logger()
	clk := func() time.Time { return w.now }
	cfg := config.Config{
		GoogleClientID: "dummy-google-client-id", GoogleClientSecret: "dummy-google-client-secret",
		GoogleIssuer: "https://accounts.google.com", OAuthRedirectURIs: []string{"http://127.0.0.1/callback"},
		DeviceVerificationURI: "http://127.0.0.1/device", AgentWSURL: "ws://127.0.0.1/agent/v1/ws", MCPResource: "http://127.0.0.1:8091",
	}
	hub := agentws.New(w.mem, audit.Writer{Store: w.mem, Log: log, Now: clk}, log, hubNow)
	srv := &Server{
		Store: w.mem, Auth: auth.New(w.mem, cfg, clk), Hub: hub,
		Audit: audit.Writer{Store: w.mem, Log: log, Now: clk}, AgentWSURL: cfg.AgentWSURL, Log: log, Now: clk,
	}
	hub.SetLateHook(srv.LateResultHook)
	ws := httptest.NewServer(hub)
	httpSrv := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { hub.CloseAll() })
	t.Cleanup(httpSrv.Close)
	t.Cleanup(ws.Close)
	return &world{t: t, mem: w.mem, srv: srv, hub: hub, http: httpSrv, ws: ws, client: w.client, now: w.now}
}

// frameLog keeps every frame the agent socket receives.
type frameLog struct {
	mu     sync.Mutex
	frames []map[string]any
}

func readAll(c *websocket.Conn) *frameLog {
	l := &frameLog{}
	go func() {
		for {
			_, b, err := c.Read(context.Background())
			if err != nil {
				return
			}
			var msg map[string]any
			if json.Unmarshal(b, &msg) == nil {
				l.mu.Lock()
				l.frames = append(l.frames, msg)
				l.mu.Unlock()
			}
		}
	}()
	return l
}

// waitFor returns the first frame of type typ (and task id, when set).
func (l *frameLog) waitFor(t *testing.T, typ, taskID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		for _, f := range l.frames {
			if f["type"] == typ && (taskID == "" || f["task_id"] == taskID) {
				l.mu.Unlock()
				return f
			}
		}
		l.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s frame for %s", typ, taskID)
	return nil
}

func (l *frameLog) count(typ, taskID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, f := range l.frames {
		if f["type"] == typ && f["task_id"] == taskID {
			n++
		}
	}
	return n
}

func lateDeployResult(taskID, digest string) map[string]any {
	f := imageDeployReply(map[string]any{"task_id": taskID}, digest)
	f["late"] = true
	return f
}

func lateAudit(t *testing.T, w *world, projectID uuid.UUID, action string) map[string]any {
	t.Helper()
	for _, row := range auditRows(t, w, projectID) {
		if row.Action != action {
			continue
		}
		var detail map[string]any
		if json.Unmarshal(row.Detail, &detail) == nil && detail["late"] == true {
			return detail
		}
	}
	return nil
}

// api-v1 v1.50 item 3: a deploy whose API process ended while the row was
// in flight is settled by the result a new API process receives; the row
// is found through the task id stored before the frame was sent.
func TestLateResultAfterRestart(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	created := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	entryID := uuid.MustParse(created["id"].(string))
	ctx := context.Background()

	// The first process began the deploy, stored the task id and died.
	row, err := w.mem.BeginImageOp(ctx, entryID, sc.host.ID, created["spec_hash"].(string), owner.ID, "mcp", w.now, w.now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	taskID := uuid.New()
	if err := w.mem.SetImageOpTask(ctx, row.ID, taskID, "deploy"); err != nil {
		t.Fatal(err)
	}

	w2 := restartWorld(t, w)
	conn := helloAgent(t, w2, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	frames := readAll(conn)
	writeFrame(t, conn, lateDeployResult(taskID.String(), shaDigest(4)))
	frames.waitFor(t, "task_result_ack", taskID.String())
	frames.waitFor(t, "task_ack", taskID.String())

	got, err := w.mem.GetImageDeploy(ctx, entryID, sc.host.ID, row.ID)
	if err != nil || got.InFlight || got.Outcome != "succeeded" || got.Digest == nil || *got.Digest != shaDigest(4) ||
		got.FinishedAt == nil || got.FinalStatus == nil || got.FinalStatus.State != "running" || len(got.LogTail) != 1 {
		t.Fatalf("settled row %+v %v", got, err)
	}
	detail := lateAudit(t, w2, sc.project.ID, "catalog.deploy")
	if detail == nil || detail["outcome"] != "succeeded" || detail["entry"] != "web" || detail["via"] != "mcp" {
		t.Fatalf("late audit %#v", detail)
	}

	// The same result again (an agent that missed the ack): the row is now
	// settled otherwise, so it is acked and left alone, with no second
	// audit row.
	writeFrame(t, conn, lateDeployResult(taskID.String(), shaDigest(5)))
	deadline := time.Now().Add(5 * time.Second)
	for frames.count("task_result_ack", taskID.String()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if frames.count("task_result_ack", taskID.String()) != 2 {
		t.Fatal("repeat not acked")
	}
	again, _ := w.mem.GetImageDeploy(ctx, entryID, sc.host.ID, row.ID)
	if *again.Digest != shaDigest(4) {
		t.Fatalf("settled row changed %+v", again)
	}

	// An unknown task id is acked and dropped.
	doneTask := uuid.New()
	writeFrame(t, conn, lateDeployResult(doneTask.String(), shaDigest(6)))
	frames.waitFor(t, "task_result_ack", doneTask.String())
	rows, err := w.mem.ListImageDeploys(ctx, entryID, sc.host.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("history %+v %v", rows, err)
	}
	n := 0
	for _, r := range auditRows(t, w2, sc.project.ID) {
		if r.Action == "catalog.deploy" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("catalog.deploy audit rows %d", n)
	}
}

// A deploy the API recorded as timeout is settled by the result that
// arrives later on the same process, through the task id performImage
// stored; a refused late result keeps the row as refused.
func TestLateResultSettlesTimeout(t *testing.T) {
	w := newWorld(t)
	w.srv.imageBudget = 1
	w.srv.imageMargin = 1
	w.hub.SetLateHook(w.srv.LateResultHook)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	created := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	entryID := uuid.MustParse(created["id"].(string))
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	frames := readAll(conn)

	status, env, _ := postImage(t, w, tok, project, "web", "deploy", host, "late-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	timedOut := objectPayload(t, env)["deploy"].(map[string]any)
	if timedOut["outcome"] != "timeout" {
		t.Fatalf("deploy %#v", timedOut)
	}
	task := frames.waitFor(t, "task", "")
	taskID, _ := task["task_id"].(string)
	// The same outcome again (the agent's own timed_out, resent twice) is
	// acked and dropped: no change, no audit (api-v1 v1.50 item 9).
	auditsBefore := len(auditRows(t, w, sc.project.ID))
	for i := 0; i < 2; i++ {
		writeFrame(t, conn, map[string]any{"v": 1, "type": "task_result", "task_id": taskID, "state": "timed_out", "late": true})
	}
	deadline0 := time.Now().Add(5 * time.Second)
	for frames.count("task_result_ack", taskID) < 2 && time.Now().Before(deadline0) {
		time.Sleep(10 * time.Millisecond)
	}
	if frames.count("task_result_ack", taskID) != 2 || len(auditRows(t, w, sc.project.ID)) != auditsBefore {
		t.Fatalf("timeout replay acks %d audits %d→%d", frames.count("task_result_ack", taskID), auditsBefore, len(auditRows(t, w, sc.project.ID)))
	}
	writeFrame(t, conn, lateDeployResult(taskID, shaDigest(7)))
	deadline1 := time.Now().Add(5 * time.Second)
	for frames.count("task_result_ack", taskID) < 3 && time.Now().Before(deadline1) {
		time.Sleep(10 * time.Millisecond)
	}
	ctx := context.Background()
	rowID := uuid.MustParse(timedOut["id"].(string))
	got, err := w.mem.GetImageDeploy(ctx, entryID, sc.host.ID, rowID)
	if err != nil || got.Outcome != "succeeded" || *got.Digest != shaDigest(7) {
		t.Fatalf("timeout row %+v %v", got, err)
	}
	if got.FinishedAt == nil || got.FinishedAt.Format(time.RFC3339) != timedOut["finished_at"] {
		t.Fatalf("finished_at moved: %v vs %v", got.FinishedAt, timedOut["finished_at"])
	}
	if detail := lateAudit(t, w, sc.project.ID, "catalog.deploy"); detail == nil || detail["digest"] != shaDigest(7) {
		t.Fatalf("late audit %#v", detail)
	}

	// A rollback that timed out, then the host's late refusal.
	status, env, _ = postImage(t, w, tok, project, "web", "rollback", host, "late-2", map[string]any{"digest": shaDigest(7)})
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	rolled := objectPayload(t, env)["deploy"].(map[string]any)
	var rollTask string
	deadline := time.Now().Add(5 * time.Second)
	for rollTask == "" && time.Now().Before(deadline) {
		frames.mu.Lock()
		for _, f := range frames.frames {
			if f["type"] == "task" && f["kind"] == "image_rollback" {
				rollTask, _ = f["task_id"].(string)
			}
		}
		frames.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	writeFrame(t, conn, map[string]any{"v": 1, "type": "task_result", "task_id": rollTask, "state": "failed", "error_code": "policy_denied", "late": true})
	frames.waitFor(t, "task_result_ack", rollTask)
	got, err = w.mem.GetImageDeploy(ctx, entryID, sc.host.ID, uuid.MustParse(rolled["id"].(string)))
	if err != nil || got.Outcome != "refused" || got.Digest != nil || got.Reason == nil || !strings.Contains(*got.Reason, "policy") {
		t.Fatalf("refused row %+v %v", got, err)
	}
	// A late refusal is audited as a denial with its error code, like on time.
	denied := 0
	for _, r := range auditRows(t, w, sc.project.ID) {
		var detail map[string]any
		if r.Action == "catalog.rollback" && json.Unmarshal(r.Detail, &detail) == nil && detail["late"] == true {
			if r.Decision != "denied" || r.ErrorCode == nil || *r.ErrorCode != "forbidden" || detail["reason"] == nil {
				t.Fatalf("late refusal audit %+v %#v", r, detail)
			}
			denied++
		}
	}
	if denied != 1 {
		t.Fatalf("late refusal audit rows %d", denied)
	}
}

// On-time image results get task_result_ack too; results of other kinds
// (snapshot, probe) do not.
func TestTaskResultAckOnTime(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	var mu sync.Mutex
	acks := map[string]int{}
	ids := map[string]string{}
	go func() {
		for {
			_, b, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var msg map[string]any
			if json.Unmarshal(b, &msg) != nil {
				continue
			}
			id, _ := msg["task_id"].(string)
			mu.Lock()
			switch msg["type"] {
			case "task":
				ids[msg["kind"].(string)] = id
				mu.Unlock()
				reply := imageDeployReply(msg, shaDigest(1))
				if msg["kind"] == "host_snapshot" {
					reply = map[string]any{"v": 1, "type": "task_result", "task_id": id, "state": "succeeded", "payload": map[string]any{}}
				}
				raw, _ := json.Marshal(reply)
				_ = conn.Write(context.Background(), websocket.MessageText, raw)
				continue
			case "task_result_ack":
				acks[id]++
			}
			mu.Unlock()
		}
	}()
	status, env, _ := postImage(t, w, tok, sc.project.ID.String(), "web", "deploy", sc.host.ID.String(), "ontime-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env, _ = mustCall(t, w, tok, http.MethodPost, "/v1/hosts/"+sc.host.ID.String()+"/snapshot", "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := acks[ids["image_deploy"]]
		mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if acks[ids["image_deploy"]] != 1 || acks[ids["host_snapshot"]] != 0 {
		t.Fatalf("acks %#v ids %#v", acks, ids)
	}
}

// A store failure while settling is not acked with task_result_ack, so the
// agent keeps its file and resends after its next hello; task_ack still goes.
func TestLateResultStoreFailureNotAcked(t *testing.T) {
	w := newWorld(t)
	w.hub.SetLateHook(func(context.Context, uuid.UUID, uuid.UUID, agentws.LiveResult, error) error {
		return context.DeadlineExceeded
	})
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	frames := readAll(conn)
	id := uuid.NewString()
	writeFrame(t, conn, lateDeployResult(id, shaDigest(1)))
	frames.waitFor(t, "task_ack", id)
	writeFrame(t, conn, map[string]any{"v": 1, "type": "ping"})
	frames.waitFor(t, "pong", "")
	if frames.count("task_result_ack", id) != 0 {
		t.Fatal("acked a result that was not stored")
	}
}

// dropWorld is a world with the late hook set and an image entry "web".
func dropWorld(t *testing.T) (*world, scene, string, uuid.UUID) {
	t.Helper()
	w := newWorld(t)
	w.hub.SetLateHook(w.srv.LateResultHook)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	created := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	return w, sc, tok, uuid.MustParse(created["id"].(string))
}

// dropAfterTask connects an agent that closes its socket as soon as it reads
// a task frame, and returns the task id it saw.
func dropAfterTask(t *testing.T, w *world, sc scene) chan string {
	t.Helper()
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	ids := make(chan string, 1)
	go func() {
		for {
			_, b, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var msg map[string]any
			if json.Unmarshal(b, &msg) == nil && msg["type"] == "task" {
				ids <- msg["task_id"].(string)
				conn.Close(websocket.StatusGoingAway, "")
				return
			}
		}
	}()
	return ids
}

// api-v1 v1.50: a deploy whose frame was written stays in flight when the
// socket drops; the caller still gets unavailable, and the result the agent
// resends after reconnecting settles the row with the real outcome.
func TestDropAfterWriteKeepsRowForLateResult(t *testing.T) {
	w, sc, tok, entryID := dropWorld(t)
	ids := dropAfterTask(t, w, sc)
	status, env, _ := postImage(t, w, tok, sc.project.ID.String(), "web", "deploy", sc.host.ID.String(), "drop-1", nil)
	mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", msgMayStillFinish)
	taskID := <-ids
	ctx := context.Background()
	row, err := w.mem.RunningImageOp(ctx, entryID, sc.host.ID)
	if err != nil || !row.InFlight {
		t.Fatalf("row after drop %+v %v", row, err)
	}
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	frames := readAll(conn)
	// The kept row is a running operation: the next deploy is a 409 with ends_by.
	status, env, _ = postImage(t, w, tok, sc.project.ID.String(), "web", "deploy", sc.host.ID.String(), "drop-1b", nil)
	if status != http.StatusConflict || env.Code != "conflict" || objectPayload(t, env)["ends_by"] != row.InFlightUntil.UTC().Format(time.RFC3339) {
		t.Fatalf("second deploy %d %#v", status, env)
	}
	writeFrame(t, conn, lateDeployResult(taskID, shaDigest(3)))
	frames.waitFor(t, "task_result_ack", taskID)
	got, err := w.mem.GetImageDeploy(ctx, entryID, sc.host.ID, row.ID)
	if err != nil || got.Outcome != "succeeded" || *got.Digest != shaDigest(3) {
		t.Fatalf("settled %+v %v", got, err)
	}
}

// A dropped deploy with no result is settled timeout by the stateless sweep
// once in_flight_until passes, audited "swept"; a late result after that
// still settles it.
func TestDropAfterWriteSweptToTimeout(t *testing.T) {
	w, sc, tok, entryID := dropWorld(t)
	ids := dropAfterTask(t, w, sc)
	status, env, _ := postImage(t, w, tok, sc.project.ID.String(), "web", "deploy", sc.host.ID.String(), "drop-2", nil)
	mustHTTP(t, status, env, http.StatusServiceUnavailable, "unavailable", msgMayStillFinish)
	taskID := <-ids
	ctx := context.Background()
	row, err := w.mem.RunningImageOp(ctx, entryID, sc.host.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Before the deadline the sweep leaves it.
	w.srv.SweepImageOps(ctx)
	if again, _ := w.mem.GetImageDeploy(ctx, entryID, sc.host.ID, row.ID); !again.InFlight {
		t.Fatal("swept before in_flight_until")
	}
	later := row.InFlightUntil.Add(time.Second)
	w.srv.Now = func() time.Time { return later }
	w.srv.SweepImageOps(ctx)
	got, err := w.mem.GetImageDeploy(ctx, entryID, sc.host.ID, row.ID)
	if err != nil || got.InFlight || got.Outcome != "timeout" || got.FinishedAt == nil || !got.FinishedAt.Equal(later) {
		t.Fatalf("swept %+v %v", got, err)
	}
	var swept map[string]any
	for _, r := range auditRows(t, w, sc.project.ID) {
		var detail map[string]any
		if r.Action == "catalog.deploy" && json.Unmarshal(r.Detail, &detail) == nil && detail["swept"] == true {
			swept = detail
		}
	}
	if swept == nil || swept["outcome"] != "timeout" {
		t.Fatalf("sweep audit %#v", swept)
	}
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	frames := readAll(conn)
	writeFrame(t, conn, lateDeployResult(taskID, shaDigest(4)))
	frames.waitFor(t, "task_result_ack", taskID)
	if got, _ := w.mem.GetImageDeploy(ctx, entryID, sc.host.ID, row.ID); got.Outcome != "succeeded" {
		t.Fatalf("late after sweep %+v", got)
	}
}

// A frame never written (the hub saw the host offline at send time) does not
// stay in flight: v1.47 keeps it as refused (the host never ran it).
func TestDropBeforeWriteDeletesRow(t *testing.T) {
	w, sc, tok, entryID := dropWorld(t)
	var mu sync.Mutex
	hubNow := w.now
	w2 := restartWorldClock(t, w, func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return hubNow
	})
	helloAgent(t, w2, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	// The API still sees the host online at its own clock; the hub's clock
	// has moved past the ping window, so live() refuses before any write.
	mu.Lock()
	hubNow = w.now.Add(2 * time.Minute)
	mu.Unlock()
	w2.srv.Now = func() time.Time { return hubNow.Add(-2 * time.Minute) }
	status, env, _ := postImage(t, w2, tok, sc.project.ID.String(), "web", "deploy", sc.host.ID.String(), "drop-3", nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status %d %#v", status, env)
	}
	ctx := context.Background()
	if _, err := w.mem.RunningImageOp(ctx, entryID, sc.host.ID); err == nil {
		t.Fatal("row kept for a frame never written")
	}
	if rows, _ := w.mem.ListImageDeploys(ctx, entryID, sc.host.ID); len(rows) != 1 || rows[0].Outcome != "refused" {
		t.Fatalf("history %+v", rows)
	}
}
