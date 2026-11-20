package memstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

type confirmationRec struct {
	hash []byte
	row  store.CatalogConfirmation
}

func entryKey(projectID uuid.UUID, name string) string {
	return projectID.String() + "/" + name
}

func idemKey(userID uuid.UUID, key string) string {
	return userID.String() + "/" + key
}

func cloneHost(h store.Host) store.Host {
	h.Programs = clonePrograms(h.Programs)
	if h.AccessLevel != nil {
		level := *h.AccessLevel
		h.AccessLevel = &level
	}
	if h.Access != nil {
		cp := append([]store.HostAccess{}, (*h.Access)...)
		h.Access = &cp
	}
	return h
}

func clonePrograms(p *store.HostPrograms) *store.HostPrograms {
	if p == nil {
		return nil
	}
	cp := *p
	cp.RunAs = append([]string(nil), p.RunAs...)
	if p.Projects != nil {
		cp.Projects = append([]string{}, p.Projects...)
	}
	cp.SharedNetworks = store.CloneSharedNetworks(p.SharedNetworks)
	if p.Edge != nil {
		edge := *p.Edge
		cp.Edge = &edge
	}
	return &cp
}

func cloneEntry(e store.CatalogEntry) store.CatalogEntry {
	e.Spec.Args = append([]string(nil), e.Spec.Args...)
	env := make(map[string]string, len(e.Spec.Env))
	for k, v := range e.Spec.Env {
		env[k] = v
	}
	e.Spec.Env = env
	e.Spec.EnvSets = append([]string(nil), e.Spec.EnvSets...)
	secret := make(map[string]string, len(e.Spec.SecretEnv))
	for k, v := range e.Spec.SecretEnv {
		secret[k] = v
	}
	e.Spec.SecretEnv = secret
	e.Spec.Norm()
	if e.MyActions != nil {
		e.MyActions = append([]string(nil), e.MyActions...)
	}
	if e.ImageRaw != nil {
		e.ImageRaw = append([]byte(nil), e.ImageRaw...)
	}
	return e
}

func cloneGrants(in []store.CatalogGrant) []store.CatalogGrant {
	if in == nil {
		return []store.CatalogGrant{}
	}
	out := make([]store.CatalogGrant, len(in))
	for i, g := range in {
		g.Actions = append([]string(nil), g.Actions...)
		out[i] = g
	}
	return out
}

func normWrite(spec store.ProgramSpec) store.ProgramSpec {
	if spec.Args == nil {
		spec.Args = []string{}
	}
	if spec.Env == nil {
		spec.Env = map[string]string{}
	} else {
		env := make(map[string]string, len(spec.Env))
		for k, v := range spec.Env {
			env[k] = v
		}
		spec.Env = env
	}
	spec.Args = append([]string(nil), spec.Args...)
	spec.EnvSets = append([]string(nil), spec.EnvSets...)
	secret := make(map[string]string, len(spec.SecretEnv))
	for k, v := range spec.SecretEnv {
		secret[k] = v
	}
	spec.SecretEnv = secret
	spec.Norm()
	return spec
}

func (m *Mem) CreateCatalogEntry(_ context.Context, projectID uuid.UUID, in store.CatalogWrite) (store.CatalogEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createCatalogEntryLocked(projectID, in)
}

func (m *Mem) createCatalogEntryLocked(projectID uuid.UUID, in store.CatalogWrite) (store.CatalogEntry, error) {
	p, ok := m.projects[projectID]
	if !ok || p.DeletedAt != nil {
		return store.CatalogEntry{}, store.ErrNotFound
	}
	if _, exists := m.entryByName[entryKey(projectID, in.Name)]; exists {
		return store.CatalogEntry{}, store.ErrConflict
	}
	kind := in.Kind
	if kind == "" {
		kind = "program"
	}
	e := store.CatalogEntry{
		ID:             uuid.New(),
		ProjectID:      projectID,
		Name:           in.Name,
		Kind:           kind,
		Spec:           normWrite(in.Spec),
		ImageRaw:       append([]byte(nil), in.ImageRaw...),
		SpecHash:       in.SpecHash,
		CommandDisplay: in.CommandDisplay,
		CreatedBy:      in.Actor,
		CreatedAt:      in.At,
		UpdatedAt:      in.At,
	}
	// syncPrivateEnvLocked fails only before it writes, so an error leaves
	// nothing behind.
	if err := m.syncPrivateEnvLocked(e, in.Private, in.Actor, in.At); err != nil {
		return store.CatalogEntry{}, err
	}
	m.entries[e.ID] = e
	m.entryByName[entryKey(projectID, in.Name)] = e.ID
	admin, viewer := store.DefaultGrantActions(kind)
	m.grants[e.ID] = []store.CatalogGrant{
		{Subject: "role:admin", Actions: admin},
		{Subject: "role:viewer", Actions: viewer},
	}
	return cloneEntry(e), nil
}

