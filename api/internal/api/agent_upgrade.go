package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/agentversion"
	"github.com/more-than-code/bot-control-plane/api/internal/agentws"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Agent upgrades from the console or MCP (api-v1 v1.48, spec
// docs/spec-agent-self-upgrade.md). The route only names the version the
// API knows as latest; the host decides the rest and rolls back by itself.

const (
	// agentUpgradeWait is how long the route waits for the task answer:
	// the agent's budget plus a margin.
	agentUpgradeWait = time.Duration(agentws.AgentUpgradeTimeoutSec)*time.Second + 10*time.Second
	// agentUpgradeRecordTimeout bounds the writes after the task returns
	// and each settle.
	agentUpgradeRecordTimeout = 10 * time.Second
	// agentUpgradeResultSkew: a host result older than this before the
	// row started belongs to an earlier upgrade to the same version.
	agentUpgradeResultSkew = 30 * time.Second
	// agentUpgradeFutureLimit: a host result dated further ahead of the API
	// clock than this is ignored (review L2).
	agentUpgradeFutureLimit = 5 * time.Minute
	// agentUpgradeSweepEvery is how often stale rows are looked for.
	agentUpgradeSweepEvery = time.Minute
)

// Answers of the upgrade route (api-v1 v1.48 item 1).
const (
	msgUpgradeNotAllowed = "this host does not allow upgrades from Deploybot (runner.toml agent_upgrade)"
	msgUpgradeRevoked    = "this host is revoked"
	msgUpgradeRunning    = "an upgrade of this host is already running"
	msgUpgradeNoAnswer   = "the host did not answer; the upgrade may still run, and the host reports its outcome when its agent connects"
)

var upgradeReasonRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// upgradeTaskRefusals are the plain answers for the agent_upgrade task
// errors (agent-protocol v1.20 item 4). The reason token is in the payload.
var upgradeTaskRefusals = map[string]string{
	"policy_denied":      "the host's runner.toml does not allow agent upgrades",
	"busy":               "the host is busy with another change; try again in a minute",
	"not_newer":          "the host already runs this version or newer",
	"bad_spec":           "the host refused the request",
	"helper_unavailable": "the host's helper is not running",
	"forbidden":          "the host's agent does not allow upgrades",
}

// agentUpgradeView is one upgrade row as the API answers it (item 3).
type agentUpgradeView struct {
	ID         uuid.UUID  `json:"id"`
	From       string     `json:"from"`
	To         string     `json:"to"`
	State      string     `json:"state"`
	Reason     *string    `json:"reason"`
	StartedBy  userRef    `json:"started_by"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

type userRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

func upgradeView(r store.AgentUpgrade) agentUpgradeView {
	v := agentUpgradeView{
		ID: r.ID, From: r.From, To: r.To, State: r.State, Reason: optText(r.Reason),
		StartedBy: userRef{ID: r.StartedBy, Name: r.StartedByName}, StartedAt: r.StartedAt.UTC(),
	}
	if r.FinishedAt != nil {
		t := r.FinishedAt.UTC()
		v.FinishedAt = &t
	}
	return v
}

// agentUpgradeAllowed is the hello flag; false when revoked or never seen.
func agentUpgradeAllowed(h store.Host) bool {
	return h.RevokedAt == nil && h.Programs != nil && h.Programs.AgentUpgrade
}

// handleAgentUpgrade is POST /v1/hosts/{id}/agent-upgrade (item 1): org
// owners and admins, console or MCP-delegated (the MCP server asks for
// confirm first).
func (s *Server) handleAgentUpgrade(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.fetchHost(w, r)
	if !ok {
		return
	}
	if _, ok := s.gateHost(w, r, p, host, authz.CapHostAdmin, nil, nil, audit.ActionHostAgentUpgrade); !ok {
		return
	}
	var body struct {
		Version *string `json:"version"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	latest := s.latestAgentVersion()
	refuse := func(code, msg string) {
		s.auditUpgradeRefused(r.Context(), p, host, code)
		writeCode(w, code, msg)
	}
	switch {
	case host.RevokedAt != nil:
		refuse("conflict", msgUpgradeRevoked)
		return
	case !agentversion.ValidRelease(latest):
		refuse("bad_request", "the latest agent version is not known")
		return
	case body.Version == nil || *body.Version != latest:
		refuse("bad_request", "version must be the latest agent version, "+latest)
		return
	case !agentversion.Older(host.AgentVersion, latest):
		refuse("bad_request", "this host already runs bot-agent "+latest+" or later")
		return
	case !agentUpgradeAllowed(host):
		refuse("unsupported_agent", msgUpgradeNotAllowed)
		return
	case s.hostStatus(host) != "online":
		refuse("host_offline", host.Name+" is offline")
		return
	}
	// A row left open past the 15 minutes no longer blocks a new upgrade.
	s.expireAgentUpgrade(r.Context(), host)
	now := s.now()
	row, err := s.Store.StartAgentUpgrade(r.Context(), store.AgentUpgrade{
		ID: uuid.New(), HostID: host.ID, From: host.AgentVersion, To: latest, StartedBy: p.User.ID, StartedAt: now,
	})
	if errors.Is(err, store.ErrConflict) {
		refuse("conflict", msgUpgradeRunning)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	audRow := hostAuditRow(host, nil, audit.ActionHostAgentUpgrade)
	audRow.Detail = map[string]any{"host": host.Name, "from": row.From, "to": row.To}
	s.auditOK(r.Context(), p, audRow)

	// The task goes on when the caller leaves: its answer is recorded.
	detached := context.WithoutCancel(r.Context())
	wait, cancel := context.WithTimeout(detached, s.agentUpgradeWaitFor())
	_, err = s.Hub.AgentUpgrade(wait, host, latest)
	cancel()
	ctx, done := context.WithTimeout(detached, agentUpgradeRecordTimeout)
	defer done()
	if err == nil {
		// Exactly started → running: a host result that settled the row first wins.
		if _, err := s.Store.SettleAgentUpgrade(ctx, row.ID, []string{store.AgentUpgradeStarted}, store.AgentUpgradeRunning, "", s.now()); err != nil {
			s.logger().Error("agent upgrade record failed", "host_id", host.ID.String())
		}
		writeOK(w, http.StatusAccepted, map[string]any{"upgrade_id": row.ID, "state": store.AgentUpgradeRunning})
		return
	}
	reason, refused := upgradeTaskReason(err)
	if !refused {
		// No answer: the helper may already have started the upgrade unit, so
		// the row stays started. A later hello or agent_upgrade_result settles
		// it, or the sweep marks it unknown after 15 minutes.
		writeCode(w, "unavailable", msgUpgradeNoAnswer)
		return
	}
	// The agent refused at once: the caller sees the 409 and the row keeps
	// the reason, so no problem opens (review M1 c).
	s.settleUpgrade(ctx, host, row, []string{store.AgentUpgradeStarted}, store.AgentUpgradeFailed, reason, audit.ViaSystem, false)
	msg, ok := upgradeTaskRefusals[reason]
	if !ok {
		msg = "the host refused the upgrade"
	}
	write(w, statusFor("conflict"), "conflict", msg, map[string]string{"reason": reason})
}

func (s *Server) agentUpgradeWaitFor() time.Duration {
	if s.agentUpgradeWaitTest > 0 {
		return s.agentUpgradeWaitTest
	}
	return agentUpgradeWait
}

// upgradeTaskReason maps a task error to the row reason. refused is true
// when the agent answered with an error code (409, the row fails);
// otherwise the task got no answer (503, the row stays open).
func upgradeTaskReason(err error) (reason string, refused bool) {
	var ce *agentws.CallError
	if errors.As(err, &ce) && ce.Agent != "" {
		if upgradeReasonRe.MatchString(ce.Agent) {
			return ce.Agent, true
		}
		return "error", true
	}
	return "unavailable", false
}

// auditUpgradeRefused records a refused start after the host gate.
func (s *Server) auditUpgradeRefused(ctx context.Context, p principal, host store.Host, code string) {
	row := hostAuditRow(host, nil, audit.ActionHostAgentUpgrade)
	row.Decision = audit.DecisionDenied
	row.ErrorCode = &code
	row.Detail = map[string]any{"host": host.Name, "from": host.AgentVersion, "to": s.latestAgentVersion()}
	s.auditOK(ctx, p, row)
}

// handleAgentUpgrades is GET /v1/hosts/{id}/agent-upgrades (item 3).
func (s *Server) handleAgentUpgrades(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.fetchHost(w, r)
	if !ok {
		return
	}
	if _, ok := s.gateHost(w, r, p, host, authz.CapHostView, nil, nil, ""); !ok {
		return
	}
	rows, err := s.Store.ListAgentUpgrades(r.Context(), host.ID, store.AgentUpgradeHistory)
	if err != nil {
		s.fail(w, err)
		return
	}
	items := make([]agentUpgradeView, len(rows))
	for i, row := range rows {
		items[i] = upgradeView(row)
	}
	writeOK(w, http.StatusOK, map[string]any{"items": items})
}

// UpgradeHook settles the host's open row from a hello or an
// agent_upgrade_result frame (item 2): a result for the row's version
// wins; otherwise a hello whose agent_version is the target means
// succeeded. Anything else leaves the row open. A result dated more than
// 5 minutes ahead of the API clock is ignored. With no open row, a hello
// may still close the agent_upgrade problem (upgradeCaughtUp).
func (s *Server) UpgradeHook(ctx context.Context, hostID uuid.UUID, agentVersion string, res *agentws.UpgradeResult) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentUpgradeRecordTimeout)
	defer cancel()
	if res != nil && res.At.After(s.now().Add(agentUpgradeFutureLimit)) {
		s.logger().Warn("agent upgrade result from the future ignored", "host_id", hostID.String())
		res = nil
	}
	row, err := s.Store.OpenAgentUpgrade(ctx, hostID)
	if errors.Is(err, store.ErrNotFound) {
		if agentVersion != "" {
			s.upgradeCaughtUp(ctx, hostID, agentVersion)
		}
		return
	}
	if err != nil {
		s.logger().Error("agent upgrade read failed", "host_id", hostID.String())
		return
	}
	state, reason := "", ""
	switch {
	case res != nil && res.Version == row.To && !res.At.Before(row.StartedAt.Add(-agentUpgradeResultSkew)):
		state, reason = res.Outcome, res.Reason
	case agentVersion != "" && agentVersion == row.To:
		state = store.AgentUpgradeSucceeded
	default:
		return
	}
	host, err := s.Store.GetHost(ctx, hostID)
	if err != nil {
		s.logger().Error("agent upgrade read failed", "host_id", hostID.String())
		return
	}
	// Any open state: the route may move started to running meanwhile (review L1).
	s.settleUpgrade(ctx, host, row, store.AgentUpgradeOpenStates, state, reason, audit.ViaAgent, true)
}

