package memstore

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Env set creation with key names only (api-v1 v1.27 item 4).

type envSetConfirmationRec struct {
	hash []byte
	row  store.EnvSetConfirmation
}

func cloneEnvSetConfirmation(c store.EnvSetConfirmation) store.EnvSetConfirmation {
	c.Body = cloneBytes(c.Body)
	c.RequestHash = cloneBytes(c.RequestHash)
	if c.UsedAt != nil {
		at := *c.UsedAt
		c.UsedAt = &at
	}
	return c
}

func (m *Mem) PutEnvSetConfirmation(_ context.Context, hash []byte, c store.EnvSetConfirmation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, rec := range m.envSetConfirmations {
		if rec.row.ExpiresAt.Before(c.CreatedAt) {
			delete(m.envSetConfirmations, key)
		}
	}
	key := hashKey(hash)
	if _, ok := m.envSetConfirmations[key]; ok {
		return store.ErrConflict
	}
	m.envSetConfirmations[key] = envSetConfirmationRec{hash: cloneBytes(hash), row: cloneEnvSetConfirmation(c)}
	return nil
}

func (m *Mem) GetEnvSetConfirmation(_ context.Context, hash []byte) (store.EnvSetConfirmation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.envSetConfirmations[hashKey(hash)]
	if !ok || !secret.Equal(hash, rec.hash) {
		return store.EnvSetConfirmation{}, store.ErrNotFound
	}
	return cloneEnvSetConfirmation(rec.row), nil
}

// CreateEnvSetNames holds the mutex across the plan check, the create, and
// the plan mark, so a conflict leaves the plan unused.
func (m *Mem) CreateEnvSetNames(_ context.Context, projectID uuid.UUID, name string, keys []store.EnvKeyCreate, confirm []byte, actor uuid.UUID, at time.Time) (store.EnvSet, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.projectAliveLocked(projectID) {
		return store.EnvSet{}, store.ErrNotFound
	}
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
	id := envKey(projectID, name)
	if _, exists := m.envSets[id]; exists {
		return store.EnvSet{}, store.ErrConflict
	}
	values := make([]envValueRec, 0, len(keys))
	seen := map[string]bool{}
	for _, k := range keys {
		if err := catalog.ValidateEnvKey(k.Key); err != nil || seen[k.Key] {
			return store.EnvSet{}, store.ErrInvalid
		}
		seen[k.Key] = true
		row := envValueRec{Key: k.Key, IsSecret: k.Secret || k.Generated, NeedsValue: !k.Generated}
		if !row.IsSecret && k.Value != nil {
			// A plain key with its value (api-v1 v1.32 item 2).
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
		values = append(values, row)
	}
	set := envSetRec{Name: name, UpdatedAt: at, UpdatedBy: actor}
	m.envSets[id] = set
	m.envValues[id] = values
	if planKey != "" {
		rec := m.envSetConfirmations[planKey]
		used := at
		rec.row.UsedAt = &used
		m.envSetConfirmations[planKey] = rec
	}
	m.rehashSetLocked(projectID, name, at)
	return m.envSetViewLocked(projectID, set), nil
}
