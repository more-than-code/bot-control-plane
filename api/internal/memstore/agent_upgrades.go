package memstore

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Agent upgrades (api-v1 v1.48), as db/agent_upgrades.go.

func (m *Mem) StartAgentUpgrade(_ context.Context, in store.AgentUpgrade) (store.AgentUpgrade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hosts[in.HostID]; !ok {
		return store.AgentUpgrade{}, store.ErrNotFound
	}
	if _, ok := m.users[in.StartedBy]; !ok {
		return store.AgentUpgrade{}, store.ErrNotFound
	}
	for _, r := range m.agentUpgrades {
		if r.HostID == in.HostID && store.AgentUpgradeOpen(r.State) {
			return store.AgentUpgrade{}, store.ErrConflict
		}
	}
	row := store.AgentUpgrade{
		ID: in.ID, HostID: in.HostID, From: in.From, To: in.To, State: store.AgentUpgradeStarted,
		StartedBy: in.StartedBy, StartedAt: in.StartedAt,
	}
	m.agentUpgrades = append(m.agentUpgrades, row)
	return m.withNameLocked(row), nil
}

func (m *Mem) SettleAgentUpgrade(_ context.Context, id uuid.UUID, from []string, to, reason string, at time.Time) (bool, error) {
	if !store.ValidAgentUpgradeState(to) || len(reason) > store.MaxAgentUpgradeReason {
		return false, store.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.agentUpgrades {
		r := &m.agentUpgrades[i]
		if r.ID != id {
			continue
		}
		if !slices.Contains(from, r.State) {
			return false, nil
		}
		r.State, r.Reason = to, reason
		if !store.AgentUpgradeOpen(to) {
			t := at
			r.FinishedAt = &t
		}
		return true, nil
	}
	return false, nil
}

func (m *Mem) OpenAgentUpgrade(_ context.Context, hostID uuid.UUID) (store.AgentUpgrade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.agentUpgrades) - 1; i >= 0; i-- {
		if r := m.agentUpgrades[i]; r.HostID == hostID && store.AgentUpgradeOpen(r.State) {
			return m.withNameLocked(r), nil
		}
	}
	return store.AgentUpgrade{}, store.ErrNotFound
}

func (m *Mem) ListAgentUpgrades(_ context.Context, hostID uuid.UUID, limit int) ([]store.AgentUpgrade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.AgentUpgrade{}
	for _, r := range m.newestFirstLocked() {
		if r.HostID == hostID && len(out) < limit {
			out = append(out, m.withNameLocked(r))
		}
	}
	return out, nil
}

func (m *Mem) LatestAgentUpgrades(_ context.Context, hostIDs []uuid.UUID) (map[uuid.UUID]store.AgentUpgrade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[uuid.UUID]bool{}
	for _, id := range hostIDs {
		want[id] = true
	}
	out := map[uuid.UUID]store.AgentUpgrade{}
	for _, r := range m.newestFirstLocked() {
		if _, seen := out[r.HostID]; want[r.HostID] && !seen {
			out[r.HostID] = m.withNameLocked(r)
		}
	}
	return out, nil
}

func (m *Mem) ListStaleAgentUpgrades(_ context.Context, before time.Time) ([]store.AgentUpgrade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []store.AgentUpgrade{}
	for _, r := range m.agentUpgrades {
		if store.AgentUpgradeOpen(r.State) && r.StartedAt.Before(before) {
			out = append(out, m.withNameLocked(r))
		}
	}
	return out, nil
}

// newestFirstLocked is the rows by started_at descending, then id.
func (m *Mem) newestFirstLocked() []store.AgentUpgrade {
	rows := append([]store.AgentUpgrade(nil), m.agentUpgrades...)
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && newerUpgrade(rows[j], rows[j-1]); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
	return rows
}

func newerUpgrade(a, b store.AgentUpgrade) bool {
	if !a.StartedAt.Equal(b.StartedAt) {
		return a.StartedAt.After(b.StartedAt)
	}
	return a.ID.String() > b.ID.String()
}

func (m *Mem) withNameLocked(r store.AgentUpgrade) store.AgentUpgrade {
	r.StartedByName = m.users[r.StartedBy].Name
	return r
}
