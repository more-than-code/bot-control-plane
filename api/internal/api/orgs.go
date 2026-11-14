package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Org route levels (api-v1 v1.24 item 3).
const (
	orgNeedMember = "member"
	orgNeedAdmin  = "admin"
	orgNeedOwner  = "owner"
)

// orgGate loads the caller's org role and checks need. A caller who is not
// an org member gets not_found; one whose role is too low gets forbidden.
// Denied writes are audited with the org.
func (s *Server) orgGate(w http.ResponseWriter, r *http.Request, p principal, need, action string) (uuid.UUID, string, bool) {
	orgID, ok := s.pathID(w, r, "id")
	if !ok {
		return uuid.Nil, "", false
	}
	role, err := s.Store.OrgRole(r.Context(), orgID, p.User.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return uuid.Nil, "", false
	}
	var denied error
	switch {
	case role == "":
		denied = &authz.Denied{Code: "not_found"}
	case need == orgNeedAdmin && !store.OrgAdmin(role):
		denied = &authz.Denied{Code: "forbidden"}
	case need == orgNeedOwner && role != store.OrgRoleOwner:
		denied = &authz.Denied{Code: "forbidden"}
	}
	if denied != nil {
		// A not_found org id may not exist, and the audit foreign key would reject it.
		var org *uuid.UUID
		if role != "" {
			org = idPtr(orgID)
		}
		s.denyRow(w, r, p, org, nil, nil, nil, action, nil, denied)
		return uuid.Nil, "", false
	}
	return orgID, role, true
}

func (s *Server) handleOrgCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, uuid.Nil, authz.CapSession); err != nil {
		s.deny(w, r, p, nil, nil, nil, audit.ActionOrgCreate, err)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	name, ok := cleanName(w, body.Name)
	if !ok {
		return
	}
	org, err := s.Store.CreateOrg(r.Context(), p.User.ID, name, store.OrgSlug(name), s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		OrgID:  idPtr(org.ID),
		Action: audit.ActionOrgCreate,
		Detail: map[string]any{"slug": org.Slug},
	})
	writeOK(w, http.StatusCreated, org)
}

func (s *Server) handleOrgGet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, role, ok := s.orgGate(w, r, p, orgNeedMember, "")
	if !ok {
		return
	}
	org, err := s.Store.GetOrg(r.Context(), orgID)
	if err != nil {
		s.fail(w, err)
		return
	}
	org.Role = role
	writeOK(w, http.StatusOK, org)
}

func (s *Server) handleOrgPatch(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, role, ok := s.orgGate(w, r, p, orgNeedAdmin, audit.ActionOrgRename)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	name, ok := cleanName(w, body.Name)
	if !ok {
		return
	}
	org, err := s.Store.RenameOrg(r.Context(), orgID, name)
	if err != nil {
		s.fail(w, err)
		return
	}
	org.Role = role
	s.auditOK(r.Context(), p, store.AuditRow{OrgID: idPtr(orgID), Action: audit.ActionOrgRename})
	writeOK(w, http.StatusOK, org)
}

// handleOrgDelete soft-deletes the org: every host credential is revoked
// and every project is deleted, as v1.0 project delete did.
func (s *Server) handleOrgDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedOwner, audit.ActionOrgDelete)
	if !ok {
		return
	}
	hosts, err := s.Store.DeleteOrg(r.Context(), orgID, s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	if s.Hub != nil {
		for _, hostID := range hosts {
			s.Hub.CloseHost(hostID)
		}
	}
	s.auditOK(r.Context(), p, store.AuditRow{OrgID: idPtr(orgID), Action: audit.ActionOrgDelete})
	for _, hostID := range hosts {
		s.auditOK(r.Context(), p, store.AuditRow{OrgID: idPtr(orgID), HostID: idPtr(hostID), Action: audit.ActionHostRevoke})
	}
	writeOK(w, http.StatusOK, nil)
}

