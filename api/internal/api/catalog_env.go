package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

const (
	maxRegistryUser  = 255
	maxRegistryToken = 4096
	envPutLimit      = 2 << 20
	maxSecretKeys    = 200
)

func (s *Server) handleEnvSetList(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	delegated := p.Session.Kind == store.KindDelegated
	if !delegated && !requireConsole(w, p) {
		return
	}
	var includePrivate bool
	switch r.URL.Query().Get("include") {
	case "":
	case "private":
		includePrivate = true
	default:
		writeCode(w, "bad_request", `include must be "private"`)
		return
	}
	all, err := s.Store.ListEnvSets(r.Context(), id)
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	consumers, err := s.envSetConsumers(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	// api-v1 v1.33 item 6: private sets only when asked for.
	items := make([]store.EnvSet, 0, len(all))
	for _, item := range all {
		if catalog.IsPrivateSet(item.Name) && !includePrivate {
			continue
		}
		items = append(items, withUsedBy(item, consumers))
	}
	if delegated {
		// api-v1 v1.27 item 5: names and flags only, nothing decrypted.
		out := make([]envSetNamesOnly, 0, len(items))
		for _, item := range items {
			out = append(out, namesOnly(item))
		}
		writeOK(w, http.StatusOK, map[string]any{"items": out})
		return
	}
	out := make([]store.EnvSet, 0, len(items))
	for _, item := range items {
		filled, ok := s.withPreviews(w, r, id, item)
		if !ok {
			return
		}
		out = append(out, filled)
	}
	writeOK(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleEnvSetGet(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	delegated := p.Session.Kind == store.KindDelegated
	if !delegated && !requireConsole(w, p) {
		return
	}
	name, ok := envSetName(w, r)
	if !ok {
		return
	}
	set, err := s.Store.GetEnvSet(r.Context(), id, name)
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	consumers, err := s.envSetConsumers(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	set = withUsedBy(set, consumers)
	if delegated {
		writeOK(w, http.StatusOK, namesOnly(set))
		return
	}
	filled, ok := s.withPreviews(w, r, id, set)
	if !ok {
		return
	}
	writeOK(w, http.StatusOK, filled)
}

func (s *Server) handleEnvSetPut(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	if s.refuseDelegated(w, r, p, idPtr(id), nil, audit.ActionEnvSetReplace) || !requireConsole(w, p) {
		return
	}
	name, ok := envSetName(w, r)
	if !ok {
		return
	}
	raw, ok := readLimitedN(w, r, envPutLimit)
	if !ok {
		return
	}
	var body struct {
		Dotenv        string  `json:"dotenv"`
		NewKeysSecret *bool   `json:"new_keys_secret"`
		Mode          *string `json:"mode"`
	}
	if !decodeRaw(w, raw, &body) {
		return
	}
	mode := "replace"
	if body.Mode != nil {
		mode = *body.Mode
	}
	if mode != "replace" && mode != "create" && mode != "merge" {
		writeCode(w, "bad_request", `mode must be "replace", "create" or "merge"`)
		return
	}
	newKeysSecret := true
	if body.NewKeysSecret != nil {
		newKeysSecret = *body.NewKeysSecret
	}
	pairs, err := catalog.ParseDotenv([]byte(body.Dotenv))
	if err != nil {
		s.writeField(w, err)
		return
	}
	if s.Secrets == nil {
		s.logger().Error("secrets key not configured")
		writeCode(w, "unavailable", errSecretsKey.Error())
		return
	}
	keys := make([]store.EnvSecret, 0, len(pairs))
	names := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		// Seal every key. The store keeps an existing key's visibility
		// inside its transaction and stores this seal only when the key is secret.
		sealed, err := s.sealValue(id, name, pair.Key, pair.Value)
		if err != nil {
			s.secretWriteErr(w, err)
			return
		}
		sealed.Plain = pair.Value
		keys = append(keys, sealed)
		names = append(names, pair.Key)
	}
	sort.Strings(names)
	if mode == "merge" {
		s.mergeEnvSet(w, r, p, id, name, keys, names, newKeysSecret)
		return
	}
	var set store.EnvSet
	if mode == "create" {
		set, err = s.Store.CreateEnvSet(r.Context(), id, name, keys, p.User.ID, s.now(), newKeysSecret)
	} else {
		set, err = s.Store.PutEnvSet(r.Context(), id, name, keys, p.User.ID, s.now(), newKeysSecret)
	}
	if mode == "create" && errors.Is(err, store.ErrConflict) {
		writeCode(w, "conflict", fmt.Sprintf("env set %s already exists", name))
		return
	}
	if errors.Is(err, store.ErrSecretEmpty) {
		writeCode(w, "bad_request", err.Error())
		return
	}
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	set = fillPreviews(set, keys)
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionEnvSetReplace,
		Detail:    map[string]any{"set": name, "keys": names},
	})
	writeOK(w, http.StatusOK, fillEnvSet(set))
}

// mergeEnvSet adds pasted keys to an existing set (api-v1 v1.19 mode merge).
// names are the pasted key names, sorted.
func (s *Server) mergeEnvSet(w http.ResponseWriter, r *http.Request, p principal, id uuid.UUID, name string, keys []store.EnvSecret, names []string, newKeysSecret bool) {
	set, created, err := s.Store.MergeEnvSet(r.Context(), id, name, keys, p.User.ID, s.now(), newKeysSecret)
	switch {
	case errors.Is(err, store.ErrSecretEmpty):
		writeCode(w, "bad_request", err.Error())
		return
	case errors.Is(err, store.ErrNotFound):
		writeCode(w, "not_found", fmt.Sprintf("no env set %s", name))
		return
	case err != nil:
		s.writeErr(w, err, false)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionEnvSetMerge,
		Detail:    map[string]any{"set": name, "keys": names, "created": created},
	})
	// Keys left out of the paste keep their stored values, so their previews
	// come from the stored seals.
	filled, ok := s.withPreviews(w, r, id, set)
	if !ok {
		return
	}
	writeOK(w, http.StatusOK, filled)
}