func (m *Mem) ReplaceCatalogEntry(_ context.Context, projectID uuid.UUID, name string, in store.CatalogWrite) (string, store.CatalogEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.replaceCatalogEntryLocked(projectID, name, in)
}

func (m *Mem) replaceCatalogEntryLocked(projectID uuid.UUID, name string, in store.CatalogWrite) (string, store.CatalogEntry, error) {
	id, ok := m.entryByName[entryKey(projectID, name)]
	if !ok {
		return "", store.CatalogEntry{}, store.ErrNotFound
	}
	e := m.entries[id]
	old := e.SpecHash
	if e.Kind == "image" {
		e.ImageRaw = append([]byte(nil), in.ImageRaw...)
	} else {
		e.Spec = normWrite(in.Spec)
	}
	if err := m.syncPrivateEnvLocked(e, in.Private, in.Actor, in.At); err != nil {
		return "", store.CatalogEntry{}, err
	}
	e.SpecHash = in.SpecHash
	e.CommandDisplay = in.CommandDisplay
	e.UpdatedAt = in.At
	m.entries[id] = e
	return old, cloneEntry(e), nil
}

func (m *Mem) DeleteCatalogEntry(_ context.Context, projectID uuid.UUID, name string) (store.CatalogEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deleteCatalogEntryLocked(projectID, name)
}

func (m *Mem) deleteCatalogEntryLocked(projectID uuid.UUID, name string) (store.CatalogEntry, error) {
	id, ok := m.entryByName[entryKey(projectID, name)]
	if !ok {
		return store.CatalogEntry{}, store.ErrNotFound
	}
	e := cloneEntry(m.entries[id])
	m.deleteEntryLocked(id)
	return e, nil
}

func (m *Mem) GetCatalogEntry(_ context.Context, projectID uuid.UUID, name string) (store.CatalogEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.entryByName[entryKey(projectID, name)]
	if !ok {
		return store.CatalogEntry{}, store.ErrNotFound
	}
	return cloneEntry(m.entries[id]), nil
}

func (m *Mem) ListCatalogEntries(_ context.Context, projectID uuid.UUID) ([]store.CatalogEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.CatalogEntry{}
	for _, e := range m.entries {
		if e.ProjectID == projectID {
			out = append(out, cloneEntry(e))
		}
	}
	sortEntries(out)
	return out, nil
}

// CatalogLastDeployed mirrors the DB rule (api-v1 v1.21): image deploys and
// allowed program.run audit rows started via console or mcp, the latter at or
// after the entry's created_at.
func (m *Mem) CatalogLastDeployed(_ context.Context, projectID uuid.UUID, entries []store.CatalogEntry) (map[uuid.UUID]time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[uuid.UUID]time.Time{}
	images := map[uuid.UUID]bool{}
	programs := map[string]store.CatalogEntry{}
	for _, e := range entries {
		if e.Kind == "image" {
			images[e.ID] = true
		} else {
			programs[e.Name] = e
		}
	}
	keep := func(id uuid.UUID, at time.Time) {
		if prev, ok := out[id]; !ok || at.After(prev) {
			out[id] = at.UTC()
		}
	}
	for _, row := range m.imageDeploys {
		if images[row.EntryID] && m.entries[row.EntryID].ProjectID == projectID && byPersonOrAI(row.Via) {
			keep(row.EntryID, row.StartedAt)
		}
	}
	for _, a := range m.audits {
		if a.ProjectID == nil || *a.ProjectID != projectID || a.Action != audit.ActionProgramRun ||
			a.Decision != audit.DecisionAllowed || !byPersonOrAI(a.Via) {
			continue
		}
		var detail struct {
			Entry string `json:"entry"`
		}
		if json.Unmarshal(a.Detail, &detail) != nil {
			continue
		}
		if e, ok := programs[detail.Entry]; ok && !a.At.Before(e.CreatedAt) {
			keep(e.ID, a.At)
		}
	}
	return out, nil
}

