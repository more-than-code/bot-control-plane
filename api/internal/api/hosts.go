package api

import (
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// projectOrgAdmin is the v1.24 rule for the project-scoped join-token routes
// (item 3): the caller must be an org owner or admin of the project's org.
// A caller the project does not reach gets not_found; one who reaches it
// but is not an org admin gets forbidden.
func (s *Server) projectOrgAdmin(w http.ResponseWriter, r *http.Request, p principal, projectID uuid.UUID, action string) (uuid.UUID, bool) {
	pa, err := s.Store.ProjectAccess(r.Context(), projectID, p.User.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return uuid.Nil, false
	}
	facts := authz.ProjectFacts{Found: err == nil, MemberRole: pa.MemberRole, OrgRole: pa.OrgRole}
	var denied error
	switch {
	case !authz.Reaches(facts):
		denied = &authz.Denied{Code: "not_found"}
	case !store.OrgAdmin(pa.OrgRole):
		denied = &authz.Denied{Code: "forbidden"}
	}
	if denied != nil {
		s.deny(w, r, p, idPtr(projectID), nil, nil, action, denied)
		return uuid.Nil, false
	}
	return pa.OrgID, true
}

type joinTokenBody struct {
	Hint    *string           `json:"host_name_hint"`
	Expires *int              `json:"expires_in_sec"`
	Grant   *store.TokenGrant `json:"grant"`
}

// newJoinToken validates the body fields shared by the org and project
// routes and creates the token. The grant is checked by the caller.
func (s *Server) newJoinToken(w http.ResponseWriter, r *http.Request, p principal, orgID uuid.UUID, body joinTokenBody) (store.JoinToken, string, bool) {
	var hint *string
	if body.Hint != nil && *body.Hint != "" {
		if !store.ValidHostName(*body.Hint) {
			writeCode(w, "bad_request", "invalid host_name_hint")
			return store.JoinToken{}, "", false
		}
		hint = body.Hint
	}
	sec := 86400
	if body.Expires != nil {
		sec = *body.Expires
	}
	if sec < 300 || sec > 604800 {
		writeCode(w, "bad_request", "invalid expires_in_sec")
		return store.JoinToken{}, "", false
	}
	plain, hash, err := secret.NewToken()
	if err != nil {
		s.fail(w, err)
		return store.JoinToken{}, "", false
	}
	now := s.now()
	tok, err := s.Store.CreateJoinToken(r.Context(), orgID, hash, hint, body.Grant, p.User.ID, now.Add(time.Duration(sec)*time.Second), now)
	if err != nil {
		s.fail(w, err)
		return store.JoinToken{}, "", false
	}
	return tok, plain, true
}

func (s *Server) writeJoinToken(w http.ResponseWriter, tok store.JoinToken, plain string) {
	writeOK(w, http.StatusCreated, struct {
		store.JoinToken
		Token          string `json:"token"`
		InstallCommand string `json:"install_command"`
	}{
		JoinToken:      tok,
		Token:          plain,
		InstallCommand: installCommand(s.AgentWSURL, plain),
	})
}

// handleJoinCreate is the v1.0 project route kept for one release (v1.24
// item 3): org admin+ of the project's org; it creates an org token that
// grants this project deploy.
func (s *Server) handleJoinCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "id")
	if !ok {
		return
	}
	orgID, ok := s.projectOrgAdmin(w, r, p, id, audit.ActionJoinCreate)
	if !ok {
		return
	}
	var body joinTokenBody
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Grant = &store.TokenGrant{ProjectID: id, Level: authz.LevelDeploy}
	tok, plain, ok := s.newJoinToken(w, r, p, orgID, body)
	if !ok {
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		OrgID:     idPtr(orgID),
		ProjectID: idPtr(id),
		Action:    audit.ActionJoinCreate,
		Detail:    map[string]any{"token_id": tok.ID.String()},
	})
	s.writeJoinToken(w, tok, plain)
}

// installCommand is the v1.0.2 shell line. The token is written to stdin, not argv.
func installCommand(wsURL, token string) string {
	return "printf '%s' '" + token + "' | sudo -u bot-agent bot-agent init --server '" + wsURL + "' --join-token-stdin"
}

func (s *Server) handleJoinList(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "id")
	if !ok {
		return
	}
	orgID, ok := s.projectOrgAdmin(w, r, p, id, "")
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
	items, next, err := s.Store.ListJoinTokens(r.Context(), orgID, &id, limit, cur)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeList(w, items, next)
}

