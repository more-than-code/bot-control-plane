package agentws

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/memstore"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// TestEnrollAuditProject is api-v1 v1.24 item 5 after the v1.26 removals:
// the host.enroll row carries the org, the host, and the join token's grant
// project at either level (not the org's oldest project), or no project for
// a token without a grant.
func TestEnrollAuditProject(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := func() time.Time { return time.Now().UTC() }
	hub := New(st, audit.Writer{Store: st, Log: log, Now: now}, log, now)
	srv := httptest.NewServer(hub)
	t.Cleanup(func() {
		hub.CloseAll()
		srv.Close()
	})
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	user, err := st.UpsertGoogleUser(ctx, store.GoogleSignIn{Subject: "enrol-audit", Email: "enrol@audit.test", Name: "E", At: now()})
	if err != nil {
		t.Fatal(err)
	}
	org := st.PersonalOrg(user.ID)
	// oldest is the org's oldest project, so a grant to later proves the row
	// names the grant, not the old mirror rule.
	oldest, err := st.CreateProject(ctx, org, user.ID, "Oldest", "enrol-oldest", now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	later, err := st.CreateProject(ctx, org, user.ID, "Later", "enrol-later", now())
	if err != nil {
		t.Fatal(err)
	}

	enroll := func(name string, grant *store.TokenGrant) uuid.UUID {
		t.Helper()
		plain, hash, err := secret.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateJoinToken(ctx, org, hash, nil, grant, user.ID, now().Add(time.Hour), now()); err != nil {
			t.Fatal(err)
		}
		c := dial(t, url, "")
		writeMsg(t, c, map[string]any{
			"v": 1, "type": "enroll", "join_token": plain,
			"hostname": name, "agent_version": "0.2.9",
			"os": "linux", "arch": "arm64", "always_on": true,
		})
		enrolled := readMsg(t, c)
		expectClose(t, c, websocket.StatusNormalClosure)
		if enrolled["type"] != "enrolled" {
			t.Fatalf("%s: %#v", name, enrolled["type"])
		}
		return uuid.MustParse(enrolled["host_id"].(string))
	}
	enrollRow := func(hostID uuid.UUID) store.AuditEntry {
		t.Helper()
		items, _, err := st.ListOrgAudit(ctx, org, 200, nil)
		if err != nil {
			t.Fatal(err)
		}
		var found []store.AuditEntry
		for _, a := range items {
			if a.Action == audit.ActionHostEnroll && a.HostID != nil && *a.HostID == hostID {
				found = append(found, a)
			}
		}
		if len(found) != 1 {
			t.Fatalf("host.enroll rows for %s: %d", hostID, len(found))
		}
		row := found[0]
		if row.OrgID == nil || *row.OrgID != org || row.Via != audit.ViaAgent || row.Decision != audit.DecisionAllowed {
			t.Fatalf("enroll row %+v", row)
		}
		return row
	}

	for _, tc := range []struct {
		name  string
		grant *store.TokenGrant
		want  *uuid.UUID
	}{
		{"deploy-grant", &store.TokenGrant{ProjectID: later.ID, Level: "deploy"}, &later.ID},
		{"metrics-grant", &store.TokenGrant{ProjectID: later.ID, Level: "metrics"}, &later.ID},
		{"no-grant", nil, nil},
	} {
		row := enrollRow(enroll(tc.name, tc.grant))
		switch {
		case tc.want == nil && row.ProjectID != nil:
			t.Fatalf("%s: project %s, want none (oldest is %s)", tc.name, row.ProjectID, oldest.ID)
		case tc.want != nil && (row.ProjectID == nil || *row.ProjectID != *tc.want):
			t.Fatalf("%s: project %v, want %s", tc.name, row.ProjectID, tc.want)
		}
	}
}
