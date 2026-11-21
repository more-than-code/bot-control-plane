package api

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.42 item 3: the API waits the task budget plus a margin. A result
// inside the margin keeps its real outcome; none at all gives timeout. The
// frame's timeout_sec stays the budget.
func TestImageResultMargin(t *testing.T) {
	w := newWorld(t)
	w.srv.imageBudget = 1
	w.srv.imageMargin = 2
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	created := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	entryID := uuid.MustParse(created["id"].(string))
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	var mu sync.Mutex
	answer := true
	log := watchTasks(t, conn, func(msg map[string]any) map[string]any {
		mu.Lock()
		reply := answer
		mu.Unlock()
		if !reply {
			return map[string]any{"v": 1, "type": "noop"}
		}
		// Past the 1 s budget, inside the 2 s margin.
		time.Sleep(1500 * time.Millisecond)
		return imageDeployReply(msg, shaDigest(9))
	})

	status, env, _ := postImage(t, w, tok, project, "web", "deploy", host, "late-1", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if got := objectPayload(t, env)["deploy"].(map[string]any); got["outcome"] != "succeeded" || got["digest"] != shaDigest(9) {
		t.Fatalf("late deploy %#v", got)
	}
	if log.count() != 1 || log.raw(0)["timeout_sec"] != float64(1) {
		t.Fatalf("frame timeout_sec %#v", log.raw(0)["timeout_sec"])
	}

	status, env, _ = postImage(t, w, tok, project, "web", "rollback", host, "late-2", map[string]any{"digest": shaDigest(9)})
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if got := objectPayload(t, env)["deploy"].(map[string]any); got["outcome"] != "succeeded" {
		t.Fatalf("late rollback %#v", got)
	}
	if log.count() != 2 || log.raw(1)["timeout_sec"] != float64(1) {
		t.Fatalf("rollback frame timeout_sec %#v", log.raw(1)["timeout_sec"])
	}

	mu.Lock()
	answer = false
	mu.Unlock()
	start := time.Now()
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "late-3", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if got := objectPayload(t, env)["deploy"].(map[string]any); got["outcome"] != "timeout" || got["digest"] != nil {
		t.Fatalf("silent deploy %#v", got)
	}
	if waited := time.Since(start); waited < 3*time.Second {
		t.Fatalf("gave up after %s, before budget + margin", waited)
	}
	rows, err := w.mem.ListImageDeploys(context.Background(), entryID, sc.host.ID)
	timeouts := 0
	for _, row := range rows {
		if row.Outcome == "timeout" {
			timeouts++
		}
	}
	if err != nil || len(rows) != 3 || timeouts != 1 {
		t.Fatalf("history %#v %v", rows, err)
	}
}

// api-v1 v1.42 item 1: only a running row whose deadline is ahead is listed,
// first, with ends_by; a stale one is not.
func TestImageHistoryRunningRow(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	created := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	entryID := uuid.MustParse(created["id"].(string))
	hash, _ := created["spec_hash"].(string)
	ctx := context.Background()
	digest := shaDigest(3)
	done, err := w.mem.BeginImageOp(ctx, entryID, sc.host.ID, hash, owner.ID, "console", w.now.Add(-time.Hour), w.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.mem.FinishImageDeploy(ctx, done.ID, "succeeded", &digest, nil, w.now.Add(-50*time.Minute)); err != nil {
		t.Fatal(err)
	}
	hist := "/v1/projects/" + project + "/catalog/web/deploys?host_id=" + host

	stale, err := w.mem.BeginImageOp(ctx, entryID, sc.host.ID, hash, owner.ID, "mcp", w.now.Add(-20*time.Minute), w.now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	status, env, _ := mustCall(t, w, tok, http.MethodGet, hist, "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if items := listItems(t, env); len(items) != 1 || items[0]["outcome"] != "succeeded" || items[0]["ends_by"] != nil {
		t.Fatalf("stale row listed %#v", items)
	}
	if err := w.mem.ReleaseImageOp(ctx, stale.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := w.mem.BeginImageOp(ctx, entryID, sc.host.ID, hash, owner.ID, "mcp", w.now.Add(-time.Minute), w.now.Add(8*time.Minute)); err != nil {
		t.Fatal(err)
	}
	status, env, _ = mustCall(t, w, tok, http.MethodGet, hist, "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	items := listItems(t, env)
	if len(items) != 2 {
		t.Fatalf("history %#v", items)
	}
	run := items[0]
	if run["outcome"] != "in_flight" || run["finished_at"] != nil || run["digest"] != nil || run["via"] != "mcp" ||
		run["started_at"] != "2026-10-13T11:59:00Z" || run["ends_by"] != "2026-10-13T12:08:00Z" {
		t.Fatalf("running row %#v", run)
	}
	if items[1]["outcome"] != "succeeded" || items[1]["ends_by"] != nil {
		t.Fatalf("finished row %#v", items[1])
	}
	// The stores keep listing finished rows only.
	rows, err := w.mem.ListImageDeploys(ctx, entryID, sc.host.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("store history %#v %v", rows, err)
	}
	if _, err := w.mem.BeginImageOp(ctx, entryID, sc.host.ID, hash, owner.ID, "console", w.now, w.now.Add(time.Hour)); err == nil {
		t.Fatal("second begin")
	} else if running, ok := err.(*store.ImageOpRunning); !ok || !running.StartedAt.Equal(w.now.Add(-time.Minute)) || !running.Until.Equal(w.now.Add(8*time.Minute)) {
		t.Fatalf("running error %#v", err)
	}
}

// api-v1 v1.42 item 2: an image confirmation whose plan no longer matches
// the entry gets its own message.
func TestImageConfirmPlanChanged(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	log := watchTasks(t, conn, func(msg map[string]any) map[string]any {
		return imageDeployReply(msg, shaDigest(4))
	})
	delegated := tokenKind(t, w, owner.ID, store.KindDelegated)
	status, env, _ := postImage(t, w, delegated, project, "web", "deploy", host, "plan-1", nil)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	id, _ := objectPayload(t, env)["confirmation_id"].(string)
	status, env = w.do(tok, http.MethodPut, "/v1/projects/"+project+"/catalog/web", `{"kind":"image","spec":{"kind":"service","image":"nginx:1.27"}}`)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("plan-1"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", msgPlanChanged)
	if log.count() != 0 {
		t.Fatal("changed plan dispatched")
	}
	// The conflict is not stored and the stale plan is released: the same
	// key plans again, and that plan runs.
	status, env, _ = postImage(t, w, delegated, project, "web", "deploy", host, "plan-1", nil)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	again, _ := objectPayload(t, env)["confirmation_id"].(string)
	if again == "" || again == id {
		t.Fatalf("replayed the stale plan %q", again)
	}
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+again, confirmBody("plan-1"))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if log.count() != 1 {
		t.Fatalf("new plan dispatched %d tasks", log.count())
	}
}

// api-v1 v1.42 item 2: a running-operation conflict is not stored under the
// request key. The same key, sent after the operation ended, runs. For a
// delegated caller the plan's key is released too, so it plans again.
func TestImageRunningConflictNotStored(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	created := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	entryID := uuid.MustParse(created["id"].(string))
	hash, _ := created["spec_hash"].(string)
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	log := watchTasks(t, conn, func(msg map[string]any) map[string]any {
		return imageDeployReply(msg, shaDigest(5))
	})
	ctx := context.Background()
	hold := func() uuid.UUID {
		t.Helper()
		row, err := w.mem.BeginImageOp(ctx, entryID, sc.host.ID, hash, owner.ID, "mcp", w.now.Add(-time.Minute), w.now.Add(8*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return row.ID
	}

	op := hold()
	status, env, _ := postImage(t, w, tok, project, "web", "deploy", host, "same-key", nil)
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "A deploy, rollback or remove of web on pi4 has been running since 11:59 UTC. Try again after 12:08 UTC.")
	if _, err := w.mem.GetIdempotency(ctx, owner.ID, "same-key", w.now); err == nil {
		t.Fatal("running conflict stored under the key")
	}
	if err := w.mem.ReleaseImageOp(ctx, op); err != nil {
		t.Fatal(err)
	}
	status, env, _ = postImage(t, w, tok, project, "web", "deploy", host, "same-key", nil)
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if log.count() != 1 {
		t.Fatalf("retry dispatched %d tasks", log.count())
	}

	delegated := tokenKind(t, w, owner.ID, store.KindDelegated)
	status, env, _ = postImage(t, w, delegated, project, "web", "deploy", host, "plan-key", nil)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	first, _ := objectPayload(t, env)["confirmation_id"].(string)
	op = hold()
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+first, confirmBody("plan-key"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "A deploy, rollback or remove of web on pi4 has been running since 11:59 UTC. Try again after 12:08 UTC.")
	if _, err := w.mem.GetIdempotency(ctx, owner.ID, "plan-key", w.now); err == nil {
		t.Fatal("plan key kept after a running conflict")
	}
	if err := w.mem.ReleaseImageOp(ctx, op); err != nil {
		t.Fatal(err)
	}
	status, env, _ = postImage(t, w, delegated, project, "web", "deploy", host, "plan-key", nil)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	second, _ := objectPayload(t, env)["confirmation_id"].(string)
	if second == "" || second == first {
		t.Fatalf("replayed the used plan %q", second)
	}
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+second, confirmBody("plan-key"))
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if log.count() != 2 {
		t.Fatalf("delegated retry dispatched %d tasks", log.count())
	}
}

// api-v1 v1.42 item 3 and spec §9 item 2: a deploy or rollback lock lasts the
// task budget (390 s for this entry) + the 30 s result margin + the 120 s
// stale grace.
func TestImageLockDeadline(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	created := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	entryID := uuid.MustParse(created["id"].(string))
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	var mu sync.Mutex
	block := false
	sent := make(chan struct{}, 1)
	release := make(chan struct{})
	digests := []string{shaDigest(1), shaDigest(2)}
	n := 0
	watchTasks(t, conn, func(msg map[string]any) map[string]any {
		mu.Lock()
		wait := block
		d := digests[n%2]
		n++
		mu.Unlock()
		if wait {
			sent <- struct{}{}
			<-release
		}
		return imageDeployReply(msg, d)
	})
	// Two finished deploys give rollback a digest to go back to.
	for _, key := range []string{"lock-a", "lock-b"} {
		status, env, _ := postImage(t, w, tok, project, "web", "deploy", host, key, nil)
		mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	}
	want := w.now.Add((390 + resultMarginSec + staleGraceSec) * time.Second)
	for _, tc := range []struct {
		action string
		extra  map[string]any
	}{
		{"deploy", nil},
		{"rollback", map[string]any{"digest": digests[0]}},
	} {
		mu.Lock()
		block = true
		mu.Unlock()
		done := make(chan callResult, 1)
		go func() {
			status, env, raw, err := callRaw(w, tok, http.MethodPost, imagePath(project, "web", tc.action), imageBody(host, "held-"+tc.action, tc.extra))
			done <- callResult{status, env, raw, err}
		}()
		select {
		case <-sent:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s task was not sent", tc.action)
		}
		row, err := w.mem.RunningImageOp(context.Background(), entryID, sc.host.ID)
		if err != nil || !row.InFlightUntil.Equal(want) {
			t.Fatalf("%s lock until %v, want %v (%v)", tc.action, row.InFlightUntil, want, err)
		}
		mu.Lock()
		block = false
		mu.Unlock()
		release <- struct{}{}
		got := <-done
		if got.err != nil {
			t.Fatal(got.err)
		}
		mustHTTP(t, got.status, got.env, http.StatusOK, "ok", "ok")
	}
}

// api-v1 v1.42 item 1: History holds the running row, then at most 50
// finished rows.
func TestImageHistoryRunningPlusFifty(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	created := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	entryID := uuid.MustParse(created["id"].(string))
	hash, _ := created["spec_hash"].(string)
	ctx := context.Background()
	for i := 0; i < 55; i++ {
		at := w.now.Add(-time.Duration(60-i) * time.Minute)
		row, err := w.mem.BeginImageOp(ctx, entryID, sc.host.ID, hash, owner.ID, "console", at, at.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		d := shaDigest(byte(i%9 + 1))
		if _, err := w.mem.FinishImageDeploy(ctx, row.ID, "succeeded", &d, nil, at.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.mem.BeginImageOp(ctx, entryID, sc.host.ID, hash, owner.ID, "console", w.now, w.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	status, env, _ := mustCall(t, w, tok, http.MethodGet, "/v1/projects/"+project+"/catalog/web/deploys?host_id="+host, "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	items := listItems(t, env)
	if len(items) != 51 || items[0]["outcome"] != "in_flight" {
		t.Fatalf("history has %d rows, first %#v", len(items), items[0])
	}
	for _, row := range items[1:] {
		if row["outcome"] != "succeeded" {
			t.Fatalf("finished row %#v", row)
		}
	}
}
