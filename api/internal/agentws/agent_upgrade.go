package agentws

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/agentversion"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Agent upgrade from the server (agent-protocol v1.20, api-v1 v1.48).

// AgentUpgradeTimeoutSec is the agent_upgrade task budget (agent-protocol
// v1.20 item 4). The task only checks the request and starts the upgrade
// unit; the upgrade itself reports through hello or agent_upgrade_result.
const AgentUpgradeTimeoutSec = 120

// AgentUpgrade asks a live host to run agent_upgrade for version. service
// is null. Nothing but the version goes into the spec.
func (h *Hub) AgentUpgrade(ctx context.Context, host store.Host, version string) (LiveResult, error) {
	return h.live(ctx, host, "agent_upgrade", nil, map[string]any{"version": version}, AgentUpgradeTimeoutSec)
}

// UpgradeResult is hello last_upgrade or the result of an
// agent_upgrade_result frame (agent-protocol v1.20 items 2 and 3).
type UpgradeResult struct {
	Version string
	Outcome string
	Reason  string
	At      time.Time
}

// UpgradeHook settles the host's open upgrade row. agentVersion is the
// hello agent_version, or "" for an agent_upgrade_result frame. res is nil
// when the agent sent no result.
type UpgradeHook func(ctx context.Context, hostID uuid.UUID, agentVersion string, res *UpgradeResult)

// SetUpgradeHook sets the function that settles agent upgrade rows. It is
// called on the read loop after each hello and each agent_upgrade_result
// frame.
func (h *Hub) SetUpgradeHook(fn UpgradeHook) {
	h.mu.Lock()
	h.upgradeHook = fn
	h.mu.Unlock()
}

func (h *Hub) notifyUpgrade(ctx context.Context, hostID uuid.UUID, agentVersion string, res *UpgradeResult) {
	h.mu.Lock()
	fn := h.upgradeHook
	h.mu.Unlock()
	if fn != nil {
		fn(ctx, hostID, agentVersion, res)
	}
}

var upgradeReasonRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// parseUpgradeResult reads a last_upgrade object. Absent or null is nil
// and ok. Anything that is not the v1.20 shape is not ok: the caller drops
// it and keeps the socket. An unknown reason token is kept only when it is
// a plain token; anything else becomes "".
func parseUpgradeResult(raw json.RawMessage) (*UpgradeResult, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true
	}
	var wire struct {
		Version string `json:"version"`
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
		At      string `json:"at"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil || !agentversion.Valid(wire.Version) {
		return nil, false
	}
	switch wire.Outcome {
	case store.AgentUpgradeSucceeded, store.AgentUpgradeRolledBack, store.AgentUpgradeFailed:
	default:
		return nil, false
	}
	at, err := time.Parse(time.RFC3339, wire.At)
	if err != nil {
		return nil, false
	}
	res := &UpgradeResult{Version: wire.Version, Outcome: wire.Outcome, At: at.UTC()}
	if upgradeReasonRe.MatchString(wire.Reason) {
		res.Reason = wire.Reason
	}
	return res, true
}

// helloUpgrade passes hello last_upgrade and agent_version to the hook. A
// malformed last_upgrade is dropped with one log line; the version alone
// can still settle a row.
func (h *Hub) helloUpgrade(ac *agentConn, agentVersion string, raw json.RawMessage) {
	res, ok := parseUpgradeResult(raw)
	if !ok {
		h.log.Warn("bad last_upgrade", "host_id", ac.hostID.String())
	}
	h.notifyUpgrade(ac.parent, ac.hostID, agentVersion, res)
}

// onUpgradeResult handles an agent_upgrade_result frame, at most one a
// second per connection. A malformed frame is dropped with one log line;
// the socket stays up.
func (h *Hub) onUpgradeResult(ac *agentConn, f frame) {
	now := h.now()
	if !ac.upgradeAt.IsZero() && now.Sub(ac.upgradeAt) < time.Second {
		return
	}
	ac.upgradeAt = now
	var wire struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(f.Raw, &wire); err != nil {
		h.log.Warn("bad agent_upgrade_result", "host_id", ac.hostID.String())
		return
	}
	res, ok := parseUpgradeResult(wire.Result)
	if !ok || res == nil {
		h.log.Warn("bad agent_upgrade_result", "host_id", ac.hostID.String())
		return
	}
	h.notifyUpgrade(ac.parent, ac.hostID, "", res)
}
