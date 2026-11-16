package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Connect requests (api-v1 v1.31): an agent asks the person to Connect
// GitHub, and the person who asked approves it on the console page
// /connect/<id>. No route here returns a token, and no audit row or log
// line holds one, the request id or the reason.

// connectRequestView is the ConnectRequest object (item 1).
type connectRequestView struct {
	RequestID string    `json:"request_id"`
	ProjectID uuid.UUID `json:"project_id"`
	Registry  string    `json:"registry"`
	State     string    `json:"state"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

func viewConnectRequest(id string, req store.ConnectRequest, at time.Time) connectRequestView {
	return connectRequestView{
		RequestID: id,
		ProjectID: req.ProjectID,
		Registry:  store.GitHubRegistry,
		State:     req.StateAt(at),
		ExpiresAt: req.ExpiresAt.UTC(),
		CreatedAt: req.CreatedAt.UTC(),
	}
}

// handleConnectRequestCreate is item 2. It decides in its handler (v1.12.1
// item 3): a delegated session may call it, and it stores no credential.
func (s *Server) handleConnectRequestCreate(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	// Configured and a secrets key: a request that could never connect is
	// not handed out.
	if !s.githubConnect() {
		writeCode(w, "unavailable", msgGitHubOff)
		return
	}
	var body struct {
		Reason *string `json:"reason"`
	}
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	reason := ""
	if body.Reason != nil {
		reason = *body.Reason
	}
	if !reasonOK(reason) {
		writeCode(w, "bad_request", msgReason)
		return
	}
	prefix, ok := consolePagePrefix(s.ConsoleOrigin, "/connect/")
	if !ok {
		// githubConnect() requires the origin; kept so the url is never relative.
		writeCode(w, "unavailable", msgGitHubOff)
		return
	}
	plain, hash, err := secret.NewToken()
	if err != nil {
		s.logger().Error("connect request id failed")
		writeCode(w, "error", "internal error")
		return
	}
	now := s.now()
	expires := now.Add(store.RequestTTL)
	err = s.Store.CreateConnectRequest(r.Context(), hash, store.ConnectRequest{RequestMeta: store.RequestMeta{
		UserID: p.User.ID, ProjectID: id, Reason: reason, CreatedAt: now, ExpiresAt: expires,
	}})
	switch {
	case errors.Is(err, store.ErrTooManyConnectRequests):
		writeCode(w, "conflict", err.Error())
		return
	case err != nil:
		s.writeErr(w, err, false)
		return
	}
	s.auditOK(context.WithoutCancel(r.Context()), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionRegistryGitHubConnectRequested,
		Detail:    map[string]any{"request": auditRequestRef(hash), "via": viaKind(p.Session.Kind)},
	})
	writeOK(w, http.StatusCreated, map[string]any{
		"request_id": plain,
		"url":        prefix + plain,
		"expires_at": expires.UTC(),
	})
}

// handleConnectRequestGet is item 3: the requester's view, console or
// delegated, while the creator still reaches the project in any role.
func (s *Server) handleConnectRequestGet(w http.ResponseWriter, r *http.Request) {
	own, ok := loadOwnRequest(s, w, r, s.Store.GetConnectRequest, authz.CapRead, false)
	if !ok {
		return
	}
	writeOK(w, http.StatusOK, viewConnectRequest(own.id, own.req, s.now()))
}

// connectLogin is the project's current ghcr.io login on the page data.
type connectLogin struct {
	Source   string `json:"source"`
	Username string `json:"username"`
}

// handleConnectRequestPage is item 4: the data of the console connect
// page. The route refuses delegated sessions before this runs.
func (s *Server) handleConnectRequestPage(w http.ResponseWriter, r *http.Request) {
	own, ok := loadOwnRequest(s, w, r, s.Store.GetConnectRequest, authz.CapMembers, true)
	if !ok {
		return
	}
	id, req := own.id, own.req
	proj, err := s.Store.GetProject(r.Context(), req.ProjectID)
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	regs, err := s.Store.ListRegistries(r.Context(), req.ProjectID)
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	var login *connectLogin
	for _, reg := range regs {
		if reg.Host == store.GitHubRegistry {
			login = &connectLogin{Source: reg.Source, Username: reg.Username}
		}
	}
	// writeJSON sends Cache-Control: no-store on every answer.
	writeOK(w, http.StatusOK, struct {
		connectRequestView
		ProjectName  string        `json:"project_name"`
		Reason       string        `json:"reason"`
		CurrentLogin *connectLogin `json:"current_login"`
	}{viewConnectRequest(id, req, s.now()), proj.Name, req.Reason, login})
}

// handleConnectRequestDecline is item 7. The route refuses delegated sessions.
func (s *Server) handleConnectRequestDecline(w http.ResponseWriter, r *http.Request) {
	// The creator must still be owner or admin of the project.
	own, ok := loadOwnRequest(s, w, r, s.Store.GetConnectRequest, authz.CapMembers, true)
	if !ok {
		return
	}
	if !s.requestOpen(w, own.req.RequestMeta) {
		return
	}
	err := s.Store.DeclineConnectRequest(r.Context(), own.hash, own.p.User.ID, s.now())
	if !s.requestWriteErr(w, err, "") {
		return
	}
	s.auditOK(context.WithoutCancel(r.Context()), own.p, store.AuditRow{
		ProjectID: idPtr(own.req.ProjectID),
		Action:    audit.ActionRegistryGitHubConnectDeclined,
		Detail:    map[string]any{"request": auditRequestRef(own.hash)},
	})
	writeOK(w, http.StatusOK, map[string]any{"state": store.RequestDeclined})
}

// startConnectRequest reads the optional start body of item 5. It returns
// the connect request id to keep in the OAuth state, or "" when the body
// names none (v1.29 behaviour). ok is false when it has answered.
func (s *Server) startConnectRequest(w http.ResponseWriter, r *http.Request, p principal, projectID uuid.UUID) (string, bool) {
	var body struct {
		ConnectRequestID *string `json:"connect_request_id"`
	}
	if !decodeOptionalJSON(w, r, &body) {
		return "", false
	}
	if body.ConnectRequestID == nil {
		return "", true
	}
	id := *body.ConnectRequestID
	// A console session may name the request by its handle (api-v1 v1.33 item 8).
	hash, ok := requestHash(id, true)
	if !ok {
		writeCode(w, "not_found", "not found")
		return "", false
	}
	// The caller already passed the owner-or-admin check on this project.
	req, err := s.Store.GetConnectRequest(r.Context(), hash)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (req.UserID != p.User.ID || req.ProjectID != projectID)) {
		writeCode(w, "not_found", "not found")
		return "", false
	}
	if err != nil {
		s.writeErr(w, err, false)
		return "", false
	}
	if !s.requestOpen(w, req.RequestMeta) {
		return "", false
	}
	return id, true
}

// decodeOptionalJSON is decodeJSON that also accepts an empty body (no
// fields), as the start route did before it took a body.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeCode(w, "bad_request", "invalid json")
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeCode(w, "bad_request", "invalid json")
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeCode(w, "bad_request", "invalid json")
		return false
	}
	return true
}
