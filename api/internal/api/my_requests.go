package api

import (
	"encoding/hex"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
)

// maxOpenRequestList is the most rows GET /v1/me/requests returns.
const maxOpenRequestList = 60

// openRequestView is one item of GET /v1/me/requests (api-v1 v1.33 item 8).
// handle is the stored SHA-256 of the id, never the id; there is no reason,
// value or URL.
type openRequestView struct {
	Type        string    `json:"type"`
	Handle      string    `json:"handle"`
	ProjectID   uuid.UUID `json:"project_id"`
	ProjectName string    `json:"project_name"`
	Subject     string    `json:"subject"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// handleMyRequests lists the caller's pending, unexpired value, file and
// connect requests, newest first. Console session only: the route refuses
// api_delegated before this runs, and requireConsole refuses any other kind.
func (s *Server) handleMyRequests(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok || !requireConsole(w, p) {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, uuid.Nil, authz.CapSession); err != nil {
		s.deny(w, r, p, nil, nil, nil, "", err)
		return
	}
	rows, err := s.Store.ListOpenRequests(r.Context(), p.User.ID, s.now(), maxOpenRequestList)
	if err != nil {
		s.fail(w, err)
		return
	}
	items := make([]openRequestView, 0, len(rows))
	for _, row := range rows {
		items = append(items, openRequestView{
			Type:        row.Type,
			Handle:      hex.EncodeToString(row.Hash),
			ProjectID:   row.ProjectID,
			ProjectName: row.ProjectName,
			Subject:     row.Subject,
			ExpiresAt:   row.ExpiresAt.UTC(),
			CreatedAt:   row.CreatedAt.UTC(),
		})
	}
	writeOK(w, http.StatusOK, map[string]any{"items": items})
}
