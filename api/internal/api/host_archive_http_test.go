package api

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

func hostArchiveRows(w *world, decision string) []store.AuditEntry {
	var out []store.AuditEntry
	for _, row := range w.mem.AllAudits() {
		if row.Action == audit.ActionHostArchive && row.Decision == decision {
			out = append(out, row)
		}
	}
	return out
}

func hostArchivedAt(t *testing.T, w *world, id uuid.UUID) any {
	t.Helper()
	h, err := w.mem.GetHost(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if h.ArchivedAt == nil {
		return nil
	}
	return *h.ArchivedAt
}

// revokeFixtureHost revokes the fixture host in the store, so it may be archived.
func revokeFixtureHost(t *testing.T, fx *accessFixture) {
	t.Helper()
	if err := fx.w.mem.RevokeHost(context.Background(), fx.sc.host.ID, fx.w.now); err != nil {
		t.Fatal(err)
	}
}

// TestHostArchiveRoles is api-v1 v1.40 item 2 (spec-host-remove 6.2): org
// owners and admins archive a revoked host; project roles alone, including
// a project admin, get forbidden (audited as host.archive denied); a caller
// who cannot see the host gets not_found; a delegated session gets
// forbidden.
func TestHostArchiveRoles(t *testing.T) {
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
		revokeFixtureHost(t, fx)
		code, env := fx.do(actor, http.MethodPost, fx.hostPath("/archive"), "")
		if code != want[actor] {
			t.Fatalf("%s: %d %s %s, want %d", actor, code, env.Code, env.Msg, want[actor])
		}
		got := hostArchivedAt(t, fx.w, fx.sc.host.ID)
		if code != http.StatusOK {
			if got != nil || len(hostArchiveRows(fx.w, audit.DecisionAllowed)) != 0 {
				t.Fatalf("%s: refused archive set archived_at %v or wrote a host.archive row", actor, got)
			}
			if code == http.StatusForbidden && len(hostArchiveRows(fx.w, audit.DecisionDenied)) != 1 {
				t.Fatalf("%s: denial not audited as host.archive", actor)
			}
			continue
		}
		payload := objectPayload(t, env)
		if payload["id"] != fx.sc.host.ID.String() || payload["status"] != "revoked" || payload["archived_at"] == nil || got == nil {
			t.Fatalf("%s: payload %v stored %v", actor, payload, got)
		}
		rows := hostArchiveRows(fx.w, audit.DecisionAllowed)
		if len(rows) != 1 {
			t.Fatalf("%s: host.archive rows %d", actor, len(rows))
		}
		row := rows[0]
		if row.HostID == nil || *row.HostID != fx.sc.host.ID || row.OrgID == nil || *row.OrgID != fx.home.OrgID ||
			row.ActorUserID == nil || *row.ActorUserID != fx.users[actor].ID {
			t.Fatalf("%s: audit row host %v org %v actor %v", actor, row.HostID, row.OrgID, row.ActorUserID)
		}
	}

	fx := newAccessFixture(t)
	revokeFixtureHost(t, fx)
	owner := fx.users[actOwnerOwner]
	tok := tokenKind(t, fx.w, owner.ID, store.KindDelegated)
	code, env := fx.w.do(tok, http.MethodPost, fx.hostPath("/archive"), "")
	mustHTTP(t, code, env, http.StatusForbidden, "forbidden", "forbidden")
	if got := hostArchivedAt(t, fx.w, fx.sc.host.ID); got != nil {
		t.Fatalf("delegated archive stored %v", got)
	}
}

// TestHostArchiveRules is api-v1 v1.40 item 2 (spec-host-remove 6.2): a
// live host is a conflict, archiving twice answers 200 with one audit row
// and the first time, an unknown host is not_found.
func TestHostArchiveRules(t *testing.T) {
	fx := newAccessFixture(t)
	w := fx.w

	code, env := fx.do(actOwnerOwner, http.MethodPost, fx.hostPath("/archive"), "")
	mustHTTP(t, code, env, http.StatusConflict, "conflict", "revoke this host first")
	if got := hostArchivedAt(t, w, fx.sc.host.ID); got != nil || len(hostArchiveRows(w, audit.DecisionAllowed)) != 0 {
		t.Fatalf("live host archived %v or audited", got)
	}

	code, env = fx.do(actOwnerOwner, http.MethodDelete, fx.hostPath("/credential"), "")
	if code != http.StatusOK {
		t.Fatalf("revoke %d %s", code, env.Code)
	}
	code, env = fx.do(actOwnerOwner, http.MethodPost, fx.hostPath("/archive"), "")
	if code != http.StatusOK {
		t.Fatalf("archive %d %s %s", code, env.Code, env.Msg)
	}
	first := objectPayload(t, env)["archived_at"]
	if first == nil {
		t.Fatal("archived_at missing")
	}
	all := len(w.mem.AllAudits())

	// Twice: 200 with the host, the same time, no audit row.
	w.now = w.now.Add(1e9)
	code, env = fx.do(actOwnerAdmin, http.MethodPost, fx.hostPath("/archive"), "")
	if code != http.StatusOK {
		t.Fatalf("archive again %d %s %s", code, env.Code, env.Msg)
	}
	if again := objectPayload(t, env)["archived_at"]; again != first {
		t.Fatalf("archived_at moved from %v to %v", first, again)
	}
	if len(hostArchiveRows(w, audit.DecisionAllowed)) != 1 || len(w.mem.AllAudits()) != all {
		t.Fatal("second archive wrote an audit row")
	}

	code, env = fx.do(actOwnerOwner, http.MethodPost, "/v1/hosts/"+uuid.NewString()+"/archive", "")
	if code != http.StatusNotFound {
		t.Fatalf("unknown host %d %s", code, env.Code)
	}
}

