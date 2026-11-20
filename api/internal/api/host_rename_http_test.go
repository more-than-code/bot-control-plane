package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

const hostNameMsg = "name must be 1–63 characters: lowercase letters, digits and -"

func renameBody(name string) string {
	b, _ := json.Marshal(map[string]string{"name": name})
	return string(b)
}

func hostRenameRows(w *world) []store.AuditEntry {
	var out []store.AuditEntry
	for _, row := range w.mem.AllAudits() {
		if row.Action == audit.ActionHostRename && row.Decision == audit.DecisionAllowed {
			out = append(out, row)
		}
	}
	return out
}

func hostName(t *testing.T, w *world, id uuid.UUID) string {
	t.Helper()
	h, err := w.mem.GetHost(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return h.Name
}

// TestHostRenameRoles is spec-host-names 7.2: org owners and admins rename;
// project roles alone, including a project admin, get forbidden; a caller
// who cannot see the host gets not_found; a delegated session gets
// forbidden.
func TestHostRenameRoles(t *testing.T) {
	want := map[string]int{
		actOwnerOwner:    http.StatusOK,
		actOwnerAdmin:    http.StatusOK,
		actOwnerViewer:   http.StatusForbidden,
		actMetricsViewer: http.StatusForbidden,
		actMetricsAdmin:  http.StatusForbidden,
		actDeployViewer:  http.StatusForbidden,
		actDeployAdmin:   http.StatusForbidden,
		actOrgMember:     http.StatusNotFound,
		actNone:          http.StatusNotFound,
	}
	for _, actor := range accessActors {
		fx := newAccessFixture(t)
		before := fx.sc.host.Name
		name := "renamed-" + strings.ReplaceAll(actor, "_", "-")
		code, env := fx.do(actor, http.MethodPatch, fx.hostPath(""), renameBody(name))
		if code != want[actor] {
			t.Fatalf("%s: %d %s %s, want %d", actor, code, env.Code, env.Msg, want[actor])
		}
		got := hostName(t, fx.w, fx.sc.host.ID)
		if code != http.StatusOK {
			if got != before || len(hostRenameRows(fx.w)) != 0 {
				t.Fatalf("%s: refused rename changed the host to %q or wrote a host.rename row", actor, got)
			}
			continue
		}
		payload := objectPayload(t, env)
		if payload["id"] != fx.sc.host.ID.String() || payload["name"] != name || got != name {
			t.Fatalf("%s: payload %v stored %q", actor, payload, got)
		}
	}

	fx := newAccessFixture(t)
	owner := fx.users[actOwnerOwner]
	tok := tokenKind(t, fx.w, owner.ID, store.KindDelegated)
	code, env := fx.w.do(tok, http.MethodPatch, fx.hostPath(""), renameBody("from-mcp"))
	mustHTTP(t, code, env, http.StatusForbidden, "forbidden", "forbidden")
	if got := hostName(t, fx.w, fx.sc.host.ID); got != fx.sc.host.Name {
		t.Fatalf("delegated rename stored %q", got)
	}
}

// TestHostRenameRules is spec-host-names 7.2: the name rule, the strict
// body, a live duplicate, a revoked host and the same name.
func TestHostRenameRules(t *testing.T) {
	fx := newAccessFixture(t)
	w := fx.w
	owner := fx.users[actOwnerOwner]
	before := fx.sc.host.Name

	for _, body := range []string{
		renameBody(""), renameBody("Pi2"), renameBody("pi_2"), renameBody("pi 2"), renameBody(strings.Repeat("a", 64)),
		`{}`, `{"name":null}`, `{"name":7}`,
	} {
		code, env := fx.do(actOwnerOwner, http.MethodPatch, fx.hostPath(""), body)
		if body == `{"name":7}` {
			mustHTTP(t, code, env, http.StatusBadRequest, "bad_request", "invalid json")
			continue
		}
		mustHTTP(t, code, env, http.StatusBadRequest, "bad_request", hostNameMsg)
	}
	code, env := fx.do(actOwnerOwner, http.MethodPatch, fx.hostPath(""), `{"name":"ok","always_on":false}`)
	mustHTTP(t, code, env, http.StatusBadRequest, "bad_request", "invalid json")
	if got := hostName(t, w, fx.sc.host.ID); got != before {
		t.Fatalf("bad request renamed to %q", got)
	}

	// A live host of the org already has the name.
	other := enrollHost(t, w, fx.home, owner.ID, "pi2")
	code, env = fx.do(actOwnerOwner, http.MethodPatch, fx.hostPath(""), renameBody("pi2"))
	mustHTTP(t, code, env, http.StatusConflict, "conflict", "a host named pi2 already exists in this organization")
	if got := hostName(t, w, fx.sc.host.ID); got != before {
		t.Fatalf("conflict renamed to %q", got)
	}

	// The 63-character maximum is accepted.
	long := strings.Repeat("a", 63)
	code, env = fx.do(actOwnerOwner, http.MethodPatch, fx.hostPath(""), renameBody(long))
	if code != http.StatusOK {
		t.Fatalf("63 characters %d %s %s", code, env.Code, env.Msg)
	}

	// The same name: 200 with the host, nothing changes, no audit row.
	rows := len(hostRenameRows(w))
	all := len(w.mem.AllAudits())
	code, env = fx.do(actOwnerOwner, http.MethodPatch, fx.hostPath(""), renameBody(long))
	if code != http.StatusOK || objectPayload(t, env)["name"] != long {
		t.Fatalf("same name %d %v", code, env.Payload)
	}
	if len(hostRenameRows(w)) != rows || len(w.mem.AllAudits()) != all {
		t.Fatal("same name wrote an audit row")
	}

	// A revoked host cannot be renamed, not even to its own name.
	code, env = fx.do(actOwnerOwner, http.MethodDelete, "/v1/hosts/"+other.ID.String()+"/credential", "")
	if code != http.StatusOK {
		t.Fatalf("revoke %d %s", code, env.Code)
	}
	for _, name := range []string{"fresh", "pi2"} {
		code, env = fx.do(actOwnerOwner, http.MethodPatch, "/v1/hosts/"+other.ID.String(), renameBody(name))
		mustHTTP(t, code, env, http.StatusConflict, "conflict", "this host is revoked")
	}
	if got := hostName(t, w, other.ID); got != "pi2" {
		t.Fatalf("revoked host renamed to %q", got)
	}
	if len(hostRenameRows(w)) != rows {
		t.Fatal("refusal wrote a host.rename row")
	}

	// Unknown host.
	code, env = fx.do(actOwnerOwner, http.MethodPatch, "/v1/hosts/"+uuid.NewString(), renameBody("ghost"))
	if code != http.StatusNotFound {
		t.Fatalf("unknown host %d %s", code, env.Code)
	}
}

// TestHostRenameRevokedName is spec-host-names 7.2 (audit) and 7.4: renaming
// the live host to a name only a revoked host holds succeeds, writes one
// host.rename row with from and to, and the org then lists two hosts with
// that name, one revoked.
func TestHostRenameRevokedName(t *testing.T) {
	fx := newAccessFixture(t)
	w := fx.w
	owner := fx.users[actOwnerOwner]
	from := fx.sc.host.Name
	old := enrollHost(t, w, fx.home, owner.ID, "pi2")
	if err := w.mem.RevokeHost(context.Background(), old.ID, w.now); err != nil {
		t.Fatal(err)
	}
	code, env := fx.do(actOwnerAdmin, http.MethodPatch, fx.hostPath(""), renameBody("pi2"))
	if code != http.StatusOK || objectPayload(t, env)["name"] != "pi2" {
		t.Fatalf("rename onto revoked name %d %s %s", code, env.Code, env.Msg)
	}
	rows := hostRenameRows(w)
	if len(rows) != 1 {
		t.Fatalf("host.rename rows %d", len(rows))
	}
	row := rows[0]
	if row.HostID == nil || *row.HostID != fx.sc.host.ID || row.OrgID == nil || *row.OrgID != fx.home.OrgID {
		t.Fatalf("audit row host %v org %v", row.HostID, row.OrgID)
	}
	if row.ActorUserID == nil || *row.ActorUserID != fx.users[actOwnerAdmin].ID {
		t.Fatalf("audit actor %v", row.ActorUserID)
	}
	var detail map[string]any
	if err := json.Unmarshal(row.Detail, &detail); err != nil || detail["from"] != from || detail["to"] != "pi2" || len(detail) != 2 {
		t.Fatalf("audit detail %s %v", row.Detail, err)
	}

	code, env = fx.do(actOwnerOwner, http.MethodGet, "/v1/orgs/"+fx.home.OrgID.String()+"/hosts?limit=200", "")
	if code != http.StatusOK {
		t.Fatalf("org hosts %d %s", code, env.Code)
	}
	statuses := map[string]bool{}
	for _, item := range listItems(t, env) {
		if item["name"] == "pi2" {
			status, _ := item["status"].(string)
			statuses[status] = true
		}
	}
	if len(statuses) != 2 || !statuses["revoked"] {
		t.Fatalf("pi2 rows by status %v", statuses)
	}
}

// TestHostRenameKeepsAgent is spec-host-names 7.3: the host keeps its id,
// its agent stays connected, and services and access are unchanged.
func TestHostRenameKeepsAgent(t *testing.T) {
	fx := newAccessFixture(t)
	w := fx.w
	_ = w.hello(fx.sc)
	waitFor(t, func() bool { return w.srv.hostStatus(fx.sc.host) == "online" })
	services := func() string {
		code, env := fx.do(actOwnerOwner, http.MethodGet, fx.hostPath("/services"), "")
		if code != http.StatusOK {
			t.Fatalf("services %d", code)
		}
		b, _ := json.Marshal(listItems(t, env))
		return string(b)
	}
	access := func() string {
		code, env := fx.do(actOwnerOwner, http.MethodGet, fx.hostPath("/access"), "")
		if code != http.StatusOK {
			t.Fatalf("access %d", code)
		}
		b, _ := json.Marshal(env.Payload)
		return string(b)
	}
	svcBefore, accessBefore := services(), access()

	code, env := fx.do(actOwnerOwner, http.MethodPatch, fx.hostPath(""), renameBody("pi3"))
	if code != http.StatusOK {
		t.Fatalf("rename %d %s %s", code, env.Code, env.Msg)
	}
	payload := objectPayload(t, env)
	if payload["id"] != fx.sc.host.ID.String() || payload["status"] != "online" || payload["name"] != "pi3" {
		t.Fatalf("payload id %v status %v name %v", payload["id"], payload["status"], payload["name"])
	}
	if _, ok := payload["access"]; !ok {
		t.Fatal("org admin view lacks access, unlike GET /v1/hosts/{id}")
	}
	if w.srv.hostStatus(fx.sc.host) != "online" {
		t.Fatal("agent dropped by the rename")
	}
	if services() != svcBefore || access() != accessBefore {
		t.Fatal("services or access changed")
	}
	code, env = fx.do(actOwnerOwner, http.MethodGet, fx.hostPath(""), "")
	if code != http.StatusOK || objectPayload(t, env)["name"] != "pi3" || objectPayload(t, env)["status"] != "online" {
		t.Fatalf("get after rename %d %v", code, env.Payload)
	}
}

// TestEnrolFreesRevokedName is spec-host-names 7.1 through the agent socket:
// with only a revoked pi2 in the org a new pi2 keeps its name, with a live
// pi2 it gets pi2-2.
func TestEnrolFreesRevokedName(t *testing.T) {
	w := newWorld(t)
	owner := w.user("owner@ex.com", "Owner")
	project, err := w.mem.CreateProject(context.Background(), w.mem.PersonalOrg(owner.ID), owner.ID, "Lab", "lab", w.now)
	if err != nil {
		t.Fatal(err)
	}
	enrolSocket := func(hostname string) string {
		t.Helper()
		plain, hash, err := secret.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.mem.CreateJoinToken(context.Background(), project.OrgID, hash, nil, nil, owner.ID, w.now.Add(time.Hour), w.now); err != nil {
			t.Fatal(err)
		}
		conn := w.dial("")
		writeFrame(t, conn, map[string]any{
			"v": 1, "type": "enroll", "join_token": plain,
			"hostname": hostname, "agent_version": "0.2.11", "os": "linux", "arch": "arm64", "always_on": true,
		})
		enrolled := readFrame(t, conn)
		id, err := uuid.Parse(enrolled["host_id"].(string))
		if enrolled["type"] != "enrolled" || err != nil {
			t.Fatalf("enrolled %v", enrolled["type"])
		}
		return hostName(t, w, id)
	}
	old := enrollHost(t, w, project, owner.ID, "pi2")
	if err := w.mem.RevokeHost(context.Background(), old.ID, w.now); err != nil {
		t.Fatal(err)
	}
	if got := enrolSocket("pi2"); got != "pi2" {
		t.Fatalf("with a revoked pi2: %q", got)
	}
	if got := enrolSocket("pi2"); got != "pi2-2" {
		t.Fatalf("with a live pi2: %q", got)
	}
}