// upgradeCaughtUp handles a hello when no row is open (review M1 a). A row
// that went unknown and whose target the host now runs becomes succeeded;
// and a host that runs its newest row's target, or the latest release or
// newer, has its agent_upgrade problem resolved (an upgrade by hand counts).
func (s *Server) upgradeCaughtUp(ctx context.Context, hostID uuid.UUID, agentVersion string) {
	latest, err := s.Store.LatestAgentUpgrades(ctx, []uuid.UUID{hostID})
	if err != nil {
		s.logger().Error("agent upgrade read failed", "host_id", hostID.String())
		return
	}
	row, ok := latest[hostID]
	if !ok {
		return
	}
	host, err := s.Store.GetHost(ctx, hostID)
	if err != nil {
		s.logger().Error("agent upgrade read failed", "host_id", hostID.String())
		return
	}
	if row.State == store.AgentUpgradeUnknown && agentVersion == row.To {
		s.settleUpgrade(ctx, host, row, []string{store.AgentUpgradeUnknown}, store.AgentUpgradeSucceeded, "", audit.ViaAgent, true)
		return
	}
	if agentVersion == row.To || s.agentCurrent(agentVersion) {
		s.resolveProblem(ctx, store.ProblemAgentUpgrade, hostID.String(), "bot-agent on "+host.Name+" is up to date", "bot-agent "+agentVersion+" is running.")
	}
}

