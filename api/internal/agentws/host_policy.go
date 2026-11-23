package agentws

import (
	"context"

	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// HostPolicyTimeoutSec is the host_policy_apply task budget
// (agent-protocol v1.22 item 2). The task only checks and stages the
// change; the agent restarts after it answers and the outcome shows in the
// next hello's policy_hash.
const HostPolicyTimeoutSec = 60

// HostPolicyApply asks a live host to run host_policy_apply. service is
// null. spec is {base_hash, edge, images, projects}.
func (h *Hub) HostPolicyApply(ctx context.Context, host store.Host, spec map[string]any) (LiveResult, error) {
	return h.live(ctx, host, "host_policy_apply", nil, spec, HostPolicyTimeoutSec)
}
