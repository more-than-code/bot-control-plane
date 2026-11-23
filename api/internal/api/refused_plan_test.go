package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/more-than-code/bot-control-plane/api/internal/agentws"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.55: a bad_spec refusal names the field the agent sent (item 1),
// and a deploy confirmation the host refused stays usable (item 2).
func TestRefusedDeployFieldAndPlanKept(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	project := sc.project.ID.String()
	host := sc.host.ID.String()
	tok := w.token(owner.ID)
	created := createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	entryID := uuid.MustParse(created["id"].(string))

	var n atomic.Int32
	fields := []string{"spec.secret_env", "spec.DB_URL", ""}
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	tasks := watchTasks(t, conn, func(msg map[string]any) map[string]any {
		i := int(n.Add(1)) - 1
		rej := map[string]any{"v": 1, "type": "task_reject", "task_id": msg["task_id"], "error_code": "bad_spec"}
		if i < len(fields) && fields[i] != "" {
			rej["field"] = fields[i]
		}
		return rej
	})

	delegated := tokenKind(t, w, owner.ID, store.KindDelegated)
	status, env, _ := postImage(t, w, delegated, project, "web", "deploy", host, "plan-1", nil)
	mustHTTP(t, status, env, http.StatusAccepted, "ok", "ok")
	id, _ := objectPayload(t, env)["confirmation_id"].(string)

	// First confirm: the field and its hint, then the plan is kept.
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("plan-1"))
	msg := envelopeMessage(t, status, env, http.StatusBadRequest)
	if !strings.Contains(msg, `field "spec.secret_env" did not pass`) || !strings.Contains(msg, specFieldHints["spec.secret_env"]) {
		t.Fatalf("message %q", msg)
	}
	if !strings.Contains(msg, "this plan is kept") || !strings.Contains(msg, "until 12:05 UTC") {
		t.Fatalf("plan not kept: %q", msg)
	}

	// Second confirm, same id and key: the host is asked again. A field
	// that is not a key name is dropped, so the old sentence comes back.
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("plan-1"))
	msg = envelopeMessage(t, status, env, http.StatusBadRequest)
	if tasks.count() != 2 || !strings.HasPrefix(msg, msgSpecRefused) || strings.Contains(msg, "DB_URL") || !strings.Contains(msg, "this plan is kept") {
		t.Fatalf("tasks %d message %q", tasks.count(), msg)
	}

	// The stored reason has no plan sentence; each refusal is its own row.
	rows, err := w.mem.ListImageDeploys(context.Background(), entryID, sc.host.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("history %d %v", len(rows), err)
	}
	for _, row := range rows {
		if row.Outcome != "refused" || row.Reason == nil || strings.Contains(*row.Reason, "plan") {
			t.Fatalf("row %#v", row)
		}
	}

	// A wrong key is still refused and nothing runs.
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("other"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "idempotency_key does not match the request that issued this confirmation")

	// Past the kept plan's expiry it is gone.
	later := w.now.Add(store.ConfirmationTTL + time.Minute)
	w.srv.Now = func() time.Time { return later }
	status, env, _ = mustCall(t, w, delegated, http.MethodPost, "/v1/confirmations/"+id, confirmBody("plan-1"))
	mustHTTP(t, status, env, http.StatusConflict, "conflict", "this confirmation expired; call the tool again for a new one")
	if tasks.count() != 2 {
		t.Fatalf("expired plan ran: %d", tasks.count())
	}
}

// A console deploy has no plan: the refusal has no plan sentence.
func TestRefusedConsoleDeployNoPlanSentence(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	sc := w.scene(owner, w.user("a@ex.com", "A"), w.user("v@ex.com", "V"), w.user("s@ex.com", "S"), w.user("e@ex.com", "E"))
	tok := w.token(owner.ID)
	createImage(t, w, tok, sc.project.ID, "web", `{"kind":"service","image":"nginx"}`)
	var field atomic.Value
	conn := helloAgent(t, w, sc.host, sc.credPlain, sc.credID, allowImages(), nil)
	watchTasks(t, conn, func(msg map[string]any) map[string]any {
		return map[string]any{"v": 1, "type": "task_reject", "task_id": msg["task_id"], "error_code": "bad_spec", "field": field.Load()}
	})
	// Every hint, and a secret-looking field name, comes back unredacted.
	names := []string{"secret_keys", "spec.healthcheck.timeout_sec"}
	for name := range specFieldHints {
		names = append(names, name)
	}
	for i, name := range names {
		field.Store(name)
		status, env, _ := postImage(t, w, tok, sc.project.ID.String(), "web", "deploy", sc.host.ID.String(), "c-"+strconv.Itoa(i), nil)
		mustHTTP(t, status, env, http.StatusBadRequest, "bad_request", specRefused(name))
	}
}

func TestRejectField(t *testing.T) {
	for in, want := range map[string]string{
		"env": "env", "spec.healthcheck.timeout_sec": "spec.healthcheck.timeout_sec",
		"spec.DB_URL": "", "a.b.c.d": "", "": "", "spec secret": "", strings.Repeat("a", 65): "",
	} {
		if got := agentws.RejectField(in); got != want {
			t.Fatalf("%q: %q", in, got)
		}
	}
}

func envelopeMessage(t *testing.T, status int, env envelope, want int) string {
	t.Helper()
	if status != want || env.Code != "bad_request" {
		t.Fatalf("status %d code %s msg %q", status, env.Code, env.Msg)
	}
	return env.Msg
}
