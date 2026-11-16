package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.fileProject(w, r)
	if !ok {
		return
	}
	items, err := s.Store.ListProjectFiles(r.Context(), id)
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	if items == nil {
		items = []store.ProjectFile{}
	}
	writeOK(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleFileGet(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.fileProject(w, r)
	if !ok {
		return
	}
	name, ok := fileName(w, r)
	if !ok {
		return
	}
	item, err := s.Store.GetProjectFile(r.Context(), id, name)
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	if item.IsSecret {
		writeCode(w, "forbidden", "forbidden")
		return
	}
	sum := ""
	if item.SHA256 != nil {
		sum = *item.SHA256
	}
	writeOK(w, http.StatusOK, map[string]any{
		"name":       item.Name,
		"content":    item.Plain,
		"size":       item.Size,
		"sha256":     sum,
		"updated_at": item.UpdatedAt,
		"updated_by": item.UpdatedBy,
	})
}

func (s *Server) handleFilePut(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.fileProject(w, r)
	if !ok {
		return
	}
	name, ok := fileName(w, r)
	if !ok {
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
	secret := true
	if body.Secret != nil {
		secret = *body.Secret
	}
	in, err := s.fileWrite(id, name, body.Content, secret)
	if err != nil {
		s.secretWriteErr(w, err)
		return
	}
	item, err := s.Store.PutProjectFile(r.Context(), id, name, in, p.User.ID, s.now())
	if err != nil {
		if errors.Is(err, store.ErrTooManyFiles) {
			writeCode(w, "bad_request", "too many files")
			return
		}
		s.writeErr(w, err, false)
		return
	}
	item.Plain = ""
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionFilePut,
		Detail:    map[string]any{"name": name, "is_secret": secret, "size": in.Size},
	})
	writeOK(w, http.StatusOK, item)
}

// fileWrite is the stored form of valid file content (v1.9 item 2): sealed
// with the env-set key and the AAD project_id|file|name when secret, plain
// text otherwise. A secret file without a secrets key is errSecretsKey.
// The console PUT and a file request fill both store through it.
func (s *Server) fileWrite(projectID uuid.UUID, name, content string, secret bool) (store.FileWrite, error) {
	sum := sha256.Sum256([]byte(content))
	in := store.FileWrite{
		IsSecret: secret,
		SHA256:   hex.EncodeToString(sum[:]),
		Size:     len(content),
	}
	if !secret {
		in.Plain = content
		return in, nil
	}
	if s.Secrets == nil {
		return store.FileWrite{}, errSecretsKey
	}
	nonce, ciphertext, err := s.Secrets.Seal(seal.FileAAD(projectID.String(), name), []byte(content))
	if err != nil {
		return store.FileWrite{}, err
	}
	in.Nonce = nonce
	in.Ciphertext = ciphertext
	in.KeyVersion = seal.KeyVersion
	return in, nil
}

func (s *Server) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.fileProject(w, r)
	if !ok {
		return
	}
	name, ok := fileName(w, r)
	if !ok {
		return
	}
	// Both rows carry name and outcome only. Content and sha256 stay out.
	if err := s.Store.DeleteProjectFile(r.Context(), id, name); err != nil {
		var used *store.FileInUse
		if errors.As(err, &used) {
			s.auditOK(r.Context(), p, store.AuditRow{
				ProjectID: idPtr(id),
				Action:    audit.ActionFileDelete,
				Detail:    map[string]any{"name": name, "outcome": "conflict"},
			})
			writeCode(w, "conflict", "referenced by "+strings.Join(used.Entries, ", "))
			return
		}
		s.writeErr(w, err, false)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionFileDelete,
		Detail:    map[string]any{"name": name, "outcome": "deleted"},
	})
	writeOK(w, http.StatusOK, nil)
}

func (s *Server) fileProject(w http.ResponseWriter, r *http.Request) (principal, uuid.UUID, bool) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return principal{}, uuid.Nil, false
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return principal{}, uuid.Nil, false
	}
	if !requireConsole(w, p) {
		return principal{}, uuid.Nil, false
	}
	return p, id, true
}

// decodeFileJSON rejects a body that is not UTF-8 before decoding. encoding/json
// would otherwise replace those bytes and store a different string.
func decodeFileJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 || !utf8.Valid(raw) {
		writeCode(w, "bad_request", "invalid json")
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return decodeJSON(w, r, dst)
}

func fileName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if !catalog.ValidFileName(name) {
		writeCode(w, "bad_request", "invalid name")
		return "", false
	}
	return name, true
}
