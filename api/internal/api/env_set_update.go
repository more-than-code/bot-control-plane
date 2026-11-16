package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Env set update and delegated delete (api-v1 v1.33 items 1 and 2), and the
// consumers of a set (item 4). Both writes are plans the person confirms
// with POST /v1/confirmations/{id}; the plan, its stored body and the audit
// rows name keys only. The one value a plan body holds is a plain key's
// value (set, or add with value), as for create (v1.32 item 2).

const (
	msgChangeSecretValue = "change secret values in the console or with request_env_value"
	msgPatchEmpty        = "send at least one of set, add, remove or make_secret"
)

// envChangePlanBody is the stored body of an update or delete plan. Action
// tells it apart from a create plan, which has none.
type envChangePlanBody struct {
	Action     string            `json:"action"`
	Set        map[string]string `json:"set,omitempty"`
	Add        []envCreateKey    `json:"add,omitempty"`
	Remove     []string          `json:"remove,omitempty"`
	MakeSecret []string          `json:"make_secret,omitempty"`
	// State is store.EnvStateHash of the set when the plan was made. The
	// confirm applies only to that state (review C, 2026-11-16).
	State string `json:"state,omitempty"`
}

const (
	envChangeUpdate = "update"
	envChangeDelete = "delete"
)

// entryRefsOf is the env references of a stored image or program entry. ok
// is false for an image spec that does not parse.
func entryRefsOf(e store.CatalogEntry) (catalog.EnvRefs, bool) {
	switch e.Kind {
	case "image":
		img, err := catalog.ParseStored(e.ImageRaw)
		if err != nil {
			return catalog.EnvRefs{}, false
		}
		return img.EnvRefs(), true
	case "program":
		return e.Spec.Program().EnvRefs(), true
	default:
		return catalog.EnvRefs{}, false
	}
}

// setConsumers maps each set name to the entries naming it in env_sets or
// secret_env, sorted (api-v1 v1.33 item 4).
func setConsumers(entries []store.CatalogEntry) map[string][]string {
	out := map[string][]string{}
	for _, e := range entries {
		refs, ok := entryRefsOf(e)
		if !ok {
			continue
		}
		for _, set := range refs.SetNames() {
			out[set] = append(out[set], e.Name)
		}
	}
	for set := range out {
		sort.Strings(out[set])
	}
	return out
}

// envSources maps each key an entry resolves to the set it comes from: the
// last set in env_sets that has it, then a secret_env source, then "" for
// inline env (the merge order of api-v1 v1.8 rule 12).
func envSources(refs catalog.EnvRefs, names map[string][]string) map[string]string {
	out := map[string]string{}
	for _, set := range refs.Sets {
		for _, key := range names[set] {
			out[key] = set
		}
	}
	for key, ref := range refs.Secret {
		set, _, _ := strings.Cut(ref, ".")
		out[key] = set
	}
	for key := range refs.Inline {
		out[key] = ""
	}
	return out
}

// keyUsers lists the entries that resolve set.key (api-v1 v1.33 item 1): the
// key's final source is the set, or a secret_env value names set.key. A
// ${KEY} reference resolves against the merged env, so it adds nothing.
func keyUsers(entries []store.CatalogEntry, names map[string][]string, set, key string) []string {
	var out []string
	for _, e := range entries {
		refs, ok := entryRefsOf(e)
		if !ok {
			continue
		}
		used := false
		if src, ok := envSources(refs, names)[key]; ok && src == set {
			used = true
		}
		for _, ref := range refs.Secret {
			if ref == set+"."+key {
				used = true
			}
		}
		if used {
			out = append(out, e.Name)
		}
	}
	sort.Strings(out)
	return out
}

func usedByMsg(set string, entries []string) string {
	return fmt.Sprintf("env set %s is used by entries %s", set, strings.Join(entries, ", "))
}

