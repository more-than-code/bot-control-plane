package memstore

import (
	"bytes"
	"context"
	"encoding/hex"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// api-v1 v1.33 in memory, with the Postgres store's rules: entry-private env
// sets (item 6), confirmed env set changes (items 1 and 2) and the
// open-requests list (item 8).

// envValueFromCreate is one new value as insertEnvCreateValue stores it.
func envValueFromCreate(k store.EnvKeyCreate) envValueRec {
	row := envValueRec{Key: k.Key, IsSecret: k.Secret || k.Generated, NeedsValue: !k.Generated}
	if !row.IsSecret && k.Value != nil {
		row.Plain = *k.Value
		row.NeedsValue = false
	}
	if row.IsSecret {
		row.Nonce = cloneBytes(k.Nonce)
		row.Ciphertext = cloneBytes(k.Ciphertext)
		row.KeyVersion = k.KeyVersion
		row.RevealPending = k.Generated
		row.Generated = k.Generated
	}
	return row
}

// syncPrivateEnvLocked is syncPrivateEnv of the Postgres store.
func (m *Mem) syncPrivateEnvLocked(e store.CatalogEntry, p *store.PrivateEnv, actor uuid.UUID, at time.Time) error {
	if p == nil {
		return nil
	}
	name := catalog.PrivateSet(e.Name)
	id := envKey(e.ProjectID, name)
	set, exists := m.envSets[id]
	if exists && (set.EntryID == nil || *set.EntryID != e.ID) {
		return store.ErrConflict
	}
	if len(p.Keys) == 0 {
		if exists {
			m.dropEnvSetLocked(e.ProjectID, name)
		}
		return nil
	}
	if !exists {
		entryID := e.ID
		set = envSetRec{Name: name, UpdatedAt: at, UpdatedBy: actor, EntryID: &entryID}
	}
	keep := map[string]bool{}
	for _, k := range p.Keys {
		keep[k] = true
	}
	changed := !exists
	values := []envValueRec{}
	have := map[string]bool{}
	for _, v := range m.envValues[id] {
		if !keep[v.Key] {
			changed = true
			continue
		}
		have[v.Key] = true
		values = append(values, v)
	}
	for _, k := range p.Add {
		if have[k.Key] {
			continue
		}
		k.Secret = true
		k.Value = nil
		values = append(values, envValueFromCreate(k))
		have[k.Key] = true
		changed = true
	}
	if changed {
		set.UpdatedAt = at
		set.UpdatedBy = actor
	}
	m.envSets[id] = set
	m.envValues[id] = values
	return nil
}

// dropEnvSetLocked removes a set, its values and its value requests, as the
// Postgres cascades do.
func (m *Mem) dropEnvSetLocked(projectID uuid.UUID, name string) {
	delete(m.envSets, envKey(projectID, name))
	delete(m.envValues, envKey(projectID, name))
	m.dropValueRequestsLocked(projectID, name)
}

// dropPrivateSetLocked deletes the private set of an entry being deleted.
func (m *Mem) dropPrivateSetLocked(e store.CatalogEntry) {
	name := catalog.PrivateSet(e.Name)
	if set, ok := m.envSets[envKey(e.ProjectID, name)]; ok && set.EntryID != nil && *set.EntryID == e.ID {
		m.dropEnvSetLocked(e.ProjectID, name)
	}
}

// ApplyEnvSetChange implements store.Store.ApplyEnvSetChange.
func (m *Mem) ApplyEnvSetChange(_ context.Context, projectID uuid.UUID, name string, ch store.EnvSetChange, confirm []byte, seal store.EnvSealer, actor uuid.UUID, at time.Time) (store.EnvSet, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !catalog.ValidName(name) {
		return store.EnvSet{}, store.ErrInvalid
	}
	planKey := ""
	if confirm != nil {
		planKey = hashKey(confirm)
		rec, ok := m.envSetConfirmations[planKey]
		if !ok || !secret.Equal(confirm, rec.hash) || rec.row.UserID != actor || rec.row.ProjectID != projectID ||
			rec.row.SetName != name || rec.row.UsedAt != nil || !rec.row.ExpiresAt.After(at) {
			return store.EnvSet{}, store.ErrNotFound
		}
	}
	if !m.projectAliveLocked(projectID) {
		return store.EnvSet{}, store.ErrNotFound
	}
	id := envKey(projectID, name)
	set, ok := m.envSets[id]
	if !ok {
		return store.EnvSet{}, store.ErrNotFound
	}
	if ch.StateHash != "" && store.EnvStateHash(m.envMaterialLocked(id)) != ch.StateHash {
		return store.EnvSet{}, store.ErrEnvSetChanged
	}
	var out store.EnvSet
	if ch.Delete {
		if m.setReferencedLocked(projectID, name) {
			return store.EnvSet{}, store.ErrConflict
		}
		m.dropEnvSetLocked(projectID, name)
		out = store.EnvSet{Name: name}
	} else {
		values, err := applyEnvKeyChangesLocked(m.envValues[id], ch, seal)
		if err != nil {
			return store.EnvSet{}, err
		}
		m.envValues[id] = values
		set.UpdatedAt = at
		set.UpdatedBy = actor
		m.envSets[id] = set
		m.rehashSetLocked(projectID, name, at)
		out = m.envSetViewLocked(projectID, set)
	}
	if planKey != "" {
		rec := m.envSetConfirmations[planKey]
		used := at
		rec.row.UsedAt = &used
		m.envSetConfirmations[planKey] = rec
	}
	return out, nil
}

// applyEnvKeyChangesLocked returns the set's new values, or ErrConflict when
// a key no longer fits its action. The input slice is not changed.
func applyEnvKeyChangesLocked(in []envValueRec, ch store.EnvSetChange, seal store.EnvSealer) ([]envValueRec, error) {
	index := map[string]int{}
	for i, v := range in {
		index[v.Key] = i
	}
	for key := range ch.Set {
		i, ok := index[key]
		if !ok || in[i].IsSecret {
			return nil, store.ErrConflict
		}
	}
	for _, k := range ch.Add {
		if _, ok := index[k.Key]; ok {
			return nil, store.ErrConflict
		}
	}
	for _, key := range append(append([]string{}, ch.Remove...), ch.MakeSecret...) {
		if _, ok := index[key]; !ok {
			return nil, store.ErrConflict
		}
	}
	values := append([]envValueRec(nil), in...)
	for key, value := range ch.Set {
		values[index[key]] = envValueRec{Key: key, Plain: value}
	}
	for _, key := range ch.MakeSecret {
		v := values[index[key]]
		if v.IsSecret {
			continue
		}
		sealed, err := seal(key, v.Plain)
		if err != nil {
			return nil, err
		}
		values[index[key]] = envValueRec{
			Key: key, IsSecret: true, Nonce: cloneBytes(sealed.Nonce), Ciphertext: cloneBytes(sealed.Ciphertext),
			KeyVersion: sealed.KeyVersion, NeedsValue: v.NeedsValue,
		}
	}
	remove := map[string]bool{}
	for _, key := range ch.Remove {
		remove[key] = true
	}
	out := make([]envValueRec, 0, len(values)+len(ch.Add))
	for _, v := range values {
		if !remove[v.Key] {
			out = append(out, v)
		}
	}
	for _, k := range ch.Add {
		out = append(out, envValueFromCreate(k))
	}
	return out, nil
}

// ListOpenRequests implements store.Store.ListOpenRequests.
func (m *Mem) ListOpenRequests(_ context.Context, userID uuid.UUID, at time.Time, max int) ([]store.OpenRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.OpenRequest{}
	add := func(kind string, key string, meta store.RequestMeta, subject string) {
		if meta.UserID != userID || meta.StateAt(at) != store.RequestPending || !m.projectAliveLocked(meta.ProjectID) {
			return
		}
		hash, err := hex.DecodeString(key)
		if err != nil {
			return
		}
		out = append(out, store.OpenRequest{
			Type: kind, Hash: hash, ProjectID: meta.ProjectID, ProjectName: m.projects[meta.ProjectID].Name,
			Subject: subject, CreatedAt: meta.CreatedAt, ExpiresAt: meta.ExpiresAt,
		})
	}
	for key, r := range m.valueRequests {
		add(store.RequestTypeValue, key, r.RequestMeta, r.Set+"."+r.Key)
	}
	for key, r := range m.fileRequests {
		add(store.RequestTypeFile, key, r.RequestMeta, r.Name)
	}
	for key, r := range m.connectRequests {
		add(store.RequestTypeConnect, key, r.RequestMeta, "ghcr.io")
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return bytes.Compare(out[i].Hash, out[j].Hash) < 0
	})
	if len(out) > max {
		out = out[:max]
	}
	return out, nil
}