func (s *Server) handleJoinRevoke(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "id")
	if !ok {
		return
	}
	tokenID, ok := s.pathID(w, r, "token_id")
	if !ok {
		return
	}
	orgID, ok := s.projectOrgAdmin(w, r, p, id, audit.ActionJoinRevoke)
	if !ok {
		return
	}
	if err := s.Store.RevokeJoinToken(r.Context(), orgID, &id, tokenID, s.now()); err != nil {
		s.fail(w, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		OrgID:     idPtr(orgID),
		ProjectID: idPtr(id),
		Action:    audit.ActionJoinRevoke,
		Detail:    map[string]any{"token_id": tokenID.String()},
	})
	writeOK(w, http.StatusOK, nil)
}

func (s *Server) handleHostList(w http.ResponseWriter, r *http.Request) {
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
	cur, ok := s.hostCursor(w, r)
	if !ok {
		return
	}
	items, next, err := s.Store.ListHosts(r.Context(), id, limit, cur)
	if err != nil {
		s.fail(w, err)
		return
	}
	viewer, err := s.Store.GetProject(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	orgAdmin, err := s.isOrgAdmin(r, viewer.OrgID, p.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	orgAdmin = orgAdmin && p.Session.Kind != store.KindDelegated
	for i := range items {
		items[i].Status = s.hostStatus(items[i])
		items[i] = projectsView(items[i], id, viewer.Slug, orgAdmin)
		if items[i], err = s.withEdge(r.Context(), items[i]); err != nil {
			s.fail(w, err)
			return
		}
	}
	writeList(w, s.hostViews(items), next)
}

// isOrgAdmin reports whether the user owns or administers the org.
func (s *Server) isOrgAdmin(r *http.Request, orgID, userID uuid.UUID) (bool, error) {
	role, err := s.Store.OrgRole(r.Context(), orgID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return store.OrgAdmin(role), nil
}

func (s *Server) handleHostGet(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.fetchHost(w, r)
	if !ok {
		return
	}
	rel, ok := s.gateHost(w, r, p, host, authz.CapHostView, nil, nil, "")
	if !ok {
		return
	}
	view, err := s.hostRead(r, p, rel, host)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeOK(w, http.StatusOK, view)
}

// hostRead is the Host object GET /v1/hosts/{id} answers, for a caller
// already past the host gate with relation rel.
func (s *Server) hostRead(r *http.Request, p principal, rel authz.Relation, host store.Host) (hostView, error) {
	host = withAccessLevel(host, authz.HighestLevel(rel.Access))
	host.Status = s.hostStatus(host)
	// A delegated (MCP) session never gets access or the allowlist (review A5).
	orgView := rel.OrgAdmin() && p.Session.Kind != store.KindDelegated
	if orgView {
		access, err := s.Store.ListHostAccess(r.Context(), host.ID)
		if err != nil {
			return hostView{}, err
		}
		host.Access = &access
	}
	viewer, slug, err := s.hostViewer(r, rel)
	if err != nil {
		return hostView{}, err
	}
	host, err = s.withEdge(r.Context(), projectsView(host, viewer, slug, orgView))
	if err != nil {
		return hostView{}, err
	}
	return s.withUpdateHint(host), nil
}

// handleHostPatch renames a host (api-v1 v1.38 item 2): org owners and
// admins, live hosts only, names unique among the org's live hosts. The
// same name answers 200 with no audit row.
func (s *Server) handleHostPatch(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.fetchHost(w, r)
	if !ok {
		return
	}
	rel, ok := s.gateHost(w, r, p, host, authz.CapHostAdmin, nil, nil, audit.ActionHostRename)
	if !ok {
		return
	}
	var body struct {
		Name *string `json:"name"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Name == nil || !store.ValidHostName(*body.Name) {
		writeCode(w, "bad_request", "name must be 1–63 characters: lowercase letters, digits and -")
		return
	}
	name := *body.Name
	from, err := s.Store.RenameHost(r.Context(), host.ID, name)
	switch {
	case errors.Is(err, store.ErrState):
		writeCode(w, "conflict", "this host is revoked")
		return
	case errors.Is(err, store.ErrConflict):
		writeCode(w, "conflict", "a host named "+name+" already exists in this organization")
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	if from != name {
		row := hostAuditRow(host, nil, audit.ActionHostRename)
		row.Detail = map[string]any{"from": from, "to": name}
		s.auditOK(r.Context(), p, row)
	}
	host, err = s.Store.GetHost(r.Context(), host.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	view, err := s.hostRead(r, p, rel, host)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeOK(w, http.StatusOK, view)
}

// hostViewer is the project whose view GET /v1/hosts/{id} shows: the
// caller's one project with access. A caller in several projects with
// access, or in none, gets uuid.Nil, so project_listed stays null.
func (s *Server) hostViewer(r *http.Request, rel authz.Relation) (uuid.UUID, string, error) {
	viewer := uuid.Nil
	for _, a := range rel.Access {
		if viewer != uuid.Nil && a.ProjectID != viewer {
			return uuid.Nil, "", nil
		}
		viewer = a.ProjectID
	}
	if viewer == uuid.Nil {
		return uuid.Nil, "", nil
	}
	project, err := s.Store.GetProject(r.Context(), viewer)
	if err != nil {
		return uuid.Nil, "", err
	}
	return viewer, project.Slug, nil
}

// projectsView applies api-v1 v1.23 item 1 as amended by v1.24 item 3:
// the projects allowlist only for org admins, and project_listed for the
// viewer's slug (null when the list is unknown or the viewer is unknown).
// Programs is copied, never changed in place.
func projectsView(h store.Host, viewer uuid.UUID, slug string, orgAdmin bool) store.Host {
	if h.Programs == nil {
		return h
	}
	cp := *h.Programs
	cp.ProjectListed = nil
	if cp.Projects != nil && viewer != uuid.Nil {
		listed := slices.Contains(cp.Projects, slug)
		cp.ProjectListed = &listed
	}
	if !orgAdmin {
		cp.Projects = nil
	} else if cp.Projects != nil {
		cp.Projects = append([]string{}, cp.Projects...)
	}
	cp.SharedNetworks = sharedNetworksView(cp.SharedNetworks, viewer, slug, orgAdmin)
	h.Programs = &cp
	return h
}

// sharedNetworksView applies api-v1 v1.25 item 2: an org admin (session,
// not delegated) sees every network with its members; anyone else sees
// only the networks that list the project in view, without members. With
// no project in view that is an empty list. null (an agent before 0.2.9)
// stays null.
func sharedNetworksView(nets []store.SharedNetwork, viewer uuid.UUID, slug string, orgAdmin bool) []store.SharedNetwork {
	if nets == nil {
		return nil
	}
	if orgAdmin {
		return store.CloneSharedNetworks(nets)
	}
	out := []store.SharedNetwork{}
	if viewer == uuid.Nil {
		return out
	}
	for _, n := range nets {
		if slices.Contains(n.Projects, slug) {
			out = append(out, store.SharedNetwork{Name: n.Name, Subnet: n.Subnet})
		}
	}
	return out
}

func (s *Server) handleHostRevoke(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.fetchHost(w, r)
	if !ok {
		return
	}
	if _, ok := s.gateHost(w, r, p, host, authz.CapHostAdmin, nil, nil, audit.ActionHostRevoke); !ok {
		return
	}
	if err := s.Store.RevokeHost(r.Context(), host.ID, s.now()); err != nil {
		s.fail(w, err)
		return
	}
	if s.Hub != nil {
		s.Hub.CloseHost(host.ID)
	}
	s.auditOK(r.Context(), p, hostAuditRow(host, nil, audit.ActionHostRevoke))
	writeOK(w, http.StatusOK, nil)
}

// handleHostArchive removes a revoked host from the lists (api-v1 v1.40):
// org owners and admins, revoked hosts only. An already archived host
// answers 200 with no audit row.
func (s *Server) handleHostArchive(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.fetchHost(w, r)
	if !ok {
		return
	}
	rel, ok := s.gateHost(w, r, p, host, authz.CapHostAdmin, nil, nil, audit.ActionHostArchive)
	if !ok {
		return
	}
	_, changed, err := s.Store.ArchiveHost(r.Context(), host.ID, s.now())
	switch {
	case errors.Is(err, store.ErrState):
		writeCode(w, "conflict", "revoke this host first")
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	// Only the call that wrote archived_at is audited.
	if changed {
		s.auditOK(r.Context(), p, hostAuditRow(host, nil, audit.ActionHostArchive))
	}
	host, err = s.Store.GetHost(r.Context(), host.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	view, err := s.hostRead(r, p, rel, host)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeOK(w, http.StatusOK, view)
}

func (s *Server) fetchHost(w http.ResponseWriter, r *http.Request) (principal, store.Host, bool) {
	p, ok := s.require(w, r)
	if !ok {
		return principal{}, store.Host{}, false
	}
	id, ok := s.pathID(w, r, "id")
	if !ok {
		return principal{}, store.Host{}, false
	}
	host, err := s.Store.GetHost(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return principal{}, store.Host{}, false
	}
	return p, host, true
}
