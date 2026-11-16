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

// File requests (api-v1 v1.30): an agent asks for a managed file, and the
// user who asked adds it on the console page /fill-file/<id>. The id,
// lifetime, single use, per-user limit (counted per type), access rule and
// state texts are those of every request type (requests.go). No route here
// returns file content, and no audit row or log line holds content or the
// reason.

const msgSecretFilePlain = "a secret file cannot be made plain"

// fileRequestView is the FileRequest object (item 1).
type fileRequestView struct {
	RequestID string    `json:"request_id"`
	ProjectID uuid.UUID `json:"project_id"`
	Name      string    `json:"name"`
	Secret    bool      `json:"secret"`
	State     string    `json:"state"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

func viewFileRequest(id string, req store.FileRequest, at time.Time) fileRequestView {
	return fileRequestView{
		RequestID: id,
		ProjectID: req.ProjectID,
		Name:      req.Name,
		Secret:    req.Secret,
		State:     req.StateAt(at),
		ExpiresAt: req.ExpiresAt.UTC(),
		CreatedAt: req.CreatedAt.UTC(),
	}
}

func fileExistsMsg(name string) string {
	return fmt.Sprintf("file %s already exists; replace it in the console", name)
}

// handleFileRequestCreate is item 2. It decides in its handler (v1.12.1
// item 3): a delegated session may call it, and it writes no file.
func (s *Server) handleFileRequestCreate(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	var body struct {
		Name   string  `json:"name"`
		Secret *bool   `json:"secret"`
		Reason *string `json:"reason"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !catalog.ValidFileName(body.Name) {
		writeCode(w, "bad_request", "invalid name")
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
	wantSecret := body.Secret == nil || *body.Secret
	// Checks 3 and 4 in contract order; the store repeats them under the
	// project lock.
	files, err := s.Store.ListProjectFiles(r.Context(), id)
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	for _, f := range files {
		if f.Name == body.Name {
			writeCode(w, "conflict", fileExistsMsg(body.Name))
			return
		}
	}
	if len(files) >= store.MaxProjectFiles {
		writeCode(w, "bad_request", "too many files")
		return
	}
	// A secret request that could never be filled is not handed out.
	if wantSecret && s.Secrets == nil {
		s.logger().Error("secrets key not configured")
		writeCode(w, "unavailable", errSecretsKey.Error())
		return
	}
	prefix, ok := consolePagePrefix(s.ConsoleOrigin, "/fill-file/")
	if !ok {
		s.logger().Error("console origin is not configured")
		writeCode(w, "error", "internal error")
		return
	}
	plain, hash, err := secret.NewToken()
	if err != nil {
		s.logger().Error("file request id failed")
		writeCode(w, "error", "internal error")
		return
	}
	now := s.now()
	expires := now.Add(store.RequestTTL)
	err = s.Store.CreateFileRequest(r.Context(), hash, store.FileRequest{
		RequestMeta: store.RequestMeta{UserID: p.User.ID, ProjectID: id, Reason: reason, CreatedAt: now, ExpiresAt: expires},
		Name:        body.Name,
		Secret:      wantSecret,
	})
	switch {
	case errors.Is(err, store.ErrFileExists):
		writeCode(w, "conflict", fileExistsMsg(body.Name))
		return
	case errors.Is(err, store.ErrTooManyFiles):
		writeCode(w, "bad_request", "too many files")
		return
	case errors.Is(err, store.ErrTooManyFileRequests):
		writeCode(w, "conflict", err.Error())
		return
	case err != nil:
		s.writeErr(w, err, false)
		return
	}
	s.auditOK(context.WithoutCancel(r.Context()), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionFileRequested,
		Detail:    map[string]any{"name": body.Name, "secret": wantSecret, "request": auditRequestRef(hash)},
	})
	writeOK(w, http.StatusCreated, map[string]any{
		"request_id": plain,
		"url":        prefix + plain,
		"expires_at": expires.UTC(),
	})
}

// handleFileRequestGet is item 3: the requester's view, console or delegated,
// while the creator still reaches the project.
func (s *Server) handleFileRequestGet(w http.ResponseWriter, r *http.Request) {
	own, ok := loadOwnRequest(s, w, r, s.Store.GetFileRequest, authz.CapRead, false)
	if !ok {
		return
	}
	writeOK(w, http.StatusOK, viewFileRequest(own.id, own.req, s.now()))
}

