package api

import (
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-api-server/internal/audit"
	"github.com/more-than-code/bot-api-server/internal/authz"
	"github.com/more-than-code/bot-api-server/internal/secret"
	"github.com/more-than-code/bot-api-server/internal/store"
)

var hostHint = regexp.MustCompile(`^[a-z0-9-]{1,63}$`)

func (s *Server) handleJoinCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	id, ok := s.pathID(w, r, "id")
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapJoinToken); err != nil {
		s.deny(w, r, p, idPtr(id), nil, nil, audit.ActionJoinCreate, err)
		return
	}
	var body struct {
		Hint    *string `json:"host_name_hint"`
		Expires *int    `json:"expires_in_sec"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	var hint *string
	if body.Hint != nil && *body.Hint != "" {
		if !hostHint.MatchString(*body.Hint) {
			writeCode(w, "bad_request", "invalid host_name_hint")
			return
		}
		hint = body.Hint
	}
	sec := 86400
	if body.Expires != nil {
		sec = *body.Expires
	}
	if sec < 300 || sec > 604800 {
		writeCode(w, "bad_request", "invalid expires_in_sec")
		return
	}
	plain, hash, err := secret.NewToken()
	if err != nil {
		s.fail(w, err)
		return
	}
	now := s.now()
	tok, err := s.Store.CreateJoinToken(r.Context(), id, hash, hint, p.User.ID, now.Add(time.Duration(sec)*time.Second), now)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionJoinCreate,
		Detail:    map[string]any{"token_id": tok.ID.String()},
	})
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
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapJoinToken); err != nil {
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
	items, next, err := s.Store.ListJoinTokens(r.Context(), id, limit, cur)
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
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapJoinToken); err != nil {
		s.deny(w, r, p, idPtr(id), nil, nil, audit.ActionJoinRevoke, err)
		return
	}
	if err := s.Store.RevokeJoinToken(r.Context(), id, tokenID, s.now()); err != nil {
		s.fail(w, err)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
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
	for i := range items {
		items[i].Status = s.hostStatus(items[i])
		items[i] = projectsView(items[i], id, viewer.Slug)
	}
	writeList(w, items, next)
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
	host = withRelation(host, rel)
	host.Status = s.hostStatus(host)
	viewer, slug, err := s.hostViewer(r, host, rel)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeOK(w, http.StatusOK, projectsView(host, viewer, slug))
}

// hostViewer is the project whose view GET /v1/hosts/{id} shows: the owning
// project for its members, or the caller's one sharing project. A caller in
// several sharing projects gets uuid.Nil, so project_listed stays null.
func (s *Server) hostViewer(r *http.Request, host store.Host, rel authz.Relation) (uuid.UUID, string, error) {
	viewer := uuid.Nil
	switch rel.Kind {
	case authz.RelOwner:
		viewer = host.ProjectID
	case authz.RelShare:
		for _, sh := range rel.Shares {
			if viewer != uuid.Nil && sh.ProjectID != viewer {
				return uuid.Nil, "", nil
			}
			viewer = sh.ProjectID
		}
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

// projectsView applies api-v1 v1.23 item 1 for the viewing project: the
// projects allowlist only for the owning project, and project_listed for the
// viewer's slug (null when the list is unknown or the viewer is unknown).
// Programs is copied, never changed in place.
func projectsView(h store.Host, viewer uuid.UUID, slug string) store.Host {
	if h.Programs == nil {
		return h
	}
	cp := *h.Programs
	cp.ProjectListed = nil
	if cp.Projects != nil && viewer != uuid.Nil {
		listed := slices.Contains(cp.Projects, slug)
		cp.ProjectListed = &listed
	}
	if viewer == uuid.Nil || viewer != h.ProjectID {
		cp.Projects = nil
	} else if cp.Projects != nil {
		cp.Projects = append([]string{}, cp.Projects...)
	}
	h.Programs = &cp
	return h
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
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(host.ProjectID),
		HostID:    idPtr(host.ID),
		Action:    audit.ActionHostRevoke,
	})
	writeOK(w, http.StatusOK, nil)
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