// TestHostArchiveLists is api-v1 v1.40 item 3 (spec-host-remove 6.1 and
// 6.3): an archived host leaves the org and project host lists and its
// rows leave every project services view; reads by id still answer, with
// archived_at set.
func TestHostArchiveLists(t *testing.T) {
	fx := newAccessFixture(t)
	host := fx.sc.host.ID.String()
	listed := func(path string) bool {
		t.Helper()
		code, env := fx.do(actOwnerOwner, http.MethodGet, path, "")
		if code != http.StatusOK {
			t.Fatalf("%s %d %s", path, code, env.Code)
		}
		for _, item := range listItems(t, env) {
			if item["id"] == host || item["host_id"] == host {
				return true
			}
		}
		return false
	}
	lists := []string{
		"/v1/orgs/" + fx.home.OrgID.String() + "/hosts?limit=200",
		"/v1/projects/" + fx.home.ID.String() + "/hosts",
		"/v1/projects/" + fx.metrics.ID.String() + "/hosts",
		"/v1/projects/" + fx.deploy.ID.String() + "/hosts",
	}
	var services []string
	for _, project := range []string{fx.home.ID.String(), fx.deploy.ID.String()} {
		// Only view=all changes on archive: the deploybot views already drop
		// revoked hosts. They stay here as regression guards.
		for _, view := range []string{"all", "deploybot", "deploybot_and_discovered"} {
			services = append(services, "/v1/projects/"+project+"/services?view="+view)
		}
	}

	revokeFixtureHost(t, fx)
	// A revoked host still lists, and view=all still has its gone rows.
	for _, path := range append(lists, "/v1/projects/"+fx.home.ID.String()+"/services?view=all", "/v1/projects/"+fx.deploy.ID.String()+"/services?view=all") {
		if !listed(path) {
			t.Fatalf("revoked host missing from %s before archive", path)
		}
	}

	code, env := fx.do(actOwnerOwner, http.MethodPost, fx.hostPath("/archive"), "")
	if code != http.StatusOK {
		t.Fatalf("archive %d %s %s", code, env.Code, env.Msg)
	}
	for _, path := range append(lists, services...) {
		if listed(path) {
			t.Fatalf("archived host still in %s", path)
		}
	}

	// Reads by id keep working: the host, its metrics, processes and services.
	for _, actor := range []string{actOwnerOwner, actDeployViewer} {
		code, env = fx.do(actor, http.MethodGet, fx.hostPath(""), "")
		if code != http.StatusOK {
			t.Fatalf("%s get %d %s", actor, code, env.Code)
		}
		payload := objectPayload(t, env)
		if payload["archived_at"] == nil || payload["status"] != "revoked" {
			t.Fatalf("%s get payload archived_at %v status %v", actor, payload["archived_at"], payload["status"])
		}
	}
	for _, suffix := range []string{"/metrics?m=cpu", "/processes", "/services"} {
		code, env = fx.do(actOwnerOwner, http.MethodGet, fx.hostPath(suffix), "")
		if code != http.StatusOK {
			t.Fatalf("%s %d %s %s", suffix, code, env.Code, env.Msg)
		}
	}
	if items := func() []map[string]any {
		_, env := fx.do(actOwnerOwner, http.MethodGet, fx.hostPath("/services"), "")
		return listItems(t, env)
	}(); len(items) == 0 {
		t.Fatal("host services by id empty after archive")
	}
	// host_access rows are kept.
	code, env = fx.do(actOwnerOwner, http.MethodGet, fx.hostPath("/access"), "")
	if code != http.StatusOK {
		t.Fatalf("access %d %s", code, env.Code)
	}
}

// TestHostJSONArchivedAt is api-v1 v1.40 item 1: a live host carries
// archived_at null.
func TestHostJSONArchivedAt(t *testing.T) {
	fx := newAccessFixture(t)
	code, env := fx.do(actOwnerOwner, http.MethodGet, fx.hostPath(""), "")
	if code != http.StatusOK {
		t.Fatalf("get %d", code)
	}
	payload := objectPayload(t, env)
	v, ok := payload["archived_at"]
	if !ok || v != nil {
		t.Fatalf("archived_at %v present %v", v, ok)
	}
}

// TestHostArchiveConcurrentAuditsOnce: parallel archives of one revoked host
// all answer 200 and write exactly one host.archive row, even with the
// fixture's fixed clock (the handler audits on the store's changed flag,
// not on comparing times).
func TestHostArchiveConcurrentAuditsOnce(t *testing.T) {
	fx := newAccessFixture(t)
	revokeFixtureHost(t, fx)
	const n = 8
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, _ := fx.do(actOwnerOwner, http.MethodPost, fx.hostPath("/archive"), "")
			codes <- code
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != http.StatusOK {
			t.Fatalf("archive %d", code)
		}
	}
	if rows := hostArchiveRows(fx.w, audit.DecisionAllowed); len(rows) != 1 {
		t.Fatalf("host.archive rows %d, want 1", len(rows))
	}
}
