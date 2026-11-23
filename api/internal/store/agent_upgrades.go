package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Agent upgrades from the console or MCP (api-v1 v1.48, migration 0032,
// spec docs/spec-agent-self-upgrade.md).

// Agent upgrade row states. started and running are open; the rest are
// settled and carry finished_at.
const (
	AgentUpgradeStarted    = "started"
	AgentUpgradeRunning    = "running"
	AgentUpgradeSucceeded  = "succeeded"
	AgentUpgradeRolledBack = "rolled_back"
	AgentUpgradeFailed     = "failed"
	AgentUpgradeUnknown    = "unknown"
)

const (
	// AgentUpgradeStale is how long an open row waits for a result before
	// it becomes unknown (api-v1 v1.48 item 2).
	AgentUpgradeStale = 15 * time.Minute
	// AgentUpgradeHistory is the most rows the history route returns.
	AgentUpgradeHistory = 20
	// MaxAgentUpgradeReason is the longest stored reason token.
	MaxAgentUpgradeReason = 64
)

// AgentUpgradeOpenStates are the states a host result or the sweep may
// settle from: the route moves started to running at the same time.
var AgentUpgradeOpenStates = []string{AgentUpgradeStarted, AgentUpgradeRunning}

// AgentUpgradeOpen reports whether state is started or running.
func AgentUpgradeOpen(state string) bool {
	return state == AgentUpgradeStarted || state == AgentUpgradeRunning
}

// ValidAgentUpgradeState reports whether state is a row state.
func ValidAgentUpgradeState(state string) bool {
	switch state {
	case AgentUpgradeStarted, AgentUpgradeRunning, AgentUpgradeSucceeded,
		AgentUpgradeRolledBack, AgentUpgradeFailed, AgentUpgradeUnknown:
		return true
	}
	return false
}

// AgentUpgrade is one row of app.host_agent_upgrades. StartedByName is
// read from the user on lists; it is not stored on the row.
type AgentUpgrade struct {
	ID            uuid.UUID
	HostID        uuid.UUID
	From          string
	To            string
	State         string
	Reason        string
	StartedBy     uuid.UUID
	StartedByName string
	StartedAt     time.Time
	FinishedAt    *time.Time
}

// AgentUpgradeStore keeps agent upgrade rows (api-v1 v1.48 items 1–4).
type AgentUpgradeStore interface {
	// StartAgentUpgrade inserts a started row for in.HostID (ID, From, To,
	// StartedBy, StartedAt from in). A started or running row for the host
	// already is ErrConflict; a missing host or user is ErrNotFound.
	StartAgentUpgrade(ctx context.Context, in AgentUpgrade) (AgentUpgrade, error)
	// SettleAgentUpgrade moves row id from one of the states in from to the
	// state to with reason; a settled state sets finished_at to at. It
	// reports false, without a write, when the row is in none of them.
	SettleAgentUpgrade(ctx context.Context, id uuid.UUID, from []string, to, reason string, at time.Time) (bool, error)
	// OpenAgentUpgrade is the host's started or running row, or ErrNotFound.
	OpenAgentUpgrade(ctx context.Context, hostID uuid.UUID) (AgentUpgrade, error)
	// ListAgentUpgrades is the host's rows, newest first, at most limit.
	ListAgentUpgrades(ctx context.Context, hostID uuid.UUID, limit int) ([]AgentUpgrade, error)
	// LatestAgentUpgrades is the newest row of each host in hostIDs that
	// has one.
	LatestAgentUpgrades(ctx context.Context, hostIDs []uuid.UUID) (map[uuid.UUID]AgentUpgrade, error)
	// ListStaleAgentUpgrades is every started or running row that started
	// before before, oldest first.
	ListStaleAgentUpgrades(ctx context.Context, before time.Time) ([]AgentUpgrade, error)
}
