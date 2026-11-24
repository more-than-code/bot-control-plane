package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/agentversion"
	"github.com/more-than-code/bot-control-plane/api/internal/agentws"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
)

// Docker usage on demand (api-v1 v1.59 item 2, spec
// docs/spec-disk-usage-breakdown.md §10). Org owners and admins only:
// image and volume names cross projects. Nothing is stored.

const (
	// dockerUsageWait is how long the route waits for the host's answer.
	dockerUsageWait = 50 * time.Second
	// dockerUsageMinAgent is the first bot-agent with the docker_usage task.
	dockerUsageMinAgent = "0.2.17"
)

const (
	msgDockerUsageOld         = "Docker usage needs bot-agent 0.2.17 or later on this host."
	msgDockerUsageDisabled    = "Docker usage needs images or edge turned on in this host's runner.toml"
	msgDockerUsageUnavailable = "Docker is not reachable on the host"
	msgDockerUsageTimeout     = "The host did not answer in time"
	msgDockerUsageFailed      = "The host could not report Docker usage"
	msgDockerUsageBusy        = "A Docker usage check is already running on this host; try again shortly."
)

// dockerUsageView is the 200 payload: the summary and when the API got it.
type dockerUsageView struct {
	agentws.DockerUsageSummary
	At string `json:"at"`
}

// handleDockerUsage is POST /v1/hosts/{id}/docker-usage.
func (s *Server) handleDockerUsage(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.fetchHost(w, r)
	if !ok {
		return
	}
	if _, ok := s.gateHost(w, r, p, host, authz.CapHostAdmin, nil, nil, audit.ActionHostDockerUsage); !ok {
		return
	}
	if host.RevokedAt != nil {
		writeCode(w, "not_found", "not found")
		return
	}
	record := func(ctx context.Context, decision, code string) {
		row := hostAuditRow(host, nil, audit.ActionHostDockerUsage)
		row.Decision = decision
		row.Detail = map[string]any{"host": host.Name}
		if code != "" {
			row.Result = &code
			row.ErrorCode = &code
		} else {
			done := "succeeded"
			row.Result = &done
		}
		s.auditOK(ctx, p, row)
	}
	answer := func(status int, code, msg string) {
		record(r.Context(), audit.DecisionDenied, code)
		write(w, status, code, msg, nil)
	}
	switch {
	case s.hostStatus(host) != "online":
		answer(statusFor("host_offline"), "host_offline", phrase("unavailable"))
		return
	case dockerUsageAgentOld(host.AgentVersion):
		answer(http.StatusConflict, "agent_too_old", msgDockerUsageOld)
		return
	case s.Hub == nil:
		writeCode(w, "error", "internal error")
		return
	}
	// One check per host at a time (spec §8): system/df can take seconds.
	if _, running := s.dockerUsageRuns.LoadOrStore(host.ID, struct{}{}); running {
		answer(http.StatusConflict, "busy", msgDockerUsageBusy)
		return
	}
	defer s.dockerUsageRuns.Delete(host.ID)
	ctx, cancel := context.WithTimeout(r.Context(), s.dockerUsageWaitFor())
	sum, err := s.Hub.DockerUsage(ctx, host)
	cancel()
	// The caller may have left during the wait: the audit row is still written.
	after := context.WithoutCancel(r.Context())
	if err != nil {
		status, code, msg := dockerUsageFailure(err)
		record(after, audit.DecisionAllowed, code)
		write(w, status, code, msg, nil)
		return
	}
	record(after, audit.DecisionAllowed, "")
	writeOK(w, http.StatusOK, dockerUsageView{DockerUsageSummary: sum, At: s.now().UTC().Format(time.RFC3339)})
}

func (s *Server) dockerUsageWaitFor() time.Duration {
	if s.dockerUsageWaitTest > 0 {
		return s.dockerUsageWaitTest
	}
	return dockerUsageWait
}

// dockerUsageAgentOld reports whether version is before 0.2.17. A dev
// build's fourth part (0.2.17.1) is ignored; a version that does not
// parse counts as old.
func dockerUsageAgentOld(version string) bool {
	if parts := strings.Split(version, "."); len(parts) == 4 && agentversion.Valid(parts[3]+".0.0") {
		version = strings.Join(parts[:3], ".")
	}
	if !agentversion.Valid(version) {
		return true
	}
	return agentversion.Older(version, dockerUsageMinAgent)
}

// dockerUsageFailure maps a docker_usage error to status, code and a fixed
// message. The agent's own text never reaches the caller.
func dockerUsageFailure(err error) (int, string, string) {
	var ce *agentws.CallError
	if !errors.As(err, &ce) {
		return http.StatusInternalServerError, "error", "internal error"
	}
	switch ce.Agent {
	case agentws.DockerUsageDisabled:
		return http.StatusConflict, agentws.DockerUsageDisabled, msgDockerUsageDisabled
	case agentws.DockerUsageUnavailable:
		return http.StatusBadGateway, agentws.DockerUsageUnavailable, msgDockerUsageUnavailable
	case agentws.DockerUsageTimeout:
		return http.StatusGatewayTimeout, "timeout", msgDockerUsageTimeout
	case "busy":
		// The agent refused: it is already running a task like this one.
		return http.StatusConflict, "busy", msgDockerUsageBusy
	case "":
		if ce.Code == "unavailable" {
			// The socket went away before an answer.
			return statusFor("host_offline"), "host_offline", phrase("unavailable")
		}
		return http.StatusBadGateway, "bad_gateway", msgDockerUsageFailed
	default:
		return http.StatusBadGateway, "bad_gateway", msgDockerUsageFailed
	}
}