func byPersonOrAI(via string) bool {
	return via == audit.ViaConsole || via == audit.ViaMCP
}

func sortEntries(out []store.CatalogEntry) {
	for i := 1; i < len(out); i++ {
		j := i
		for j > 0 && (out[j].Name < out[j-1].Name || (out[j].Name == out[j-1].Name && out[j].ID.String() < out[j-1].ID.String())) {
			out[j], out[j-1] = out[j-1], out[j]
			j--
		}
	}
}

func (m *Mem) ListCatalogGrants(_ context.Context, entryID uuid.UUID) ([]store.CatalogGrant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[entryID]; !ok {
		return nil, store.ErrNotFound
	}
	return m.grantsLocked(entryID), nil
}

func (m *Mem) grantsLocked(entryID uuid.UUID) []store.CatalogGrant {
	out := cloneGrants(m.grants[entryID])
	for i := 1; i < len(out); i++ {
		j := i
		for j > 0 && out[j].Subject < out[j-1].Subject {
			out[j], out[j-1] = out[j-1], out[j]
			j--
		}
	}
	return out
}

func (m *Mem) ReplaceCatalogGrants(_ context.Context, projectID uuid.UUID, name string, grants []store.CatalogGrant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.entryByName[entryKey(projectID, name)]
	if !ok {
		return store.ErrNotFound
	}
	m.grants[id] = cloneGrants(grants)
	return nil
}

func (m *Mem) PutConfirmation(_ context.Context, hash []byte, c store.CatalogConfirmation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := hashKey(hash)
	if _, ok := m.confirmations[key]; ok {
		return store.ErrConflict
	}
	m.confirmations[key] = confirmationRec{hash: cloneBytes(hash), row: c}
	return nil
}

func (m *Mem) GetConfirmation(_ context.Context, hash []byte) (store.CatalogConfirmation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.confirmations[hashKey(hash)]
	if !ok || !secret.Equal(hash, rec.hash) {
		return store.CatalogConfirmation{}, store.ErrNotFound
	}
	return rec.row, nil
}

func (m *Mem) MarkConfirmationUsed(_ context.Context, hash []byte, userID uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := hashKey(hash)
	rec, ok := m.confirmations[key]
	if !ok || !secret.Equal(hash, rec.hash) || rec.row.UserID != userID || rec.row.UsedAt != nil || !rec.row.ExpiresAt.After(at) {
		return store.ErrNotFound
	}
	used := at
	rec.row.UsedAt = &used
	m.confirmations[key] = rec
	return nil
}

func (m *Mem) GetIdempotency(_ context.Context, userID uuid.UUID, key string, at time.Time) (store.IdempotencyRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.idempotency[idemKey(userID, key)]
	if !ok {
		return store.IdempotencyRecord{}, store.ErrNotFound
	}
	if !rec.CreatedAt.After(at.Add(-store.IdempotencyTTL)) {
		delete(m.idempotency, idemKey(userID, key))
		return store.IdempotencyRecord{}, store.ErrNotFound
	}
	return copyIdem(rec), nil
}

func (m *Mem) ClaimIdempotency(_ context.Context, userID uuid.UUID, key string, hash []byte, at time.Time) (store.IdempotencyRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := idemKey(userID, key)
	if rec, ok := m.idempotency[k]; ok {
		if !rec.CreatedAt.After(at.Add(-store.IdempotencyTTL)) {
			delete(m.idempotency, k)
		} else {
			out := copyIdem(rec)
			if rec.Pending {
				return out, false, store.ErrPending
			}
			if !secret.Equal(rec.RequestHash, hash) {
				return out, false, store.ErrConflict
			}
			return out, false, nil
		}
	}
	rec := store.IdempotencyRecord{RequestHash: cloneBytes(hash), Pending: true, CreatedAt: at}
	m.idempotency[k] = rec
	return copyIdem(rec), true, nil
}

func copyIdem(rec store.IdempotencyRecord) store.IdempotencyRecord {
	rec.RequestHash = cloneBytes(rec.RequestHash)
	rec.Body = append([]byte(nil), rec.Body...)
	return rec
}

