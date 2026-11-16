package api

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Helpers shared by the request types: value requests (value_requests.go,
// api-v1 v1.28) and file requests (file_requests.go, v1.30). An agent asks;
// the user who asked answers on a console page. Every type has the same id,
// reason rule, access rule and state texts.

const maxRequestReason = 200

const msgReason = "reason must be at most 200 characters of plain text"

// requestIDPattern is the 43-character base64url id secret.NewToken mints.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// reasonOK is the reason rule of every create: at most 200 characters, no
// control characters.
func reasonOK(reason string) bool {
	if !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > maxRequestReason {
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

// consolePagePrefix is <BOT_API_CONSOLE_ORIGIN><page> with a trailing slash
// on the origin removed. ok is false when the origin is not configured.
func consolePagePrefix(origin, page string) (string, bool) {
	origin = strings.TrimRight(origin, "/")
	if origin == "" {
		return "", false
	}
	return origin + page, true
}

// storedRequest is any request type: each embeds store.RequestMeta.
type storedRequest interface{ Meta() store.RequestMeta }

// ownedRequest is a request the caller made, loaded by loadOwnRequest.
type ownedRequest[T storedRequest] struct {
	p    principal
	id   string
	hash []byte
	req  T
}

// loadOwnRequest reads the session ({request_id} route, console only when
// consoleOnly; the route refuses delegated sessions before this runs) and
// the caller's own request, whose project the caller must still reach with
// capability: authz.CapRead for the read (a member in any role, or an org
// owner or admin), authz.CapMembers for the page, the answer and decline.
// A malformed or unknown id, another user's request, or a creator who lost
// that access all get not_found, so an id says nothing to anyone else.
func loadOwnRequest[T storedRequest](s *Server, w http.ResponseWriter, r *http.Request, get func(context.Context, []byte) (T, error), capability string, consoleOnly bool) (ownedRequest[T], bool) {
	var out ownedRequest[T]
	p, ok := s.require(w, r)
	if !ok || (consoleOnly && !requireConsole(w, p)) {
		return out, false
	}
	id := r.PathValue("request_id")
	if !requestIDPattern.MatchString(id) {
		writeCode(w, "not_found", "not found")
		return out, false
	}
	hash := secret.Hash(id)
	req, err := get(r.Context(), hash)
	if errors.Is(err, store.ErrNotFound) || (err == nil && req.Meta().UserID != p.User.ID) {
		writeCode(w, "not_found", "not found")
		return out, false
	}
	if err != nil {
		s.writeErr(w, err, false)
		return out, false
	}
	if err := authz.Authorize(r.Context(), p.User.ID, req.Meta().ProjectID, capability); err != nil {
		var denied *authz.Denied
		if errors.As(err, &denied) {
			writeCode(w, "not_found", "not found")
		} else {
			s.fail(w, err)
		}
		return out, false
	}
	return ownedRequest[T]{p: p, id: id, hash: hash, req: req}, true
}

// requestOpen answers a request that can no longer be answered or declined.
// The store checks the state again under its row lock.
func (s *Server) requestOpen(w http.ResponseWriter, meta store.RequestMeta) bool {
	if err := meta.OpenErr(s.now()); err != nil {
		writeCode(w, "conflict", err.Error())
		return false
	}
	return true
}

// requestWriteErr maps an answer or decline store error. subjectTaken is the
// conflict text for a subject that got a value or file meanwhile (the
// request stays pending). ok is true for nil.
func (s *Server) requestWriteErr(w http.ResponseWriter, err error, subjectTaken string) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrRequestUsed), errors.Is(err, store.ErrRequestExpired):
		writeCode(w, "conflict", err.Error())
	case errors.Is(err, store.ErrKeyExists), errors.Is(err, store.ErrFileExists):
		writeCode(w, "conflict", subjectTaken)
	case errors.Is(err, store.ErrTooManyFiles):
		writeCode(w, "bad_request", "too many files")
	case errors.Is(err, store.ErrNotFound):
		writeCode(w, "not_found", "not found")
	default:
		s.writeErr(w, err, false)
	}
	return false
}
