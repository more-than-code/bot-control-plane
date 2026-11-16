package api

import (
	"context"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

func (s *Server) handleProjectCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, uuid.Nil, authz.CapSession); err != nil {
		s.deny(w, r, p, nil, nil, nil, audit.ActionProjectCreate, err)
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
	// api-v1 v1.24 item 3: kept for one release; the project goes into the
	// caller's only org where they are owner or admin.
	orgs, err := s.Store.ListUserOrgs(r.Context(), p.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	orgID := uuid.Nil
	for _, o := range orgs {
		if !store.OrgAdmin(o.Role) {
			continue
		}
		if orgID != uuid.Nil {
			writeCode(w, "bad_request", "choose an organization")
			return
		}
		orgID = o.ID
	}
	if orgID == uuid.Nil {
		writeCode(w, "bad_request", "choose an organization")
		return
	}
	s.createProject(w, r, p, orgID, name)
}

// createProject creates the project in orgID with the caller as owner.
func (s *Server) createProject(w http.ResponseWriter, r *http.Request, p principal, orgID uuid.UUID, name string) {
	if store.Slug(name) == reservedProjectSlug {
		writeCode(w, "bad_request", msgReservedProject)
		return
	}
	project, err := s.Store.CreateProject(r.Context(), orgID, p.User.ID, name, store.Slug(name), s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		OrgID:     idPtr(orgID),
		ProjectID: idPtr(project.ID),
		Action:    audit.ActionProjectCreate,
		Detail:    map[string]any{"slug": project.Slug},
	})
	writeOK(w, http.StatusCreated, project)
}

// callerRole is the caller's effective project role for catalog grants and
// actions (api-v1 v1.24 item 2, D-1): their membership role, raised to admin
// when they own or administer the project's org. ErrNotFound when the
// project does not reach them.
func (s *Server) callerRole(ctx context.Context, projectID, userID uuid.UUID) (string, error) {
	pa, err := s.Store.ProjectAccess(ctx, projectID, userID)
	if err != nil {
		return "", err
	}
	role := pa.MemberRole
	if store.OrgAdmin(pa.OrgRole) && role != authz.RoleOwner {
		role = authz.RoleAdmin
	}
	if role == "" {
		return "", store.ErrNotFound
	}
	return role, nil
}

// withCallerRole sets role and via for the caller (api-v1 v1.24 item 1): a
// membership wins; an org owner or admin without one is admin via org.
func (s *Server) withCallerRole(r *http.Request, project store.Project, userID uuid.UUID) (store.Project, error) {
	pa, err := s.Store.ProjectAccess(r.Context(), project.ID, userID)
	if err != nil {
		return project, err
	}
	switch {
	case pa.MemberRole != "":
		project.Role, project.Via = pa.MemberRole, "member"
	case store.OrgAdmin(pa.OrgRole):
		project.Role, project.Via = authz.RoleAdmin, "org"
	}
	return project, nil
}

func (s *Server) handleProjectList(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, uuid.Nil, authz.CapSession); err != nil {
		s.deny(w, r, p, nil, nil, nil, "", err)
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
	items, next, err := s.Store.ListProjects(r.Context(), p.User.ID, limit, cur)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeList(w, items, next)
}

func (s *Server) handleProjectGet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "id")
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapRead); err != nil {
		s.deny(w, r, p, idPtr(id), nil, nil, "", err)
		return
	}
	project, err := s.Store.GetProject(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	project, err = s.withCallerRole(r, project, p.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeOK(w, http.StatusOK, project)
}

func (s *Server) handleProjectPatch(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "id")
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapProjectWrite); err != nil {
		s.deny(w, r, p, idPtr(id), nil, nil, audit.ActionProjectRename, err)
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
	project, err := s.Store.RenameProject(r.Context(), id, name)
	if err != nil {
		s.fail(w, err)
		return
	}
	project, err = s.withCallerRole(r, project, p.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{OrgID: idPtr(project.OrgID), ProjectID: idPtr(id), Action: audit.ActionProjectRename})
	writeOK(w, http.StatusOK, project)
}