func (m *Mem) PutIdempotency(_ context.Context, userID uuid.UUID, key string, rec store.IdempotencyRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := idemKey(userID, key)
	if _, ok := m.idempotency[k]; ok {
		return store.ErrConflict
	}
	rec.RequestHash = cloneBytes(rec.RequestHash)
	rec.Body = append([]byte(nil), rec.Body...)
	m.idempotency[k] = rec
	return nil
}

func (m *Mem) ReplaceIdempotencyResponse(_ context.Context, userID uuid.UUID, key string, status int, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := idemKey(userID, key)
	rec, ok := m.idempotency[k]
	if !ok {
		return store.ErrNotFound
	}
	rec.Status = status
	rec.Body = append([]byte(nil), body...)
	rec.Pending = false
	m.idempotency[k] = rec
	return nil
}

func (m *Mem) SetHostPrograms(_ context.Context, hostID uuid.UUID, programs *store.HostPrograms) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hosts[hostID]
	if !ok {
		return store.ErrNotFound
	}
	h.Programs = clonePrograms(programs)
	m.hosts[hostID] = h
	return nil
}

// lookupOwnerLocked is lookupCatalogLocked for a service report on hostID:
// the project must also be in the host's org with deploy access (api-v1
// v1.24 item 6).
func (m *Mem) lookupOwnerLocked(hostID uuid.UUID, slug, entry, kind string) (uuid.UUID, bool) {
	id, ok := m.lookupCatalogLocked(slug, entry, kind)
	if !ok {
		return uuid.Nil, false
	}
	h, ok := m.hosts[hostID]
	if !ok || m.projects[id].OrgID != h.OrgID || m.accessLevelLocked(hostID, id) != "deploy" {
		return uuid.Nil, false
	}
	// D-10: without a reported projects allowlist (agents before 0.2.8) a
	// program is placed only for the host's single deploy project.
	if kind == "program" && (h.Programs == nil || h.Programs.Projects == nil) && m.deployProjectsLocked(hostID) != 1 {
		return uuid.Nil, false
	}
	return id, true
}

// deployProjectsLocked counts the live projects with deploy access to the host.
func (m *Mem) deployProjectsLocked(hostID uuid.UUID) int {
	n := 0
	for _, a := range m.access {
		if a.HostID != hostID || a.Level != "deploy" {
			continue
		}
		if p, ok := m.projects[a.ProjectID]; ok && p.DeletedAt == nil {
			n++
		}
	}
	return n
}

func (m *Mem) lookupCatalogLocked(slug, entry, kind string) (uuid.UUID, bool) {
	id, ok := m.bySlug[slug]
	if !ok {
		return uuid.Nil, false
	}
	p, ok := m.projects[id]
	if !ok || p.DeletedAt != nil {
		return uuid.Nil, false
	}
	eid, ok := m.entryByName[entryKey(id, entry)]
	if !ok {
		return uuid.Nil, false
	}
	if m.entries[eid].Kind != kind {
		return uuid.Nil, false
	}
	return id, true
}

func (m *Mem) deleteCatalogLocked(projectID uuid.UUID) {
	var ids []uuid.UUID
	for id, e := range m.entries {
		if e.ProjectID == projectID {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		m.deleteEntryLocked(id)
	}
	for key, rec := range m.catalogWriteConfirmations {
		if rec.row.ProjectID == projectID {
			delete(m.catalogWriteConfirmations, key)
		}
	}
	for key, rec := range m.envSetConfirmations {
		if rec.row.ProjectID == projectID {
			delete(m.envSetConfirmations, key)
		}
	}
}

func (m *Mem) deleteEntryLocked(id uuid.UUID) {
	e, ok := m.entries[id]
	if !ok {
		return
	}
	m.dropPrivateSetLocked(e)
	delete(m.entries, id)
	delete(m.entryByName, entryKey(e.ProjectID, e.Name))
	delete(m.grants, id)
	m.dropEntryEdgeLocked(id)
	m.deleteDeploysLocked(id)
	m.dropEntryProblemsLocked(id)
	for key, rec := range m.confirmations {
		if rec.row.EntryID == id {
			delete(m.confirmations, key)
		}
	}
}
