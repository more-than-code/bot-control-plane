package api

import (
	"errors"
	"net/http"

	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// handleAccessList is GET /v1/hosts/{id}/access (api-v1 v1.24 item 3): org admin+.
func (s *Server) handleAccessList(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.fetchHost(w, r)
	if !ok {
		return
	}
	if _, ok := s.gateHost(w, r, p, host, authz.CapHostAdmin, nil, nil, ""); !ok {
		return
	}
	items, err := s.Store.ListHostAccess(r.Context(), host.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if items == nil {
		items = []store.HostAccess{}
	}
	writeOK(w, http.StatusOK, map[string]any{"items": items})
}

// handleAccessPut is PUT /v1/hosts/{id}/access/{project_id}: an org admin+
// upsert. A project outside the host's org is not_found.
func (s *Server) handleAccessPut(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.fetchHost(w, r)
	if !ok {
		return
	}
	target, ok := s.pathID(w, r, "project_id")
	if !ok {
		return
	}
	if _, ok := s.gateHost(w, r, p, host, authz.CapHostAdmin, nil, nil, audit.ActionHostAccessGrant); !ok {
		return
	}
	var body struct {
		Level *string `json:"level"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Level == nil || !store.ValidAccessLevel(*body.Level) {
		writeCode(w, "bad_request", "invalid level")
		return
	}
	row, created, err := s.Store.PutHostAccess(r.Context(), host.ID, target, *body.Level, p.User.ID, s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	action := audit.ActionHostAccessGrant
	if !created {
		action = audit.ActionHostAccessUpdate
	}
	audited := hostAuditRow(host, idPtr(row.ProjectID), action)
	audited.Detail = map[string]any{
		"project_id":   row.ProjectID.String(),
		"project_name": row.ProjectName,
		"level":        row.Level,
	}
	s.auditOK(r.Context(), p, audited)
	writeOK(w, http.StatusOK, row)
}

// handleAccessDelete is DELETE /v1/hosts/{id}/access/{project_id}: org
// admin+, or an admin+ of that project leaving. conflict while the project
// has services on the host, unless ?reassign=org.
func (s *Server) handleAccessDelete(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.fetchHost(w, r)
	if !ok {
		return
	}
	target, ok := s.pathID(w, r, "project_id")
	if !ok {
		return
	}
	rel, err := authz.HostRelation(r.Context(), p.User.ID, host.ID)
	if err != nil {
		var denied *authz.Denied
		if errors.As(err, &denied) {
			s.denyHost(w, r, p, host, idPtr(target), nil, audit.ActionHostAccessRevoke, err)
			return
		}
		s.fail(w, err)
		return
	}
	projectRole := ""
	for _, a := range rel.Access {
		if a.ProjectID == target {
			projectRole = a.Role
		}
	}
	if err := authz.AuthorizeAccessRemove(rel, projectRole); err != nil {
		s.denyHost(w, r, p, host, idPtr(target), nil, audit.ActionHostAccessRevoke, err)
		return
	}
	reassign, ok := reassignOrg(w, r)
	if !ok {
		return
	}
	removed, err := s.Store.DeleteHostAccess(r.Context(), host.ID, target, reassign)
	if err != nil {
		s.fail(w, err)
		return
	}
	audited := hostAuditRow(host, idPtr(removed.ProjectID), audit.ActionHostAccessRevoke)
	audited.Detail = map[string]any{
		"project_id":   removed.ProjectID.String(),
		"project_name": removed.ProjectName,
		"level":        removed.Level,
	}
	if reassign {
		audited.Detail["reassign"] = "org"
	}
	s.auditOK(r.Context(), p, audited)
	writeOK(w, http.StatusOK, nil)
}

func reassignOrg(w http.ResponseWriter, r *http.Request) (bool, bool) {
	switch r.URL.Query().Get("reassign") {
	case "":
		return false, true
	case "org":
		return true, true
	default:
		writeCode(w, "bad_request", "invalid reassign")
		return false, false
	}
}

// handleSharesGone answers the removed /v1/hosts/{id}/shares* routes (api-v1 v1.24 item 3).
func (s *Server) handleSharesGone(w http.ResponseWriter, _ *http.Request) {
	writeCode(w, "gone", "replaced by /access")
}

// handleApprovalsGone answers the removed catalog-approval routes (api-v1 v1.24 item 4).
func (s *Server) handleApprovalsGone(w http.ResponseWriter, _ *http.Request) {
	writeCode(w, "gone", "host approvals were removed; a deploy access grant is the approval")
}