// setUsers is the entries naming set, read now.
func (s *Server) setUsers(ctx context.Context, projectID uuid.UUID, set string) ([]string, error) {
	entries, err := s.Store.ListCatalogEntries(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return setConsumers(entries)[set], nil
}

type envPatchBody struct {
	Set        map[string]string `json:"set"`
	Add        []envCreateKeyIn  `json:"add"`
	Remove     []string          `json:"remove"`
	MakeSecret []string          `json:"make_secret"`
	Key        string            `json:"idempotency_key"`
}

// handleEnvSetPatch is item 1: a plan, for a console or delegated session.
func (s *Server) handleEnvSetPatch(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	if p.Session.Kind != store.KindDelegated && !requireConsole(w, p) {
		return
	}
	name, ok := envSetName(w, r)
	if !ok {
		return
	}
	var body envPatchBody
	if !decodeJSON(w, r, &body) {
		return
	}
	plan, ok := s.normalizePatch(w, body)
	if !ok {
		return
	}
	if !s.envChangeFits(w, r, id, name, plan) {
		return
	}
	s.issueEnvSetChange(w, r, p, id, name, plan, body.Key)
}

// normalizePatch checks the body shape: at least one non-empty list, valid
// keys, each key in one list only, plain values under the env value rules.
func (s *Server) normalizePatch(w http.ResponseWriter, body envPatchBody) (envChangePlanBody, bool) {
	plan := envChangePlanBody{Action: envChangeUpdate}
	if len(body.Set) == 0 && len(body.Add) == 0 && len(body.Remove) == 0 && len(body.MakeSecret) == 0 {
		writeCode(w, "bad_request", msgPatchEmpty)
		return plan, false
	}
	if len(body.Set) > maxCreateKeys || len(body.Add) > maxCreateKeys || len(body.Remove) > maxCreateKeys || len(body.MakeSecret) > maxCreateKeys {
		writeCode(w, "bad_request", "each list may name at most 64 keys")
		return plan, false
	}
	seen := map[string]bool{}
	claim := func(key string) bool {
		if !envKeyOK(w, key) {
			return false
		}
		if seen[key] {
			writeCode(w, "bad_request", "key "+key+" is named more than once")
			return false
		}
		seen[key] = true
		return true
	}
	setKeys := make([]string, 0, len(body.Set))
	for key := range body.Set {
		setKeys = append(setKeys, key)
	}
	sort.Strings(setKeys)
	for _, key := range setKeys {
		if !claim(key) {
			return plan, false
		}
		if err := catalog.ValidateEnvValue(body.Set[key]); err != nil {
			s.writeField(w, err)
			return plan, false
		}
	}
	add, ok := s.normalizeCreateKeys(w, body.Add)
	if !ok {
		return plan, false
	}
	for _, k := range add {
		if !claim(k.Key) {
			return plan, false
		}
	}
	for _, list := range [][]string{body.Remove, body.MakeSecret} {
		for _, key := range list {
			if !claim(key) {
				return plan, false
			}
		}
	}
	if len(body.Set) > 0 {
		plan.Set = body.Set
	}
	plan.Add = add
	plan.Remove = sortedCopy(body.Remove)
	plan.MakeSecret = sortedCopy(body.MakeSecret)
	if (needsSecretsKey(add) || len(plan.MakeSecret) > 0) && s.Secrets == nil {
		s.logger().Error("secrets key not configured")
		writeCode(w, "unavailable", errSecretsKey.Error())
		return plan, false
	}
	return plan, true
}

func sortedCopy(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// envChangeFits checks an update plan against the set as stored now: the
// set exists, set keys are existing plain keys, add keys are new, remove
// and make_secret keys exist, and no entry resolves a removed key. It runs
// at issue and again at confirm.
func (s *Server) envChangeFits(w http.ResponseWriter, r *http.Request, projectID uuid.UUID, name string, plan envChangePlanBody) bool {
	set, err := s.Store.GetEnvSet(r.Context(), projectID, name)
	if errors.Is(err, store.ErrNotFound) {
		writeCode(w, "not_found", fmt.Sprintf("no env set %s", name))
		return false
	}
	if err != nil {
		s.writeErr(w, err, false)
		return false
	}
	have := map[string]store.EnvKey{}
	for _, k := range set.Keys {
		have[k.Key] = k
	}
	missing := func(key string) bool {
		if _, ok := have[key]; ok {
			return false
		}
		writeCode(w, "not_found", fmt.Sprintf("no key %s in env set %s", key, name))
		return true
	}
	keys := make([]string, 0, len(plan.Set))
	for key := range plan.Set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if missing(key) {
			return false
		}
		if have[key].IsSecret {
			writeCode(w, "bad_request", msgChangeSecretValue)
			return false
		}
	}
	for _, k := range plan.Add {
		if _, ok := have[k.Key]; ok {
			writeCode(w, "conflict", fmt.Sprintf("%s is already in env set %s", k.Key, name))
			return false
		}
	}
	for _, key := range append(append([]string{}, plan.Remove...), plan.MakeSecret...) {
		if missing(key) {
			return false
		}
	}
	if len(plan.Remove) == 0 {
		return true
	}
	entries, err := s.Store.ListCatalogEntries(r.Context(), projectID)
	if err != nil {
		s.fail(w, err)
		return false
	}
	names, err := s.Store.ListEnvKeyNames(r.Context(), projectID)
	if err != nil {
		s.fail(w, err)
		return false
	}
	for _, key := range plan.Remove {
		if users := keyUsers(entries, names, name, key); len(users) > 0 {
			writeCode(w, "conflict", fmt.Sprintf("%s is used by entries %s; update them first", key, strings.Join(users, ", ")))
			return false
		}
	}
	return true
}

// envSetUpdatePlan is the plan sentence: keys and actions, never a value,
// with the v1.32 item 2 reminder for a plain value under a secret-looking
// name.
func envSetUpdatePlan(name string, plan envChangePlanBody) string {
	var parts, warnings []string
	if len(plan.Set) > 0 {
		keys := make([]string, 0, len(plan.Set))
		for key := range plan.Set {
			keys = append(keys, key)
			if catalog.LooksSecret(key) {
				warnings = append(warnings, key+plainSecretWarning)
			}
		}
		sort.Strings(keys)
		sort.Strings(warnings)
		parts = append(parts, "set the plain value of "+strings.Join(keys, ", "))
	}
	if len(plan.Add) > 0 {
		added, more := envKeysPhrase(plan.Add)
		warnings = append(warnings, more...)
		parts = append(parts, "add "+added)
	}
	if len(plan.Remove) > 0 {
		parts = append(parts, "remove "+strings.Join(plan.Remove, ", "))
	}
	if len(plan.MakeSecret) > 0 {
		parts = append(parts, "make secret (one way) "+strings.Join(plan.MakeSecret, ", "))
	}
	out := "update env set " + name + ": " + strings.Join(parts, "; ")
	if len(warnings) > 0 {
		out += ". " + strings.Join(warnings, " ")
	}
	return out
}

func envSetDeletePlan(name string, keys []string) string {
	if len(keys) == 0 {
		return "delete env set " + name + " (no keys)"
	}
	return "delete env set " + name + " and its " + keyCount(len(keys)) + " (" + strings.Join(keys, ", ") + "); open value requests for it stop working"
}

// envChangeRequestHash binds the project, the set and the plan body. The
// idempotency key is not part of it.
func envChangeRequestHash(projectID uuid.UUID, name string, body []byte) []byte {
	raw := `{"action":"env_set.change","name":` + jsonQuote(name) +
		`,"project_id":` + jsonQuote(projectID.String()) + `,"plan":` + string(body) + `}`
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

// issueEnvSetChange stores an update or delete plan and answers 202.
func (s *Server) issueEnvSetChange(w http.ResponseWriter, r *http.Request, p principal, projectID uuid.UUID, name string, plan envChangePlanBody, key string) {
	if !catalog.ValidIdempotencyKey(key) {
		writeCode(w, "bad_request", "invalid idempotency_key")
		return
	}
	// The request hash binds the request, not the set state, so a replay
	// with the same key still matches after the confirm changed the set.
	request, err := json.Marshal(plan)
	if err != nil {
		s.fail(w, err)
		return
	}
	mats, err := s.Store.ListEnvMaterial(r.Context(), projectID, name)
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	plan.State = store.EnvStateHash(mats)
	body, err := json.Marshal(plan)
	if err != nil {
		s.fail(w, err)
		return
	}
	var text string
	if plan.Action == envChangeDelete {
		set, err := s.Store.GetEnvSet(r.Context(), projectID, name)
		if err != nil {
			s.writeErr(w, err, false)
			return
		}
		keys := make([]string, 0, len(set.Keys))
		for _, k := range set.Keys {
			keys = append(keys, k.Key)
		}
		text = envSetDeletePlan(name, keys)
	} else {
		text = envSetUpdatePlan(name, plan)
	}
	reqHash := envChangeRequestHash(projectID, name, request)
	if !s.claimOrReplay(w, r, p.User.ID, key, reqHash) {
		return
	}
	plain, tokenHash, err := secret.NewToken()
	if err != nil {
		s.failClaimed(w, r, p.User.ID, key, err)
		return
	}
	now := s.now().UTC()
	expires := now.Add(store.ConfirmationTTL)
	row := store.EnvSetConfirmation{
		UserID: p.User.ID, ProjectID: projectID, SetName: name, Body: body,
		RequestHash: reqHash, IdempotencyKey: key, CreatedAt: now, ExpiresAt: expires,
	}
	if err := s.Store.PutEnvSetConfirmation(r.Context(), tokenHash, row); err != nil {
		s.failClaimed(w, r, p.User.ID, key, err)
		return
	}
	payload := map[string]any{
		"confirmation_id": plain,
		"plan":            text,
		"expires_at":      expires.Format(time.RFC3339),
	}
	out := envelopeRaw("ok", "ok", payload)
	s.finishIdempotency(r.Context(), p.User.ID, key, http.StatusAccepted, out)
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(projectID),
		Action:    audit.ActionEnvSetWriteConfirmation,
		Detail:    map[string]any{"set": name, "action": plan.Action},
	})
	writeJSON(w, http.StatusAccepted, out)
}

