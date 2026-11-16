package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Value requests (api-v1 v1.28): an agent asks for an env value, and the
// user who asked fills it on the console page /fill/<id>. No route here
// returns a value, and no audit row or log line holds one or the reason.

// valueRequestView is the ValueRequest object (item 1).
type valueRequestView struct {
	RequestID string    `json:"request_id"`
	ProjectID uuid.UUID `json:"project_id"`
	Set       string    `json:"set"`
	Key       string    `json:"key"`
	State     string    `json:"state"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

func viewValueRequest(id string, req store.ValueRequest, at time.Time) valueRequestView {
	return valueRequestView{
		RequestID: id,
		ProjectID: req.ProjectID,
		Set:       req.Set,
		Key:       req.Key,
		State:     req.StateAt(at),
		ExpiresAt: req.ExpiresAt.UTC(),
		CreatedAt: req.CreatedAt.UTC(),
	}
}

func keyHasValueMsg(key string) string {
	return fmt.Sprintf("%s already has a value; edit it in the console", key)
}

// handleValueRequestCreate is item 2. It decides in its handler (v1.12.1
// item 3): a delegated session may call it, and it writes no env value.
func (s *Server) handleValueRequestCreate(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	name, ok := envSetName(w, r)
	if !ok {
		return
	}
	var body struct {
		Key    string  `json:"key"`
		Reason *string `json:"reason"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	set, err := s.Store.GetEnvSet(r.Context(), id, name)
	if errors.Is(err, store.ErrNotFound) {
		writeCode(w, "not_found", fmt.Sprintf("no env set %s", name))
		return
	}
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	if !envKeyOK(w, body.Key) {
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
	for _, k := range set.Keys {
		if k.Key == body.Key && !k.NeedsValue {
			writeCode(w, "conflict", keyHasValueMsg(body.Key))
			return
		}
	}
	// A request that could never be filled is not handed out.
	if s.Secrets == nil {
		s.logger().Error("secrets key not configured")
		writeCode(w, "unavailable", errSecretsKey.Error())
		return
	}
	prefix, ok := consolePagePrefix(s.ConsoleOrigin, "/fill/")
	if !ok {
		s.logger().Error("console origin is not configured")
		writeCode(w, "error", "internal error")
		return
	}
	plain, hash, err := secret.NewToken()
	if err != nil {
		s.logger().Error("value request id failed")
		writeCode(w, "error", "internal error")
		return
	}
	now := s.now()
	expires := now.Add(store.RequestTTL)
	err = s.Store.CreateValueRequest(r.Context(), hash, store.ValueRequest{
		RequestMeta: store.RequestMeta{UserID: p.User.ID, ProjectID: id, Reason: reason, CreatedAt: now, ExpiresAt: expires},
		Set:         name,
		Key:         body.Key,
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeCode(w, "not_found", fmt.Sprintf("no env set %s", name))
		return
	case errors.Is(err, store.ErrKeyExists):
		writeCode(w, "conflict", keyHasValueMsg(body.Key))
		return
	case errors.Is(err, store.ErrTooManyRequests):
		writeCode(w, "conflict", err.Error())
		return
	case err != nil:
		s.writeErr(w, err, false)
		return
	}
	s.auditOK(context.WithoutCancel(r.Context()), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionEnvSetValueRequested,
		Detail:    map[string]any{"set": name, "key": body.Key, "request": auditRequestRef(hash)},
	})
	writeOK(w, http.StatusCreated, map[string]any{
		"request_id": plain,
		"url":        prefix + plain,
		"expires_at": expires.UTC(),
	})
}

// handleValueRequestGet is item 3: the requester's view, console or delegated.
func (s *Server) handleValueRequestGet(w http.ResponseWriter, r *http.Request) {
	own, ok := loadOwnRequest(s, w, r, s.Store.GetValueRequest, authz.CapRead, false)
	if !ok {
		return
	}
	writeOK(w, http.StatusOK, viewValueRequest(own.id, own.req, s.now()))
}

// handleValueRequestFillPage is item 4: the data of the console fill page.
// The route refuses delegated sessions before this runs.
func (s *Server) handleValueRequestFillPage(w http.ResponseWriter, r *http.Request) {
	own, ok := loadOwnRequest(s, w, r, s.Store.GetValueRequest, authz.CapMembers, true)
	if !ok {
		return
	}
	id, req := own.id, own.req
	proj, err := s.Store.GetProject(r.Context(), req.ProjectID)
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	// writeJSON sends Cache-Control: no-store on every answer.
	writeOK(w, http.StatusOK, struct {
		valueRequestView
		ProjectName string `json:"project_name"`
		Reason      string `json:"reason"`
	}{viewValueRequest(id, req, s.now()), proj.Name, req.Reason})
}

// handleValueRequestFill is item 5. The route refuses delegated sessions.
func (s *Server) handleValueRequestFill(w http.ResponseWriter, r *http.Request) {
	// The creator must still be owner or admin of the project.
	own, ok := loadOwnRequest(s, w, r, s.Store.GetValueRequest, authz.CapMembers, true)
	if !ok {
		return
	}
	p, hash, req := own.p, own.hash, own.req
	if !s.requestOpen(w, req.RequestMeta) || !s.keyStillOpen(w, r, req) {
		return
	}
	var body struct {
		Value    *string           `json:"value"`
		Generate *catalog.Generate `json:"generate"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if (body.Value == nil) == (body.Generate == nil) {
		writeCode(w, "bad_request", "send value or generate, not both")
		return
	}
	plain, _, ok := s.keyPutValue(w, body.Value, body.Generate, nil)
	if !ok {
		return
	}
	if plain == "" {
		writeCode(w, "bad_request", "value must not be empty")
		return
	}
	if s.Secrets == nil {
		s.logger().Error("secrets key not configured")
		writeCode(w, "unavailable", errSecretsKey.Error())
		return
	}
	sealed, err := s.sealValue(req.ProjectID, req.Set, req.Key, plain)
	if err != nil {
		s.secretWriteErr(w, err)
		return
	}
	stored, err := s.Store.FillValueRequest(r.Context(), hash, p.User.ID, s.now(), store.EnvKeyPut{
		Key:        req.Key,
		Nonce:      sealed.Nonce,
		Ciphertext: sealed.Ciphertext,
		KeyVersion: sealed.KeyVersion,
		Generated:  body.Generate != nil,
	})
	if !s.requestWriteErr(w, err, keyHasValueMsg(req.Key)) {
		return
	}
	detail := map[string]any{"set": req.Set, "key": req.Key, "via": "value_request", "request": auditRequestRef(hash)}
	if body.Generate != nil {
		detail["generated"] = body.Generate.Kind
	}
	// The value is committed: a client disconnect must not drop the row.
	s.auditOK(context.WithoutCancel(r.Context()), p, store.AuditRow{
		ProjectID: idPtr(req.ProjectID),
		Action:    audit.ActionEnvSetKeyPut,
		Detail:    detail,
	})
	out := map[string]any{"state": store.RequestFilled}
	if stored.RevealPending {
		out["reveal_pending"] = true
	}
	writeOK(w, http.StatusOK, out)
}

// handleValueRequestDecline is item 6. The route refuses delegated sessions.
func (s *Server) handleValueRequestDecline(w http.ResponseWriter, r *http.Request) {
	// The creator must still be owner or admin of the project.
	own, ok := loadOwnRequest(s, w, r, s.Store.GetValueRequest, authz.CapMembers, true)
	if !ok {
		return
	}
	p, hash, req := own.p, own.hash, own.req
	if !s.requestOpen(w, req.RequestMeta) {
		return
	}
	err := s.Store.DeclineValueRequest(r.Context(), hash, p.User.ID, s.now())
	if !s.requestWriteErr(w, err, keyHasValueMsg(req.Key)) {
		return
	}
	s.auditOK(context.WithoutCancel(r.Context()), p, store.AuditRow{
		ProjectID: idPtr(req.ProjectID),
		Action:    audit.ActionEnvSetValueRequestDeclined,
		Detail:    map[string]any{"set": req.Set, "key": req.Key, "request": auditRequestRef(hash)},
	})
	writeOK(w, http.StatusOK, map[string]any{"state": store.RequestDeclined})
}

// keyStillOpen answers a key that got a value since the request was made
// (api-v1 v1.28 item 5 checks it before the value rules). The store checks
// again under the set lock.
func (s *Server) keyStillOpen(w http.ResponseWriter, r *http.Request, req store.ValueRequest) bool {
	set, err := s.Store.GetEnvSet(r.Context(), req.ProjectID, req.Set)
	if errors.Is(err, store.ErrNotFound) {
		writeCode(w, "not_found", "not found")
		return false
	}
	if err != nil {
		s.writeErr(w, err, false)
		return false
	}
	for _, k := range set.Keys {
		if k.Key == req.Key && !k.NeedsValue {
			writeCode(w, "conflict", keyHasValueMsg(req.Key))
			return false
		}
	}
	return true
}