// agentCurrent reports whether version is the latest release or newer.
// False when the latest release is not set.
func (s *Server) agentCurrent(version string) bool {
	latest := s.latestAgentVersion()
	return agentversion.ValidRelease(latest) && agentversion.Valid(version) && !agentversion.Older(version, latest)
}

// settleUpgrade moves the row from one of the states in from to a settled
// state, then writes the audit row and, when problem is true, opens or
// resolves the agent_upgrade problem. A row another path settled first is
// left alone.
func (s *Server) settleUpgrade(ctx context.Context, host store.Host, row store.AgentUpgrade, from []string, state, reason, via string, problem bool) {
	ok, err := s.Store.SettleAgentUpgrade(ctx, row.ID, from, state, reason, s.now())
	if err != nil {
		s.logger().Error("agent upgrade record failed", "host_id", host.ID.String())
		return
	}
	if !ok {
		return
	}
	aud := hostAuditRow(host, nil, audit.ActionHostAgentUpgradeResult)
	aud.Via = via
	aud.Decision = audit.DecisionAllowed
	aud.Detail = map[string]any{"host": host.Name, "to": row.To, "state": state, "reason": optText(reason)}
	s.Audit.Record(ctx, aud)
	if problem {
		s.upgradeProblem(ctx, host, row.To, state)
	}
}