// issueEnvSetDelete is item 2 for a delegated session: a plan, refused while
// any entry names the set. A set the confirm already deleted replays this
// key's stored answer.
func (s *Server) issueEnvSetDelete(w http.ResponseWriter, r *http.Request, p principal, projectID uuid.UUID, name string) {
	plan := envChangePlanBody{Action: envChangeDelete}
	body, _ := json.Marshal(plan)
	if _, err := s.Store.GetEnvSet(r.Context(), projectID, name); errors.Is(err, store.ErrNotFound) {
		if key, ok := idempotencyKeyQuiet(r); ok && s.replayStoredIdempotency(w, r, p.User.ID, key, envChangeRequestHash(projectID, name, body)) {
			return
		}
		writeCode(w, "not_found", fmt.Sprintf("no env set %s", name))
		return
	} else if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		Key string `json:"idempotency_key"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	users, err := s.setUsers(r.Context(), projectID, name)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(users) > 0 {
		writeCode(w, "conflict", usedByMsg(name, users))
		return
	}
	s.issueEnvSetChange(w, r, p, projectID, name, plan, req.Key)
}

// confirmEnvSetChange applies an update or delete plan. The caller checked
// the plan's owner, state, idempotency key and the caller's role.
func (s *Server) confirmEnvSetChange(w http.ResponseWriter, r *http.Request, p principal, hash []byte, c store.EnvSetConfirmation, plan envChangePlanBody) {
	ch := store.EnvSetChange{Delete: plan.Action == envChangeDelete, StateHash: plan.State}
	if ch.Delete {
		users, err := s.setUsers(r.Context(), c.ProjectID, c.SetName)
		if err != nil {
			s.fail(w, err)
			return
		}
		if len(users) > 0 {
			writeCode(w, "conflict", usedByMsg(c.SetName, users))
			return
		}
	} else {
		if !s.envChangeFits(w, r, c.ProjectID, c.SetName, plan) {
			return
		}
		add, err := s.envKeyCreates(c.ProjectID, c.SetName, plan.Add)
		if err != nil {
			s.secretWriteErr(w, err)
			return
		}
		ch.Set, ch.Add, ch.Remove, ch.MakeSecret = plan.Set, add, plan.Remove, plan.MakeSecret
	}
	sealer := func(key, plain string) (store.EnvSecret, error) {
		sealed, err := s.sealValue(c.ProjectID, c.SetName, key, plain)
		if err != nil {
			return store.EnvSecret{}, sealFailure{err}
		}
		return sealed, nil
	}
	set, err := s.Store.ApplyEnvSetChange(r.Context(), c.ProjectID, c.SetName, ch, hash, sealer, p.User.ID, s.now())
	var failed sealFailure
	switch {
	case errors.As(err, &failed):
		s.secretWriteErr(w, failed.err)
		return
	case errors.Is(err, store.ErrNotFound):
		s.confirmationLost(w, p, func() (uuid.UUID, *time.Time, time.Time, error) {
			c, err := s.Store.GetEnvSetConfirmation(r.Context(), hash)
			return c.UserID, c.UsedAt, c.ExpiresAt, err
		})
		return
	case errors.Is(err, store.ErrEnvSetChanged):
		writeCode(w, "conflict", fmt.Sprintf("env set %s changed since the plan; ask for a new one", c.SetName))
		return
	case errors.Is(err, store.ErrConflict) && ch.Delete:
		users, uerr := s.setUsers(r.Context(), c.ProjectID, c.SetName)
		if uerr != nil || len(users) == 0 {
			writeCode(w, "conflict", "conflict")
			return
		}
		writeCode(w, "conflict", usedByMsg(c.SetName, users))
		return
	case errors.Is(err, store.ErrConflict):
		writeCode(w, "conflict", fmt.Sprintf("env set %s changed since the plan; ask for a new one", c.SetName))
		return
	case err != nil:
		s.writeErr(w, err, false)
		return
	}
	// The change is committed: a client disconnect must not drop its audit row.
	auditCtx := context.WithoutCancel(r.Context())
	var payload any
	status := http.StatusOK
	if ch.Delete {
		s.auditOK(auditCtx, p, store.AuditRow{
			ProjectID: idPtr(c.ProjectID),
			Action:    audit.ActionEnvSetDelete,
			Via:       viaKind(p.Session.Kind),
			Detail:    map[string]any{"set": c.SetName},
		})
		payload = map[string]any{"name": c.SetName, "deleted": true}
	} else {
		s.auditOK(auditCtx, p, store.AuditRow{
			ProjectID: idPtr(c.ProjectID),
			Action:    audit.ActionEnvSetUpdate,
			Via:       viaKind(p.Session.Kind),
			Detail:    envChangeAuditDetail(c.SetName, plan),
		})
		if p.Session.Kind == store.KindDelegated {
			payload = namesOnly(set)
		} else {
			filled, ok := s.withPreviews(w, r, c.ProjectID, set)
			if !ok {
				return
			}
			payload = filled
		}
	}
	out := envelopeRaw("ok", "ok", payload)
	s.finishIdempotency(r.Context(), p.User.ID, c.IdempotencyKey, status, out)
	writeJSON(w, status, out)
}

// envChangeAuditDetail names keys per action, never a value.
func envChangeAuditDetail(name string, plan envChangePlanBody) map[string]any {
	set := make([]string, 0, len(plan.Set))
	for key := range plan.Set {
		set = append(set, key)
	}
	sort.Strings(set)
	added := []string{}
	generated := map[string]string{}
	placeholders := []string{}
	plainValues := []string{}
	for _, k := range plan.Add {
		added = append(added, k.Key)
		switch {
		case k.Generate != nil:
			generated[k.Key] = k.Generate.Kind
		case k.Value != nil:
			plainValues = append(plainValues, k.Key)
		default:
			placeholders = append(placeholders, k.Key)
		}
	}
	return map[string]any{
		"set":          name,
		"set_values":   set,
		"added":        added,
		"generated":    generated,
		"placeholders": placeholders,
		"plain_values": plainValues,
		"removed":      nonNilStrings(plan.Remove),
		"made_secret":  nonNilStrings(plan.MakeSecret),
	}
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