func (s *Server) handleProjectDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "id")
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapProjectWrite); err != nil {
		s.deny(w, r, p, idPtr(id), nil, nil, audit.ActionProjectDelete, err)
		return
	}
	project, err := s.Store.GetProject(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	// api-v1 v1.24 item 7: hosts stay in the org; nothing is revoked or stopped.
	if err := s.Store.DeleteProject(r.Context(), id, s.now()); err != nil {
		s.fail(w, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{OrgID: idPtr(project.OrgID), ProjectID: idPtr(id), Action: audit.ActionProjectDelete})
	// api-v1 v1.35: the project's routes stop on the org's edges.
	s.kickOrgEdges(r.Context(), project.OrgID)
	writeOK(w, http.StatusOK, nil)
}

func (s *Server) handleMemberList(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "id")
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapRead); err != nil {
		s.deny(w, r, p, idPtr(id), nil, nil, "", err)
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
	items, next, err := s.Store.ListMembers(r.Context(), id, limit, cur)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeList(w, items, next)
}

func (s *Server) handleMemberAdd(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "id")
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.deny(w, r, p, idPtr(id), nil, nil, audit.ActionMemberAdd, err)
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
	if body.Role != authz.RoleAdmin && body.Role != authz.RoleViewer {
		writeCode(w, "bad_request", "invalid role")
		return
	}
	user, err := s.Store.UserByEmail(r.Context(), email)
	if err != nil {
		s.fail(w, err)
		return
	}
	member, err := s.Store.AddMember(r.Context(), id, user.ID, body.Role, p.User.ID, s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionMemberAdd,
		Detail:    map[string]any{"user_id": user.ID.String(), "role": body.Role},
	})
	writeOK(w, http.StatusCreated, member)
}

func (s *Server) handleMemberPatch(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "id")
	if !ok {
		return
	}
	userID, ok := s.pathID(w, r, "user_id")
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.deny(w, r, p, idPtr(id), nil, nil, audit.ActionMemberUpdate, err)
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Role != authz.RoleAdmin && body.Role != authz.RoleViewer {
		writeCode(w, "bad_request", "invalid role")
		return
	}
	member, err := s.Store.UpdateMemberRole(r.Context(), id, userID, body.Role)
	if err != nil {
		s.ownerErr(w, r, p, idPtr(id), audit.ActionMemberUpdate, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionMemberUpdate,
		Detail:    map[string]any{"user_id": userID.String(), "role": body.Role},
	})
	writeOK(w, http.StatusOK, member)
}

func (s *Server) handleMemberDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "id")
	if !ok {
		return
	}
	userID, ok := s.pathID(w, r, "user_id")
	if !ok {
		return
	}
	err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers)
	if err != nil && userID == p.User.ID {
		err = authz.Authorize(r.Context(), p.User.ID, id, authz.CapRead)
	}
	if err != nil {
		s.deny(w, r, p, idPtr(id), nil, nil, audit.ActionMemberRemove, err)
		return
	}
	if err := s.Store.RemoveMember(r.Context(), id, userID); err != nil {
		s.ownerErr(w, r, p, idPtr(id), audit.ActionMemberRemove, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionMemberRemove,
		Detail:    map[string]any{"user_id": userID.String()},
	})
	writeOK(w, http.StatusOK, nil)
}

func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "id")
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapProjectWrite); err != nil {
		s.deny(w, r, p, idPtr(id), nil, nil, audit.ActionProjectTransfer, err)
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
	if err := s.Store.TransferOwnership(r.Context(), id, userID); err != nil {
		s.fail(w, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionProjectTransfer,
		Detail:    map[string]any{"user_id": userID.String()},
	})
	writeOK(w, http.StatusOK, nil)
}

func cleanName(w http.ResponseWriter, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if !store.ValidName(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		writeCode(w, "bad_request", "invalid name")
		return "", false
	}
	if utf8.RuneCountInString(name) < 1 {
		writeCode(w, "bad_request", "invalid name")
		return "", false
	}
	return name, true
}