// handleFileRequestFillPage is item 4: the data of the console fill page.
// The route refuses delegated sessions before this runs.
func (s *Server) handleFileRequestFillPage(w http.ResponseWriter, r *http.Request) {
	own, ok := loadOwnRequest(s, w, r, s.Store.GetFileRequest, authz.CapMembers, true)
	if !ok {
		return
	}
	proj, err := s.Store.GetProject(r.Context(), own.req.ProjectID)
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	// writeJSON sends Cache-Control: no-store on every answer.
	writeOK(w, http.StatusOK, struct {
		fileRequestView
		ProjectName string `json:"project_name"`
		Reason      string `json:"reason"`
	}{viewFileRequest(own.id, own.req, s.now()), proj.Name, own.req.Reason})
}

// handleFileRequestFill is item 5. The route refuses delegated sessions.
func (s *Server) handleFileRequestFill(w http.ResponseWriter, r *http.Request) {
	// The creator must still be owner or admin of the project.
	own, ok := loadOwnRequest(s, w, r, s.Store.GetFileRequest, authz.CapMembers, true)
	if !ok {
		return
	}
	req := own.req
	if !s.requestOpen(w, req.RequestMeta) || !s.fileStillAbsent(w, r, req) {
		return
	}
	var body struct {
		Content string `json:"content"`
		Secret  *bool  `json:"secret"`
	}
	if !decodeFileJSON(w, r, &body) {
		return
	}
	if !catalog.ValidFileContent(body.Content) {
		writeCode(w, "bad_request", "invalid content")
		return
	}
	// The flag defaults to the request's; plain may become secret, never
	// the other way (as for env keys).
	secretFile := req.Secret
	if body.Secret != nil {
		if req.Secret && !*body.Secret {
			writeCode(w, "bad_request", msgSecretFilePlain)
			return
		}
		secretFile = *body.Secret
	}
	in, err := s.fileWrite(req.ProjectID, req.Name, body.Content, secretFile)
	if err != nil {
		s.secretWriteErr(w, err)
		return
	}
	err = s.Store.FillFileRequest(r.Context(), own.hash, own.p.User.ID, s.now(), in)
	if !s.requestWriteErr(w, err, fileExistsMsg(req.Name)) {
		return
	}
	// The file is committed: a client disconnect must not drop the row.
	s.auditOK(context.WithoutCancel(r.Context()), own.p, store.AuditRow{
		ProjectID: idPtr(req.ProjectID),
		Action:    audit.ActionFilePut,
		Detail:    map[string]any{"name": req.Name, "secret": secretFile, "via": "file_request", "request": auditRequestRef(own.hash)},
	})
	writeOK(w, http.StatusOK, map[string]any{"state": store.RequestFilled, "secret": secretFile})
}

// handleFileRequestDecline is item 6. The route refuses delegated sessions.
func (s *Server) handleFileRequestDecline(w http.ResponseWriter, r *http.Request) {
	own, ok := loadOwnRequest(s, w, r, s.Store.GetFileRequest, authz.CapMembers, true)
	if !ok {
		return
	}
	if !s.requestOpen(w, own.req.RequestMeta) {
		return
	}
	err := s.Store.DeclineFileRequest(r.Context(), own.hash, own.p.User.ID, s.now())
	if !s.requestWriteErr(w, err, fileExistsMsg(own.req.Name)) {
		return
	}
	s.auditOK(context.WithoutCancel(r.Context()), own.p, store.AuditRow{
		ProjectID: idPtr(own.req.ProjectID),
		Action:    audit.ActionFileRequestDeclined,
		Detail:    map[string]any{"name": own.req.Name, "request": auditRequestRef(own.hash)},
	})
	writeOK(w, http.StatusOK, map[string]any{"state": store.RequestDeclined})
}

// fileStillAbsent answers a file created since the request was made (item 5
// checks it before the content rules). It reads no content. The store
// checks again under the project lock.
func (s *Server) fileStillAbsent(w http.ResponseWriter, r *http.Request, req store.FileRequest) bool {
	exists, err := s.Store.ProjectFileExists(r.Context(), req.ProjectID, req.Name)
	if err != nil {
		s.writeErr(w, err, false)
		return false
	}
	if exists {
		writeCode(w, "conflict", fileExistsMsg(req.Name))
		return false
	}
	return true
}