func (s *Server) handleEnvSetDelete(w http.ResponseWriter, r *http.Request) {
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
	// api-v1 v1.33 item 2: a delegated session gets a plan to confirm.
	if p.Session.Kind == store.KindDelegated {
		s.issueEnvSetDelete(w, r, p, id, name)
		return
	}
	if !requireConsole(w, p) {
		return
	}
	users, err := s.setUsers(r.Context(), id, name)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(users) > 0 {
		if _, gerr := s.Store.GetEnvSet(r.Context(), id, name); gerr == nil {
			writeCode(w, "conflict", usedByMsg(name, users))
			return
		}
	}
	if err := s.Store.DeleteEnvSet(r.Context(), id, name); err != nil {
		if errors.Is(err, store.ErrConflict) {
			if users, uerr := s.setUsers(r.Context(), id, name); uerr == nil && len(users) > 0 {
				writeCode(w, "conflict", usedByMsg(name, users))
				return
			}
		}
		s.writeErr(w, err, false)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionEnvSetDelete,
		Detail:    map[string]any{"set": name},
	})
	writeOK(w, http.StatusOK, map[string]any{})
}

func (s *Server) handleEnvKeyPut(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	if s.refuseDelegated(w, r, p, idPtr(id), nil, audit.ActionEnvSetKeyPut) || !requireConsole(w, p) {
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
	var body struct {
		Value    *string           `json:"value"`
		Generate *catalog.Generate `json:"generate"`
		IsSecret *bool             `json:"is_secret"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	plain, secretFlag, ok := s.keyPutValue(w, body.Value, body.Generate, body.IsSecret)
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
	stored, created, err := s.Store.PutEnvKey(r.Context(), id, name, store.EnvKeyPut{
		Key:        key,
		Plain:      plain,
		Secret:     secretFlag,
		Nonce:      sealed.Nonce,
		Ciphertext: sealed.Ciphertext,
		KeyVersion: sealed.KeyVersion,
		Generated:  body.Generate != nil,
	}, p.User.ID, s.now())
	if errors.Is(err, store.ErrKeyExists) {
		writeCode(w, "bad_request", fmt.Sprintf("%s already has a value; edit it to change it", key))
		return
	}
	if errors.Is(err, store.ErrSecretFixed) || errors.Is(err, store.ErrSecretEmpty) {
		writeCode(w, "bad_request", err.Error())
		return
	}
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	detail := map[string]any{"set": name, "key": key, "created": created}
	if body.Generate != nil {
		detail["generated"] = body.Generate.Kind
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionEnvSetKeyPut,
		Detail:    detail,
	})
	item := envKeyBody(stored.Key, stored.IsSecret, plain, stored.Generated)
	item.RevealPending = stored.RevealPending
	writeOK(w, http.StatusOK, item)
}

// keyPutValue resolves a key PUT body to the value to store and the secret
// flag to request (api-v1 v1.27 item 1): value xor generate, a generated
// value is secret, and a body with neither stores "" as in v1.19. ok is
// false when it wrote a refusal.
func (s *Server) keyPutValue(w http.ResponseWriter, value *string, gen *catalog.Generate, isSecret *bool) (string, *bool, bool) {
	if gen == nil {
		plain := ""
		if value != nil {
			plain = *value
		}
		if err := catalog.ValidateEnvValue(plain); err != nil {
			s.writeField(w, err)
			return "", nil, false
		}
		return plain, isSecret, true
	}
	if value != nil {
		writeCode(w, "bad_request", "send value or generate, not both")
		return "", nil, false
	}
	if err := gen.Validate(); err != nil {
		s.writeField(w, err)
		return "", nil, false
	}
	if isSecret != nil && !*isSecret {
		writeCode(w, "bad_request", "a generated value is secret")
		return "", nil, false
	}
	plain, err := gen.NewValue()
	if err != nil {
		s.logger().Error("generate failed")
		writeCode(w, "error", "internal error")
		return "", nil, false
	}
	t := true
	return plain, &t, true
}

// secretPreview is the preview of one secret value. A generated value is
// always four bullets, so no part of it shows outside reveal (v1.27 item 2).
func secretPreview(plain string, generated bool) string {
	if generated {
		return catalog.SecretPreview("")
	}
	return catalog.SecretPreview(plain)
}

// handleEnvKeyReveal returns a generated value once (api-v1 v1.27 item 3).
// The route wrapper refuses delegated sessions. The store clears
// reveal_pending in one statement, so at most one call per generation
// gets the value.
func (s *Server) handleEnvKeyReveal(w http.ResponseWriter, r *http.Request) {
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
	s.revealKey(w, r, p, id, name, key)
}

// revealKey is the show-once reveal of one generated key (api-v1 v1.27
// item 3), for a shared or a private set.
func (s *Server) revealKey(w http.ResponseWriter, r *http.Request, p principal, id uuid.UUID, name, key string) {
	if s.Secrets == nil {
		s.logger().Error("secrets key not configured")
		writeCode(w, "unavailable", errSecretsKey.Error())
		return
	}
	mat, err := s.Store.RevealEnvKey(r.Context(), id, name, key)
	switch {
	case errors.Is(err, store.ErrConflict):
		writeCode(w, "conflict", fmt.Sprintf("the value of %s was already shown or was not generated; edit it to set a new value", key))
		return
	case errors.Is(err, store.ErrNotFound):
		writeCode(w, "not_found", "not found")
		return
	case err != nil:
		s.writeErr(w, err, false)
		return
	}
	// The flag is already cleared. A value that does not open is not shown;
	// editing the key sets a new one.
	plain, err := s.openEnv(id, name, mat)
	if err != nil {
		s.secretWriteErr(w, err)
		return
	}
	// A client disconnect must not drop the audit row of a value that left.
	s.auditOK(context.WithoutCancel(r.Context()), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionEnvSetKeyReveal,
		Detail:    map[string]any{"set": name, "key": key},
	})
	// writeJSON sends Cache-Control: no-store on every answer, this one included.
	writeOK(w, http.StatusOK, map[string]any{"key": key, "value": plain})
}

// handlePrivateKeyReveal is handleEnvKeyReveal for a key of an entry's
// private set "@<entry>" (api-v1 v1.33 item 6, review A): the set routes
// refuse private names, so a generated private secret is shown once here.
// Console only (the route refuses delegated sessions); same capability,
// show-once rule, answer and audit (set "@<entry>", key).
func (s *Server) handlePrivateKeyReveal(w http.ResponseWriter, r *http.Request) {
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
	s.revealKey(w, r, p, id, catalog.PrivateSet(entry.Name), key)
}

func (s *Server) handleEnvKeyDelete(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	if s.refuseDelegated(w, r, p, idPtr(id), nil, audit.ActionEnvSetKeyDelete) || !requireConsole(w, p) {
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
	if err := s.Store.DeleteEnvKey(r.Context(), id, name, key, p.User.ID, s.now()); err != nil {
		s.writeErr(w, err, false)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionEnvSetKeyDelete,
		Detail:    map[string]any{"set": name, "key": key},
	})
	writeOK(w, http.StatusOK, map[string]any{})
}

// handleEnvKeysSecret marks plain keys of a set secret (api-v1 v1.17 item 1).
// It is one way: no route makes a secret key plain again.
func (s *Server) handleEnvKeysSecret(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	if s.refuseDelegated(w, r, p, idPtr(id), nil, audit.ActionEnvSetKeySecret) || !requireConsole(w, p) {
		return
	}
	name, ok := envSetName(w, r)
	if !ok {
		return
	}
	var body struct {
		Keys []string `json:"keys"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !secretKeysOK(w, body.Keys) {
		return
	}
	if s.Secrets == nil {
		s.logger().Error("secrets key not configured")
		writeCode(w, "unavailable", errSecretsKey.Error())
		return
	}
	// The store reads each plain value under its row lock and calls this to
	// seal it, so the value never leaves the transaction unsealed. A seal
	// error comes back wrapped in sealFailure so it maps like other seal errors.
	sealer := func(key, plain string) (store.EnvSecret, error) {
		sealed, err := s.sealValue(id, name, key, plain)
		if err != nil {
			return store.EnvSecret{}, sealFailure{err}
		}
		return sealed, nil
	}
	set, changed, err := s.Store.MakeEnvKeysSecret(r.Context(), id, name, body.Keys, sealer, p.User.ID, s.now())
	var missing *store.MissingEnvKeyError
	var failed sealFailure
	switch {
	case errors.As(err, &failed):
		s.secretWriteErr(w, failed.err)
		return
	case errors.As(err, &missing):
		writeCode(w, "not_found", missing.Error())
		return
	case err != nil:
		s.writeErr(w, err, false)
		return
	}
	// The change is committed: a client disconnect must not drop its audit rows.
	auditCtx := context.WithoutCancel(r.Context())
	for _, key := range changed {
		s.auditOK(auditCtx, p, store.AuditRow{
			ProjectID: idPtr(id),
			Action:    audit.ActionEnvSetKeySecret,
			Detail:    map[string]any{"set": name, "key": key},
		})
	}
	filled, ok := s.withPreviews(w, r, id, set)
	if !ok {
		return
	}
	writeOK(w, http.StatusOK, filled)
}

// sealFailure marks an error from the handler's sealer as it comes back
// through MakeEnvKeysSecret, so it is answered by secretWriteErr.
type sealFailure struct{ err error }

func (f sealFailure) Error() string { return f.err.Error() }

func (f sealFailure) Unwrap() error { return f.err }

// secretKeysOK checks the v1.17 body: 1 to maxSecretKeys valid keys, no duplicates.
func secretKeysOK(w http.ResponseWriter, keys []string) bool {
	if len(keys) == 0 || len(keys) > maxSecretKeys {
		writeCode(w, "bad_request", fmt.Sprintf("keys must list 1 to %d keys", maxSecretKeys))
		return false
	}
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if !envKeyOK(w, key) {
			return false
		}
		if seen[key] {
			writeCode(w, "bad_request", "duplicate key "+key)
			return false
		}
		seen[key] = true
	}
	return true
}

func (s *Server) handleRegistryList(w http.ResponseWriter, r *http.Request) {
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
	items, err := s.Store.ListRegistries(r.Context(), id)
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	if items == nil {
		items = []store.Registry{}
	}
	// github_connect (api-v1 v1.29 item 5): the console shows Connect GitHub from it.
	writeOK(w, http.StatusOK, map[string]any{"items": items, "github_connect": s.githubConnect()})
}

func (s *Server) handleRegistryPut(w http.ResponseWriter, r *http.Request) {
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
	host, ok := registryHost(w, r)
	if !ok {
		return
	}
	var body struct {
		Username string `json:"username"`
		Token    string `json:"token"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !credentialOK(body.Username, maxRegistryUser) {
		writeCode(w, "bad_request", "invalid username")
		return
	}
	if !credentialOK(body.Token, maxRegistryToken) {
		writeCode(w, "bad_request", "invalid token")
		return
	}
	if s.Secrets == nil {
		s.logger().Error("secrets key not configured")
		writeCode(w, "unavailable", errSecretsKey.Error())
		return
	}
	nonce, ciphertext, err := s.Secrets.Seal(seal.RegistryAAD(id.String(), host), []byte(body.Token))
	if err != nil {
		s.logger().Error("encrypt failed")
		writeCode(w, "error", "internal error")
		return
	}
	reg, err := s.Store.PutRegistry(r.Context(), id, host, store.RegistryWrite{
		Username:   body.Username,
		Nonce:      nonce,
		Ciphertext: ciphertext,
		KeyVersion: seal.KeyVersion,
	}, p.User.ID, s.now())
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionRegistryPut,
		Detail:    map[string]any{"host": host},
	})
	writeOK(w, http.StatusOK, reg)
}

func (s *Server) handleRegistryDelete(w http.ResponseWriter, r *http.Request) {
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
	host, ok := registryHost(w, r)
	if !ok {
		return
	}
	mat, err := s.Store.GetRegistryMaterial(r.Context(), id, host)
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	if mat.Source == store.RegistrySourceGitHub {
		s.deleteGitHubRegistry(w, r, p, id, mat)
		return
	}
	if err := s.Store.DeleteRegistry(r.Context(), id, host); err != nil {
		s.writeErr(w, err, false)
		return
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(id),
		Action:    audit.ActionRegistryDelete,
		Detail:    map[string]any{"host": host},
	})
	writeOK(w, http.StatusOK, map[string]any{})
}

// envSetConsumers is setConsumers of the project's entries (api-v1 v1.33 item 4).
func (s *Server) envSetConsumers(ctx context.Context, projectID uuid.UUID) (map[string][]string, error) {
	entries, err := s.Store.ListCatalogEntries(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return setConsumers(entries), nil
}

// withUsedBy sets used_by, an empty list for a set no entry names.
func withUsedBy(set store.EnvSet, consumers map[string][]string) store.EnvSet {
	used := append([]string{}, consumers[set.Name]...)
	set.UsedBy = &used
	return set
}

func requireConsole(w http.ResponseWriter, p principal) bool {
	if p.Session.Kind != store.KindConsole {
		writeCode(w, "forbidden", "forbidden")
		return false
	}
	return true
}

func envSetName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("set")
	if !catalog.ValidName(name) {
		writeCode(w, "bad_request", "invalid name")
		return "", false
	}
	return name, true
}

func envKeyName(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.PathValue("key")
	if !envKeyOK(w, key) {
		return "", false
	}
	return key, true
}

// envKeyOK writes the ValidateEnvKey refusal and reports whether key is valid.
func envKeyOK(w http.ResponseWriter, key string) bool {
	err := catalog.ValidateEnvKey(key)
	if err == nil {
		return true
	}
	var field *catalog.FieldError
	if errors.As(err, &field) {
		writeCode(w, field.Code, field.Msg)
		return false
	}
	writeCode(w, "bad_request", "invalid env key")
	return false
}

func registryHost(w http.ResponseWriter, r *http.Request) (string, bool) {
	host := r.PathValue("host")
	if !catalog.ValidRegistryHost(host) {
		writeCode(w, "bad_request", "invalid host")
		return "", false
	}
	return host, true
}

func credentialOK(v string, max int) bool {
	return v != "" && len(v) <= max && !strings.ContainsAny(v, "\r\n\x00")
}

func (s *Server) secretWriteErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errSecretsKey):
		s.logger().Error("secrets key not configured")
		writeCode(w, "unavailable", errSecretsKey.Error())
	case errors.Is(err, errDecrypt):
		s.logger().Error("decrypt failed")
		writeCode(w, "error", "internal error")
	default:
		s.logger().Error("encrypt failed")
		writeCode(w, "error", "internal error")
	}
}

func fillEnvSet(set store.EnvSet) store.EnvSet {
	if set.Keys == nil {
		set.Keys = []store.EnvKey{}
	}
	return set
}

func fillPreviews(set store.EnvSet, sealed []store.EnvSecret) store.EnvSet {
	set = fillEnvSet(set)
	plain := map[string]string{}
	for _, key := range sealed {
		plain[key.Key] = key.Plain
	}
	for i := range set.Keys {
		if !set.Keys[i].IsSecret {
			continue
		}
		preview := secretPreview(plain[set.Keys[i].Key], set.Keys[i].Generated)
		set.Keys[i].Preview = &preview
		set.Keys[i].Value = nil
	}
	return set
}

func envKeyBody(key string, secret bool, plain string, generated bool) store.EnvKey {
	item := store.EnvKey{Key: key, IsSecret: secret}
	if secret {
		preview := secretPreview(plain, generated)
		item.Preview = &preview
		return item
	}
	value := plain
	item.Value = &value
	return item
}

func (s *Server) withPreviews(w http.ResponseWriter, r *http.Request, projectID uuid.UUID, set store.EnvSet) (store.EnvSet, bool) {
	set = fillEnvSet(set)
	need := false
	for _, key := range set.Keys {
		if key.IsSecret {
			need = true
			break
		}
	}
	if !need {
		return set, true
	}
	if s.Secrets == nil {
		s.logger().Error("secrets key not configured")
		writeCode(w, "unavailable", errSecretsKey.Error())
		return store.EnvSet{}, false
	}
	mats, err := s.Store.ListEnvMaterial(r.Context(), projectID, set.Name)
	if err != nil {
		s.writeErr(w, err, false)
		return store.EnvSet{}, false
	}
	byKey := map[string]store.EnvMaterial{}
	for _, mat := range mats {
		byKey[mat.Key] = mat
	}
	for i := range set.Keys {
		if !set.Keys[i].IsSecret {
			continue
		}
		mat, ok := byKey[set.Keys[i].Key]
		if !ok {
			s.secretWriteErr(w, errDecrypt)
			return store.EnvSet{}, false
		}
		plain, err := s.openEnv(projectID, set.Name, mat)
		if err != nil {
			s.secretWriteErr(w, err)
			return store.EnvSet{}, false
		}
		preview := secretPreview(plain, mat.Generated)
		set.Keys[i].Preview = &preview
		set.Keys[i].Value = nil
	}
	return set, true
}
