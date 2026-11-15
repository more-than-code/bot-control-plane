package api

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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

const maxValueRequestReason = 200

const msgReason = "reason must be at most 200 characters of plain text"

// valueRequestID is the 43-character base64url id secret.NewToken mints.
var valueRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

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

// reasonOK is item 2 check 3: at most 200 characters, no control characters.
func reasonOK(reason string) bool {
	if !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > maxValueRequestReason {
		return false
	}
	for _, r := range reason {
		// Control, format (bidi overrides, zero-width) and line or
		// paragraph separators: the note must read as it is stored.
		if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}
	return true
}

// auditRequestRef names a request in audit rows: the first 8 hex characters
// of the SHA-256 of its id (api-v1 v1.28 rev 3), never part of the id, which
// is the token in a live link. hash is that SHA-256.
func auditRequestRef(hash []byte) string {
	return hex.EncodeToString(hash[:4])
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
	prefix, ok := fillURLPrefix(s.ConsoleOrigin)
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
	expires := now.Add(store.ValueRequestTTL)
	err = s.Store.CreateValueRequest(r.Context(), hash, store.ValueRequest{
		UserID: p.User.ID, ProjectID: id, Set: name, Key: body.Key, Reason: reason,
		CreatedAt: now, ExpiresAt: expires,
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

// fillURLPrefix is <BOT_API_CONSOLE_ORIGIN>/fill/ with a trailing slash on
// the origin removed. ok is false when the origin is not configured.
func fillURLPrefix(origin string) (string, bool) {
	origin = strings.TrimRight(origin, "/")
	if origin == "" {
		return "", false
	}
	return origin + "/fill/", true
}

// handleValueRequestGet is item 3: the requester's view, console or delegated.
func (s *Server) handleValueRequestGet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	id, hash, ok := requestIDHash(w, r)
	if !ok {
		return
	}
	req, ok := s.ownValueRequest(w, r, p, hash)
	if !ok {
		return
	}
	// The creator must still reach the project, in any role.
	if !s.requestAccess(w, authz.Authorize(r.Context(), p.User.ID, req.ProjectID, authz.CapSession)) {
		return
	}
	writeOK(w, http.StatusOK, viewValueRequest(id, req, s.now()))
}

// handleValueRequestFillPage is item 4: the data of the console fill page.
// The route refuses delegated sessions before this runs.
func (s *Server) handleValueRequestFillPage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok || !requireConsole(w, p) {
		return
	}
	id, hash, ok := requestIDHash(w, r)
	if !ok {
		return
	}
	req, ok := s.ownValueRequest(w, r, p, hash)
	if !ok {
		return
	}
	if !s.requestAccess(w, authz.Authorize(r.Context(), p.User.ID, req.ProjectID, authz.CapMembers)) {
		return
	}
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
	p, ok := s.require(w, r)
	if !ok || !requireConsole(w, p) {
		return
	}
	_, hash, ok := requestIDHash(w, r)
	if !ok {
		return
	}
	req, ok := s.ownValueRequest(w, r, p, hash)
	if !ok {
		return
	}
	// The creator must still be owner or admin of the project.
	if !s.requestAccess(w, authz.Authorize(r.Context(), p.User.ID, req.ProjectID, authz.CapMembers)) {
		return
	}
	if !s.requestOpen(w, req) || !s.keyStillOpen(w, r, req) {
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
	if !s.requestWriteErr(w, req, err) {
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
	out := map[string]any{"state": store.ValueRequestFilled}
	if stored.RevealPending {
		out["reveal_pending"] = true
	}
	writeOK(w, http.StatusOK, out)
}

// handleValueRequestDecline is item 6. The route refuses delegated sessions.
func (s *Server) handleValueRequestDecline(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok || !requireConsole(w, p) {
		return
	}
	_, hash, ok := requestIDHash(w, r)
	if !ok {
		return
	}
	req, ok := s.ownValueRequest(w, r, p, hash)
	if !ok {
		return
	}
	// The creator must still be owner or admin of the project.
	if !s.requestAccess(w, authz.Authorize(r.Context(), p.User.ID, req.ProjectID, authz.CapMembers)) {
		return
	}
	if !s.requestOpen(w, req) {
		return
	}
	err := s.Store.DeclineValueRequest(r.Context(), hash, p.User.ID, s.now())
	if !s.requestWriteErr(w, req, err) {
		return
	}
	s.auditOK(context.WithoutCancel(r.Context()), p, store.AuditRow{
		ProjectID: idPtr(req.ProjectID),
		Action:    audit.ActionEnvSetValueRequestDeclined,
		Detail:    map[string]any{"set": req.Set, "key": req.Key, "request": auditRequestRef(hash)},
	})
	writeOK(w, http.StatusOK, map[string]any{"state": store.ValueRequestDeclined})
}

// requestIDHash reads {request_id}. A malformed id is not_found, as an
// unknown one is.
func requestIDHash(w http.ResponseWriter, r *http.Request) (string, []byte, bool) {
	id := r.PathValue("request_id")
	if !valueRequestID.MatchString(id) {
		writeCode(w, "not_found", "not found")
		return "", nil, false
	}
	return id, secret.Hash(id), true
}

// ownValueRequest loads a request of the caller. Anything else is
// not_found, so a stranger learns nothing about the id.
func (s *Server) ownValueRequest(w http.ResponseWriter, r *http.Request, p principal, hash []byte) (store.ValueRequest, bool) {
	req, err := s.Store.GetValueRequest(r.Context(), hash)
	if errors.Is(err, store.ErrNotFound) || (err == nil && req.UserID != p.User.ID) {
		writeCode(w, "not_found", "not found")
		return store.ValueRequest{}, false
	}
	if err != nil {
		s.writeErr(w, err, false)
		return store.ValueRequest{}, false
	}
	return req, true
}

// requestAccess answers a failed project check of a request's creator. A
// creator who no longer holds the role gets not_found, as a stranger does.
func (s *Server) requestAccess(w http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	var denied *authz.Denied
	if errors.As(err, &denied) {
		writeCode(w, "not_found", "not found")
		return false
	}
	s.fail(w, err)
	return false
}

// requestOpen answers a request that can no longer be filled or declined.
// The store checks the state again under its row lock.
func (s *Server) requestOpen(w http.ResponseWriter, req store.ValueRequest) bool {
	switch req.StateAt(s.now()) {
	case store.ValueRequestPending:
		return true
	case store.ValueRequestExpired:
		writeCode(w, "conflict", store.ErrRequestExpired.Error())
	default:
		writeCode(w, "conflict", store.ErrRequestUsed.Error())
	}
	return false
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

// requestWriteErr maps a fill or decline store error. ok is true for nil.
func (s *Server) requestWriteErr(w http.ResponseWriter, req store.ValueRequest, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrRequestUsed), errors.Is(err, store.ErrRequestExpired):
		writeCode(w, "conflict", err.Error())
	case errors.Is(err, store.ErrKeyExists):
		writeCode(w, "conflict", keyHasValueMsg(req.Key))
	case errors.Is(err, store.ErrNotFound):
		writeCode(w, "not_found", "not found")
	default:
		s.writeErr(w, err, false)
	}
	return false
}
