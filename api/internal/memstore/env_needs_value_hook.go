package memstore

import (
	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// SetEnvNeedsValue marks one stored key as a needs_value placeholder and
// leaves its value as it is. It is not part of store.Store: API tests use
// it to stand in for a placeholder that appears after a plan was issued
// (api-v1 v1.27 item 6 at confirm). A missing set or key is ErrNotFound.
func (m *Mem) SetEnvNeedsValue(projectID uuid.UUID, set, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := envKey(projectID, set)
	values := m.envValues[id]
	for i := range values {
		if values[i].Key == key {
			next := append([]envValueRec(nil), values...)
			next[i].NeedsValue = true
			m.envValues[id] = next
			return nil
		}
	}
	return store.ErrNotFound
}
