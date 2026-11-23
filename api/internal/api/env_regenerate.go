package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Regenerate (api-v1 v1.52): a new generated value for an existing secret
// key, for when the one-time reveal was missed or the secret must rotate.
// The value is not returned here; the key becomes reveal_pending and the
// v1.27 item 3 reveal route shows it once. Console only (the routes refuse
// delegated sessions): an agent sends the person a console link instead.
// Running copies keep the old value until their next deploy, so the answer
// names the entries that use the set.

// regenerateBody is the request body: generate is required.
type regenerateBody struct {
	Generate *catalog.Generate `json:"generate"`
}

// regenerateAnswer is the 200 answer: the key's flags and the entries that
// use its set, which keep the old value until they are redeployed.
type regenerateAnswer struct {
	Key           string   `json:"key"`
	IsSecret      bool     `json:"is_secret"`
	Preview       *string  `json:"preview"`
	RevealPending bool     `json:"reveal_pending"`
	UsedBy        []string `json:"used_by"`
}

func (s *Server) handleEnvKeyRegenerate(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	if !requireConsole(w, p) {
		return
	}
	name, ok := envSetName(w, r)
	if !ok {
		return
	}
	key, ok := envKeyName(w, r)
	if !ok {
		return
	}
	s.regenerateKey(w, r, p, id, name, key)
}

// handlePrivateKeyRegenerate is handleEnvKeyRegenerate for a key of an
// entry's private set "@<entry>", as handlePrivateKeyReveal.
func (s *Server) handlePrivateKeyRegenerate(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	if !requireConsole(w, p) {
		return
	}
	entry, err := s.catalogByName(r, id, r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	key, ok := envKeyName(w, r)
	if !ok {
		return
	}
	s.regenerateKey(w, r, p, id, catalog.PrivateSet(entry.Name), key)
}

func (s *Server) regenerateKey(w http.ResponseWriter, r *http.Request, p principal, id uuid.UUID, name, key string) {
	var body regenerateBody
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Generate == nil {
		writeCode(w, "bad_request", "send generate")
		return
	}
	plain, _, ok := s.keyPutValue(w, nil, body.Generate, nil)
	if !ok {
		return
	}
	if s.Secrets == nil {
		s.logger().Error("secrets key not configured")
		writeCode(w, "unavailable", errSecretsKey.Error())
		return
	}
	sealed, err := s.sealValue(id, name, key, plain)
	if err != nil {
		s.secretWriteErr(w, err)
		return
	}
	stored, _, err := s.Store.PutEnvKey(r.Context(), id, name, store.EnvKeyPut{
		Key:        key,
		Plain:      plain,
		Nonce:      sealed.Nonce,
		Ciphertext: sealed.Ciphertext,
		KeyVersion: sealed.KeyVersion,
		Generated:  true,
		Regenerate: true,
	}, p.User.ID, s.now())
	switch {
	case errors.Is(err, store.ErrSecretFixed):
		writeCode(w, "bad_request", fmt.Sprintf("%s is not secret; edit it to change it", key))
		return
	case errors.Is(err, store.ErrNotFound):
		writeCode(w, "not_found", "not found")
		return
	case err != nil:
		s.writeErr(w, err, false)
		return
	}
	// used_by is advice: a failed read does not undo the new value.
	usedBy := []string{}
	if consumers, err := s.envSetConsumers(r.Context(), id); err == nil {
		usedBy = append(usedBy, consumers[name]...)
	} else {
		s.logger().Warn("regenerate: used_by read failed")
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionEnvSetKeyRegenerate,
		Detail:    map[string]any{"set": name, "key": key, "kind": body.Generate.Kind, "used_by": usedBy},
	})
	item := envKeyBody(stored.Key, stored.IsSecret, plain, stored.Generated)
	writeOK(w, http.StatusOK, regenerateAnswer{
		Key:           stored.Key,
		IsSecret:      true,
		Preview:       item.Preview,
		RevealPending: stored.RevealPending,
		UsedBy:        usedBy,
	})
}