// upgradeProblem opens the host's agent_upgrade problem on rolled_back,
// failed or unknown and resolves it on succeeded (item 6).
func (s *Server) upgradeProblem(ctx context.Context, host store.Host, to, state string) {
	subject := host.ID.String()
	if state == store.AgentUpgradeSucceeded {
		s.resolveProblem(ctx, store.ProblemAgentUpgrade, subject, "bot-agent on "+host.Name+" is upgraded", "bot-agent "+to+" is running.")
		return
	}
	title, detail := "", ""
	switch state {
	case store.AgentUpgradeRolledBack:
		title = "bot-agent upgrade on " + host.Name + " was rolled back"
		detail = "The host put the previous version back by itself. See the upgrade history on the host page."
	case store.AgentUpgradeFailed:
		title = "bot-agent upgrade on " + host.Name + " failed"
		detail = "The upgrade to " + to + " did not run. See the upgrade history on the host page."
	case store.AgentUpgradeUnknown:
		title = "bot-agent upgrade on " + host.Name + " did not report back"
		detail = "No result for the upgrade to " + to + " after 15 minutes. Check the host."
	default:
		return
	}
	id := host.ID
	s.openProblem(ctx, store.ProblemOpen{
		Kind: store.ProblemAgentUpgrade, Subject: subject, OrgID: host.OrgID, HostID: &id,
		Level: store.ProblemNeedsAction, Title: title, Detail: detail,
	})
}

// expireAgentUpgrade settles the host's open row as unknown when it is
// older than store.AgentUpgradeStale.
func (s *Server) expireAgentUpgrade(ctx context.Context, host store.Host) {
	row, err := s.Store.OpenAgentUpgrade(ctx, host.ID)
	if err != nil || s.now().Sub(row.StartedAt) < store.AgentUpgradeStale {
		return
	}
	s.settleUpgrade(ctx, host, row, store.AgentUpgradeOpenStates, store.AgentUpgradeUnknown, "", audit.ViaSystem, true)
}

// RunAgentUpgrades settles stale rows every minute until ctx ends. It runs
// with BOT_API_PROBLEMS=off too: the rows must settle either way.
func (s *Server) RunAgentUpgrades(ctx context.Context) {
	tick := time.NewTicker(agentUpgradeSweepEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.SweepAgentUpgrades(ctx)
		}
	}
}

// SweepAgentUpgrades marks every started or running row older than 15
// minutes unknown (item 2), and resolves the agent_upgrade problem of a
// host that is gone, revoked or archived.
func (s *Server) SweepAgentUpgrades(ctx context.Context) {
	rows, err := s.Store.ListStaleAgentUpgrades(ctx, s.now().Add(-store.AgentUpgradeStale))
	if err != nil {
		s.logger().Error("agent upgrade sweep failed")
		return
	}
	for _, row := range rows {
		host, err := s.Store.GetHost(ctx, row.HostID)
		if err != nil {
			continue
		}
		wctx, cancel := context.WithTimeout(ctx, agentUpgradeRecordTimeout)
		s.settleUpgrade(wctx, host, row, store.AgentUpgradeOpenStates, store.AgentUpgradeUnknown, "", audit.ViaSystem, true)
		cancel()
	}
	if !s.problemsOn() {
		return
	}
	open, err := s.Store.ListProblems(ctx, store.ProblemFilter{All: true, Kind: store.ProblemAgentUpgrade})
	if err != nil {
		s.logger().Error("agent upgrade sweep failed")
		return
	}
	for _, p := range open {
		if p.ResolvedAt != nil || p.HostID == nil {
			continue
		}
		host, err := s.Store.GetHost(ctx, *p.HostID)
		gone := errors.Is(err, store.ErrNotFound) || (err == nil && (host.RevokedAt != nil || host.ArchivedAt != nil))
		switch {
		case gone:
			s.resolveProblem(ctx, store.ProblemAgentUpgrade, p.Subject, "", "No longer checked: the host was revoked.")
		case err == nil && s.agentCurrent(host.AgentVersion):
			// Review M1 b: the host caught up, by Deploybot or by hand.
			s.resolveProblem(ctx, store.ProblemAgentUpgrade, p.Subject, "bot-agent on "+host.Name+" is up to date", "bot-agent "+host.AgentVersion+" is running.")
		}
	}
}
