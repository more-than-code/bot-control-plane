package agentws

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/memstore"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// TestDeployHookAfterAutoAction: a stored auto action calls the deploy hook
// (api-v1 v1.41) with the entry's project, id and name and the host; a
// replayed frame does not call it again.
func TestDeployHookAfterAutoAction(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	clock := &autoClock{at: time.Now().UTC().Truncate(time.Second)}
	hub := New(st, audit.Writer{Store: st, Log: log, Now: clock.now}, log, clock.now)
	type call struct {
		project, entry uuid.UUID
		name           string
		host           uuid.UUID
	}
	var mu sync.Mutex
	var calls []call
	hub.SetDeployHook(func(_ context.Context, projectID, entryID uuid.UUID, entry string, hostID uuid.UUID) {
		mu.Lock()
		calls = append(calls, call{projectID, entryID, entry, hostID})
		mu.Unlock()
	})
	srv := httptest.NewServer(hub)
	t.Cleanup(func() {
		hub.CloseAll()
		srv.Close()
	})
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	user, err := st.UpsertGoogleUser(ctx, store.GoogleSignIn{Subject: "sub", Email: "a@b.c", Name: "A", At: clock.now()})
	if err != nil {
		t.Fatal(err)
	}
	project, err := st.CreateProject(ctx, st.PersonalOrg(user.ID), user.ID, "P", "p", clock.now())
	if err != nil {
		t.Fatal(err)
	}
	web := autoEntry(t, st, project.ID, user.ID, "web", autoPolicy("rollback"), clock.now())
	joinPlain, joinHash, err := secret.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateJoinToken(ctx, project.OrgID, joinHash, nil, &store.TokenGrant{ProjectID: project.ID, Level: "deploy"}, user.ID, clock.now().Add(time.Hour), clock.now()); err != nil {
		t.Fatal(err)
	}
	enrollConn := dial(t, url, "")
	writeMsg(t, enrollConn, map[string]any{
		"v": 1, "type": "enroll", "join_token": joinPlain,
		"hostname": "pi", "agent_version": "0.1.0", "os": "linux", "arch": "arm64", "always_on": true,
	})
	enrolled := readMsg(t, enrollConn)
	hostID := uuid.MustParse(enrolled["host_id"].(string))
	began, err := st.BeginImageOp(ctx, web.ID, hostID, strings.Repeat("a", 64), user.ID, "console", clock.now().Add(-time.Hour), clock.now())
	if err != nil {
		t.Fatal(err)
	}
	digest := autoDigest('1')
	if _, err := st.FinishImageDeploy(ctx, began.ID, "succeeded", &digest, nil, clock.now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	conn := dial(t, url, enrolled["credential"].(string))
	writeMsg(t, conn, map[string]any{
		"v": 1, "type": "hello", "host_id": hostID.String(), "credential_id": enrolled["credential_id"].(string),
		"agent_version": "0.2.6", "os": "linux", "arch": "arm64", "services": []any{},
		"programs": map[string]any{"enabled": true, "run_as": []string{"joe"}, "allow_shell": false, "policy_hash": "abc",
			"images": true, "docker": true, "healthcheck": true, "health_policy": true},
	})
	if got := readMsg(t, conn); got["type"] != "hello_ok" {
		t.Fatalf("hello %#v", got)
	}
	id := uuid.New().String()
	frame := autoFrame(id, "p", "web", "rollback", "ok", autoDigest('2'), clock.now().Add(-time.Minute))
	clock.add(time.Second)
	writeMsg(t, conn, frame)
	expectAck(t, conn, id)
	clock.add(time.Second)
	writeMsg(t, conn, frame)
	expectAck(t, conn, id)
	hub.WaitHooks()
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != (call{project.ID, web.ID, "web", hostID}) {
		t.Fatalf("hook calls %+v", calls)
	}
}