func (s *Server) handleOrgMemberList(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedMember, "")
	if !ok {
		return
	}
	limit, ok := s.limit(w, r)
	if !ok {
		return
	}
	cur, ok := s.cursor(w, r)
	if !ok {
		return
	}
	items, next, err := s.Store.ListOrgMembers(r.Context(), orgID, limit, cur)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeList(w, items, next)
}

func validOrgMemberRole(role string) bool {
	return role == store.OrgRoleAdmin || role == store.OrgRoleMember
}

func (s *Server) handleOrgMemberAdd(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedAdmin, audit.ActionOrgMemberAdd)
	if !ok {
		return
	}
	var body struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if email == "" || len(email) > 320 || strings.ContainsAny(email, " \t\r\n") {
		writeCode(w, "bad_request", "invalid email")
		return
	}
	if !validOrgMemberRole(body.Role) {
		writeCode(w, "bad_request", "invalid role")
		return
	}
	user, err := s.Store.UserByEmail(r.Context(), email)
	if err != nil {
		s.fail(w, err)
		return
	}
	member, err := s.Store.AddOrgMember(r.Context(), orgID, user.ID, body.Role, p.User.ID, s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		OrgID:  idPtr(orgID),
		Action: audit.ActionOrgMemberAdd,
		Detail: map[string]any{"user_id": user.ID.String(), "role": body.Role},
	})
	writeOK(w, http.StatusCreated, member)
}

func (s *Server) handleOrgMemberPatch(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedAdmin, audit.ActionOrgMemberUpdate)
	if !ok {
		return
	}
	userID, ok := s.pathID(w, r, "user_id")
	if !ok {
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !validOrgMemberRole(body.Role) {
		writeCode(w, "bad_request", "invalid role")
		return
	}
	member, err := s.Store.UpdateOrgMemberRole(r.Context(), orgID, userID, body.Role)
	if err != nil {
		s.orgOwnerErr(w, r, p, orgID, audit.ActionOrgMemberUpdate, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		OrgID:  idPtr(orgID),
		Action: audit.ActionOrgMemberUpdate,
		Detail: map[string]any{"user_id": userID.String(), "role": body.Role},
	})
	writeOK(w, http.StatusOK, member)
}

// handleOrgMemberDelete: admin+, or any member removing themself. The
// owner cannot be removed.
func (s *Server) handleOrgMemberDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	userID, err := uuid.Parse(r.PathValue("user_id"))
	if err != nil {
		writeCode(w, "bad_request", "invalid id")
		return
	}
	need := orgNeedAdmin
	if userID == p.User.ID {
		need = orgNeedMember
	}
	orgID, _, ok := s.orgGate(w, r, p, need, audit.ActionOrgMemberRemove)
	if !ok {
		return
	}
	if err := s.Store.RemoveOrgMember(r.Context(), orgID, userID); err != nil {
		s.orgOwnerErr(w, r, p, orgID, audit.ActionOrgMemberRemove, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		OrgID:  idPtr(orgID),
		Action: audit.ActionOrgMemberRemove,
		Detail: map[string]any{"user_id": userID.String()},
	})
	writeOK(w, http.StatusOK, nil)
}

// orgOwnerErr maps ErrOwner (the owner's role cannot change, and the owner
// cannot be removed) to an audited forbidden.
func (s *Server) orgOwnerErr(w http.ResponseWriter, r *http.Request, p principal, orgID uuid.UUID, action string, err error) {
	if errors.Is(err, store.ErrOwner) {
		s.denyRow(w, r, p, idPtr(orgID), nil, nil, nil, action, nil, &authz.Denied{Code: "forbidden"})
		return
	}
	s.fail(w, err)
}

