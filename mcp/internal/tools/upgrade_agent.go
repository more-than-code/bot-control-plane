package tools

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
	"github.com/more-than-code/bot-control-plane/mcp/internal/auth"
)

// upgrade_agent (api-v1 v1.48 item 8). The API accepts the delegated
// upgrade route directly, so the confirmation lives here: upgrade_agent
// reads the host and keeps a plan in memory; confirm with that id and key
// posts POST /v1/hosts/{id}/agent-upgrade with the version the host read
// named as latest. A restart of the MCP server drops pending plans.

const upgradeAgentSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["host_id", "idempotency_key"],
  "properties": {
    "host_id": {"type": "string", "format": "uuid"},
    "idempotency_key": {"type": "string", "pattern": "^[A-Za-z0-9._:-]{1,128}$"}
  }
}`

const (
	// upgradePlanTTL is how long an upgrade plan waits for confirm.
	upgradePlanTTL = 10 * time.Minute
	// maxUpgradePlans caps the pending plans kept in memory.
	maxUpgradePlans = 1000
	// maxUpgradePlansPerCaller caps one caller's pending plans.
	maxUpgradePlansPerCaller = 10
)

const msgUpgradeOff = "this host does not allow upgrades from Deploybot: its administrator sets agent_upgrade = true in runner.toml (bot-agent 0.2.15 or later)"

type upgradePlan struct {
	// caller is the digest of the caller's token (auth.CallerKeyFromContext):
	// only that caller can confirm the plan.
	caller  [32]byte
	hostID  string
	host    string
	from    string
	version string
	key     string
	expires time.Time
}

type upgradePlans struct {
	mu    sync.Mutex
	plans map[string]upgradePlan
	now   func() time.Time
}

var pendingUpgrades = &upgradePlans{plans: map[string]upgradePlan{}, now: time.Now}

// add stores p under a new 43-character id. Expired plans are dropped
// first; a full store, or a caller with maxUpgradePlansPerCaller pending
// plans, refuses.
func (u *upgradePlans) add(p upgradePlan) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(raw[:])
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	for k, v := range u.plans {
		if !now.Before(v.expires) {
			delete(u.plans, k)
		}
	}
	if len(u.plans) >= maxUpgradePlans {
		return "", errors.New("too many pending upgrade confirmations; try again later")
	}
	mine := 0
	for _, v := range u.plans {
		if v.caller == p.caller {
			mine++
		}
	}
	if mine >= maxUpgradePlansPerCaller {
		return "", errors.New("too many pending upgrade confirmations for you; confirm or let some expire (10 minutes)")
	}
	p.expires = now.Add(upgradePlanTTL)
	u.plans[id] = p
	return id, nil
}

// take returns the plan for id. found is false for an id this server did
// not issue and for another caller's plan, which is left alone. A key
// mismatch keeps the plan; an expired plan is dropped.
func (u *upgradePlans) take(caller [32]byte, id, key string) (p upgradePlan, found bool, fail *mcp.CallToolResult) {
	u.mu.Lock()
	defer u.mu.Unlock()
	p, found = u.plans[id]
	if !found || p.caller != caller {
		return upgradePlan{}, false, nil
	}
	if p.key != key {
		return upgradePlan{}, true, toolError("conflict", "idempotency_key does not match the request that issued this confirmation")
	}
	delete(u.plans, id)
	if !u.now().Before(p.expires) {
		return upgradePlan{}, true, toolError("conflict", "this confirmation expired; call the tool again for a new one")
	}
	return p, true, nil
}

func registerUpgradeAgent(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name:        "upgrade_agent",
		Description: "Plan an upgrade of a host's bot-agent to the latest release (latest_agent_version in list_hosts). Organization owners and admins only. Nothing runs until confirm, and this call does not contact the host: show the plan to the user, and after they agree call confirm with the returned confirmation_id and the same idempotency_key. The host must allow it (agent_upgrade_allowed in list_hosts; its administrator sets agent_upgrade = true in runner.toml) and be online. On confirm the host downloads the signed release from GitHub, swaps and restarts its agent, and puts the previous agent back by itself if the new one does not come back. The result arrives later: list_hosts shows agent_upgrade with state running, then succeeded, rolled_back, failed or unknown.",
		Annotations: mutatingAnnotations(false),
		InputSchema: json.RawMessage(upgradeAgentSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			HostID         string `json:"host_id"`
			IdempotencyKey string `json:"idempotency_key"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := requireUUID("host_id", in.HostID); err != nil {
			return badRequest(err)
		}
		if err := requireIdempotencyKey(in.IdempotencyKey); err != nil {
			return badRequest(err)
		}
		caller, ok := auth.CallerKeyFromContext(ctx)
		if !ok {
			return toolError("unauthorized", "missing caller")
		}
		payload, fail := get(ctx, api, idPath("v1", "hosts", in.HostID), nil)
		if fail != nil {
			return fail
		}
		var host struct {
			Name                string  `json:"name"`
			AgentVersion        string  `json:"agent_version"`
			LatestAgentVersion  *string `json:"latest_agent_version"`
			UpdateAvailable     bool    `json:"update_available"`
			AgentUpgradeAllowed bool    `json:"agent_upgrade_allowed"`
			AgentUpgrade        *struct {
				State string `json:"state"`
			} `json:"agent_upgrade"`
		}
		if err := json.Unmarshal(payload, &host); err != nil {
			return toolError("error", "host response was not usable")
		}
		switch {
		case host.LatestAgentVersion == nil || !versionPattern.MatchString(*host.LatestAgentVersion):
			return toolError("bad_request", "the latest bot-agent version is not known to Deploybot")
		case !host.UpdateAvailable:
			return toolError("bad_request", "this host already runs the latest bot-agent")
		case !host.AgentUpgradeAllowed:
			return toolError("unsupported_agent", msgUpgradeOff)
		case host.AgentUpgrade != nil && (host.AgentUpgrade.State == "started" || host.AgentUpgrade.State == "running"):
			return toolError("conflict", "an upgrade of this host is already running")
		}
		// Whether the caller is an org owner or admin is not visible on a
		// delegated host read (the API omits access); the API decides at confirm.
		plan := upgradePlan{caller: caller, hostID: in.HostID, host: host.Name, from: host.AgentVersion, version: *host.LatestAgentVersion, key: in.IdempotencyKey}
		id, err := pendingUpgrades.add(plan)
		if err != nil {
			return toolError("unavailable", err.Error())
		}
		text := "Upgrade bot-agent on " + summaryField(plan.host) + " from " + summaryField(plan.from) + " to " + plan.version +
			". The host downloads the signed release from GitHub, restarts its agent, and puts the previous agent back by itself if the new one does not come back. Ask the user, then call confirm with this confirmation_id and the same idempotency_key."
		out, _ := json.Marshal(map[string]any{
			"confirmation_id": id,
			"plan":            text,
			"expires_at":      pendingUpgrades.now().Add(upgradePlanTTL).UTC().Format(time.RFC3339),
		})
		return finishConfirmation(out, nil)
	})
}

