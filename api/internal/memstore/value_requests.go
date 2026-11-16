package memstore

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Value requests (api-v1 v1.28). Every method holds m.mu, so a fill and a
// decline of one request never both succeed.

func (m *Mem) CreateValueRequest(_ context.Context, hash []byte, in store.ValueRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !catalog.ValidEnvSetName(in.Set) {
		return store.ErrInvalid
	}
	if err := catalog.ValidateEnvKey(in.Key); err != nil {
		return store.ErrInvalid
	}
	if !m.projectAliveLocked(in.ProjectID) {
		return store.ErrNotFound
	}
	if _, ok := m.envSets[envKey(in.ProjectID, in.Set)]; !ok {
		return store.ErrNotFound
	}
	if _, err := m.keyOpenLocked(in.ProjectID, in.Set, in.Key); err != nil {
		return err
	}
	pruneRequestsLocked(m.valueRequests, in.CreatedAt)
	// At most one open request per user and key (api-v1 v1.28 rev 3): the
	// one this replaces is not counted, and expires only if the create succeeds.
	open, replaced := openRequestsLocked(m.valueRequests, in.UserID, in.CreatedAt, func(r store.ValueRequest) bool {
		return r.ProjectID == in.ProjectID && r.Set == in.Set && r.Key == in.Key
	})
	if open >= store.MaxOpenRequests {
		return store.ErrTooManyRequests
	}
	if m.valueRequests == nil {
		m.valueRequests = map[string]store.ValueRequest{}
	}
	if _, dup := m.valueRequests[hashKey(hash)]; dup {
		return store.ErrConflict
	}
	if replaced != "" {
		r := m.valueRequests[replaced]
		r.ExpiresAt = in.CreatedAt
		m.valueRequests[replaced] = r
	}
	in.State = store.RequestPending
	in.DoneAt = nil
	m.valueRequests[hashKey(hash)] = in
	return nil
}

func (m *Mem) GetValueRequest(_ context.Context, hash []byte) (store.ValueRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.liveRequestLocked(hash)
	if !ok {
		return store.ValueRequest{}, store.ErrNotFound
	}
	return r, nil
}

func (m *Mem) FillValueRequest(_ context.Context, hash []byte, userID uuid.UUID, at time.Time, in store.EnvKeyPut) (store.EnvKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	req, err := m.openRequestLocked(hash, userID, at)
	if err != nil {
		return store.EnvKey{}, err
	}
	if in.Key != req.Key {
		return store.EnvKey{}, store.ErrInvalid
	}
	id := envKey(req.ProjectID, req.Set)
	set, ok := m.envSets[id]
	if !ok {
		return store.EnvKey{}, store.ErrNotFound
	}
	idx, err := m.keyOpenLocked(req.ProjectID, req.Set, req.Key)
	if err != nil {
		return store.EnvKey{}, err
	}
	// A secret key that is not a placeholder is a rotation (v1.33 item 3).
	replaced := idx >= 0 && m.envValues[id][idx].IsSecret && !m.envValues[id][idx].NeedsValue
	// Written secret whatever a placeholder was, as in the Postgres store.
	row := envValueRec{
		Key:           req.Key,
		IsSecret:      true,
		Nonce:         append([]byte(nil), in.Nonce...),
		Ciphertext:    append([]byte(nil), in.Ciphertext...),
		KeyVersion:    in.KeyVersion,
		RevealPending: in.Generated,
		Generated:     in.Generated,
	}
	values := append([]envValueRec(nil), m.envValues[id]...)
	if idx < 0 {
		values = append(values, row)
	} else {
		values[idx] = row
	}
	m.envValues[id] = values
	set.UpdatedAt = at
	set.UpdatedBy = userID
	m.envSets[id] = set
	m.rehashSetLocked(req.ProjectID, req.Set, at)
	done := at
	req.State = store.RequestFilled
	req.DoneAt = &done
	m.valueRequests[hashKey(hash)] = req
	return store.EnvKey{Key: req.Key, IsSecret: true, RevealPending: in.Generated, Generated: in.Generated, Replaced: replaced}, nil
}

func (m *Mem) DeclineValueRequest(_ context.Context, hash []byte, userID uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	req, err := m.openRequestLocked(hash, userID, at)
	if err != nil {
		return err
	}
	done := at
	req.State = store.RequestDeclined
	req.DoneAt = &done
	m.valueRequests[hashKey(hash)] = req
	return nil
}

// SetNeedsValue marks an existing key as a needs_value placeholder. It is
// for store-level tests that need a placeholder without going through the
// v1.27 item 4 create route.
func (m *Mem) SetNeedsValue(projectID uuid.UUID, set, key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	values := m.envValues[envKey(projectID, set)]
	for i := range values {
		if values[i].Key == key {
			values[i].NeedsValue = true
			return true
		}
	}
	return false
}

// dropValueRequestsLocked removes the requests of a deleted set, as the
// Postgres foreign key cascade does (api-v1 v1.28 item 7).
func (m *Mem) dropValueRequestsLocked(projectID uuid.UUID, set string) {
	for k, r := range m.valueRequests {
		if r.ProjectID == projectID && r.Set == set {
			delete(m.valueRequests, k)
		}
	}
}

// liveRequestLocked is a request of a project that is not deleted.
func (m *Mem) liveRequestLocked(hash []byte) (store.ValueRequest, bool) {
	r, ok := m.valueRequests[hashKey(hash)]
	if !ok || !m.projectAliveLocked(r.ProjectID) {
		return store.ValueRequest{}, false
	}
	return r, true
}

// openRequestLocked is a request of userID that is pending and unexpired.
func (m *Mem) openRequestLocked(hash []byte, userID uuid.UUID, at time.Time) (store.ValueRequest, error) {
	req, found := m.valueRequests[hashKey(hash)]
	if err := m.answerableLocked(req.RequestMeta, found, userID, at); err != nil {
		return store.ValueRequest{}, err
	}
	return req, nil
}

// keyOpenLocked returns the key's index in the set, -1 when absent. A plain
// key with a value is ErrKeyExists; a secret key with a value may be
// replaced (api-v1 v1.33 item 3).
func (m *Mem) keyOpenLocked(projectID uuid.UUID, set, key string) (int, error) {
	for i, v := range m.envValues[envKey(projectID, set)] {
		if v.Key != key {
			continue
		}
		if !v.NeedsValue && !v.IsSecret {
			return i, store.ErrKeyExists
		}
		return i, nil
	}
	return -1, nil
}