func (s *Server) handleOrgTransfer(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedOwner, audit.ActionOrgTransfer)
	if !ok {
		return
	}
	var body struct {
		UserID string `json:"user_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	userID, err := uuid.Parse(body.UserID)
	if err != nil {
		writeCode(w, "bad_request", "invalid id")
		return
	}
	if err := s.Store.TransferOrgOwnership(r.Context(), orgID, userID); err != nil {
		s.fail(w, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		OrgID:  idPtr(orgID),
		Action: audit.ActionOrgTransfer,
		Detail: map[string]any{"user_id": userID.String()},
	})
	writeOK(w, http.StatusOK, nil)
}

// handleOrgProjects lists the org's projects the caller can reach.
func (s *Server) handleOrgProjects(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedMember, "")
	if !ok {
		return
	}
	items, err := s.Store.ListOrgProjects(r.Context(), orgID, p.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeOK(w, http.StatusOK, map[string]any{"items": nonNilSlice(items)})
}

func (s *Server) handleOrgProjectCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedAdmin, audit.ActionProjectCreate)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	name, ok := cleanName(w, body.Name)
	if !ok {
		return
	}
	s.createProject(w, r, p, orgID, name)
}

// handleOrgHosts is every host of the org with its access rows (admin+).
func (s *Server) handleOrgHosts(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedAdmin, "")
	if !ok {
		return
	}
	limit, ok := s.limit(w, r)
	if !ok {
		return
	}
	cur, ok := s.hostCursor(w, r)
	if !ok {
		return
	}
	items, next, err := s.Store.ListOrgHosts(r.Context(), orgID, limit, cur)
	if err != nil {
		s.fail(w, err)
		return
	}
	for i := range items {
		items[i] = withAccessLevel(items[i], "")
		items[i].Status = s.hostStatus(items[i])
		items[i] = projectsView(items[i], uuid.Nil, "", true)
	}
	writeList(w, items, next)
}

func (s *Server) handleOrgJoinCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedAdmin, audit.ActionOrgJoinCreate)
	if !ok {
		return
	}
	var body joinTokenBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Grant != nil && (body.Grant.ProjectID == uuid.Nil || !store.ValidAccessLevel(body.Grant.Level)) {
		writeCode(w, "bad_request", "invalid grant")
		return
	}
	tok, plain, ok := s.newJoinToken(w, r, p, orgID, body)
	if !ok {
		return
	}
	row := store.AuditRow{
		OrgID:  idPtr(orgID),
		Action: audit.ActionOrgJoinCreate,
		Detail: map[string]any{"token_id": tok.ID.String()},
	}
	if tok.Grant != nil {
		row.ProjectID = idPtr(tok.Grant.ProjectID)
		row.Detail["level"] = tok.Grant.Level
	}
	s.auditOK(r.Context(), p, row)
	s.writeJoinToken(w, tok, plain)
}

func (s *Server) handleOrgJoinList(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedAdmin, "")
	if !ok {
		return
	}
	limit, ok := s.limit(w, r)
	if !ok {
		return
	}
	cur, ok := s.cursor(w, r)
	if !ok {
		return
	}
	items, next, err := s.Store.ListJoinTokens(r.Context(), orgID, nil, limit, cur)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeList(w, items, next)
}

func (s *Server) handleOrgJoinRevoke(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedAdmin, audit.ActionOrgJoinRevoke)
	if !ok {
		return
	}
	tokenID, ok := s.pathID(w, r, "token_id")
	if !ok {
		return
	}
	if err := s.Store.RevokeJoinToken(r.Context(), orgID, nil, tokenID, s.now()); err != nil {
		s.fail(w, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		OrgID:  idPtr(orgID),
		Action: audit.ActionOrgJoinRevoke,
		Detail: map[string]any{"token_id": tokenID.String()},
	})
	writeOK(w, http.StatusOK, nil)
}

func (s *Server) handleOrgAudit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	orgID, _, ok := s.orgGate(w, r, p, orgNeedAdmin, "")
	if !ok {
		return
	}
	limit, ok := s.limit(w, r)
	if !ok {
		return
	}
	cur, ok := s.cursor(w, r)
	if !ok {
		return
	}
	items, next, err := s.Store.ListOrgAudit(r.Context(), orgID, limit, cur)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeList(w, items, next)
}