// confirmUpgrade performs a pending upgrade_agent plan. handled is false
// when id is not one of this server's plans, so confirm asks the API.
func confirmUpgrade(ctx context.Context, api *apiclient.Client, id, key string) (res *mcp.CallToolResult, handled bool) {
	caller, ok := auth.CallerKeyFromContext(ctx)
	if !ok {
		return nil, false
	}
	plan, found, fail := pendingUpgrades.take(caller, id, key)
	if !found {
		return nil, false
	}
	if fail != nil {
		return fail, true
	}
	extendWriteBudget(ctx, apiclient.MaxCallTimeout)
	status, payload, fail, deadline := postWithin(ctx, api, idPath("v1", "hosts", plan.hostID, "agent-upgrade"), map[string]string{"version": plan.version}, apiclient.MaxCallTimeout)
	if deadline {
		return toolError("error", "the host has not answered yet; the upgrade may still start. list_hosts shows agent_upgrade for this host in a few minutes"), true
	}
	if fail != nil {
		return fail, true
	}
	var body struct {
		UpgradeID string `json:"upgrade_id"`
		State     string `json:"state"`
	}
	if status != http.StatusAccepted || json.Unmarshal(payload, &body) != nil || requireUUID("upgrade_id", body.UpgradeID) != nil {
		return toolError("error", "upgrade response was not usable"), true
	}
	out, _ := json.Marshal(map[string]any{"upgrade_id": body.UpgradeID, "state": "running", "host_id": plan.hostID, "to": plan.version})
	return present(out, "upgrade of bot-agent on "+summaryField(plan.host)+" to "+plan.version+" started. The host reports back when the new agent connects, or puts the old agent back by itself; list_hosts shows agent_upgrade (running, then succeeded, rolled_back, failed or unknown)."), true
}
