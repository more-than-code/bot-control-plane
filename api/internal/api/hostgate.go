package api

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// gateHost loads the caller's host relation and authorizes capability.
// serviceProject is the service's project for service-scoped capabilities
// (nil for an org-level service).
func (s *Server) gateHost(w http.ResponseWriter, r *http.Request, p principal, host store.Host, capability string, serviceProject *uuid.UUID, serviceID *uuid.UUID, action string) (authz.Relation, bool) {
	rel, err := authz.HostRelation(r.Context(), p.User.ID, host.ID)
	if err != nil {
		var denied *authz.Denied
		if errors.As(err, &denied) {
			s.denyHost(w, r, p, host, serviceProject, serviceID, action, err)
			return authz.Relation{}, false
		}
		if errors.Is(err, store.ErrNotFound) {
			writeCode(w, "not_found", "not found")
			return authz.Relation{}, false
		}
		s.fail(w, err)
		return authz.Relation{}, false
	}
	if err := authz.AuthorizeHost(rel, capability, serviceProject); err != nil {
		s.denyHost(w, r, p, host, serviceProject, serviceID, action, err)
		return authz.Relation{}, false
	}
	return rel, true
}

// withAccessLevel sets access_level and its deprecated v1.24 mirrors
// (relation, share_level) for one level, or clears them for "".
func withAccessLevel(h store.Host, level string) store.Host {
	h.AccessLevel, h.ShareLevel, h.Relation = nil, nil, ""
	if level == "" {
		return h
	}
	l := level
	h.AccessLevel = &l
	m := level
	h.ShareLevel = &m
	h.Relation = "shared"
	if level == authz.LevelDeploy {
		h.Relation = "owner"
	}
	return h
}

// hostAuditRow is an audit row for a host event: its org, and projectID when one applies.
func hostAuditRow(host store.Host, projectID *uuid.UUID, action string) store.AuditRow {
	return store.AuditRow{OrgID: idPtr(host.OrgID), ProjectID: projectID, HostID: idPtr(host.ID), Action: action}
}
