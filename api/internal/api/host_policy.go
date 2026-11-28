package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/agentws"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/hostpolicy"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// A host's console-managed policy (api-v1 v1.51, agent-protocol v1.22,
// spec docs/spec-console-host-policy.md). A host whose root set
// console_policy = true lets org owners and admins change edge, images and
// projects. The agent stages the change and restarts; the API learns the
// outcome from the next hello's policy_hash. Console only: MCP links to the
// console page instead (the privileged-steps principle).

const (
	// hostPolicyWait is how long the route waits for the task answer.
	hostPolicyWait = time.Duration(agentws.HostPolicyTimeoutSec)*time.Second + 10*time.Second
	// hostPolicySettle is how long after the accept a hello with the new
	// hash may take: the 3 s restart delay, the host's 90 s hello wait, its
	// rollback restart and a margin. After it the apply is rolled_back (the
	// host is on the old hash) or unknown.
	hostPolicySettle = 150 * time.Second
	// hostPolicyKeep is how long a settled apply is still shown.
	hostPolicyKeep = 24 * time.Hour
)

// Apply states (api-v1 v1.51 item 2).
const (
	policyRunning    = "running"
	policyApplied    = "applied"
	policyRolledBack = "rolled_back"
	policyUnknown    = "unknown"
)

const (
	msgPolicyNotManaged = "this host's root has not let the console change its policy: add console_policy = true to /etc/bot-agent/runner.toml once and restart bot-agent (bot-agent 0.2.15 or later)"
	msgPolicyChanged    = "the host's policy changed since this page loaded; reload and make the change again"
	msgPolicyNoChange   = "nothing to change"
	msgPolicyNoAnswer   = "the host did not answer; the change may still apply, so reload in a minute"
)

// policyTaskRefusals are the plain answers for host_policy_apply errors.
var policyTaskRefusals = map[string][2]string{
	"policy_denied":      {"unsupported_agent", msgPolicyNotManaged},
	"forbidden":          {"unsupported_agent", msgPolicyNotManaged},
	"conflict":           {"conflict", msgPolicyChanged},
	"busy":               {"conflict", "the host is busy with another change; try again in a minute"},
	"bad_spec":           {"bad_request", "the host refused this policy"},
	"helper_unavailable": {"unavailable", "the host's helper is not running"},
}

// hostPolicyRun is one apply: in flight until settled.
type hostPolicyRun struct {
	BaseHash  string
	NewHash   string
	StartedAt time.Time
	By        userRef
	Change    hostpolicy.Change
	State     string
	SettledAt *time.Time
}

// hostPolicyRuns is the newest apply per host, kept in memory: the host is
// the source of truth for the policy, and the audit log keeps the history.
type hostPolicyRuns struct {
	mu   sync.Mutex
	runs map[uuid.UUID]hostPolicyRun
}

func (h *hostPolicyRuns) get(hostID uuid.UUID) (hostPolicyRun, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.runs[hostID]
	return r, ok
}

// begin stores run unless one is still running.
func (h *hostPolicyRuns) begin(hostID uuid.UUID, run hostPolicyRun) (hostPolicyRun, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.runs == nil {
		h.runs = map[uuid.UUID]hostPolicyRun{}
	}
	if cur, ok := h.runs[hostID]; ok && cur.State == policyRunning {
		return cur, false
	}
	h.runs[hostID] = run
	return run, true
}

func (h *hostPolicyRuns) drop(hostID uuid.UUID, run hostPolicyRun) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur, ok := h.runs[hostID]; ok && cur.StartedAt.Equal(run.StartedAt) {
		delete(h.runs, hostID)
	}
}

// settle moves a running apply to its outcome from the host's current
// hash. changed is true the one time the state leaves running.
func (h *hostPolicyRuns) settle(host store.Host, now time.Time) (hostPolicyRun, bool, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	run, ok := h.runs[host.ID]
	if !ok {
		return hostPolicyRun{}, false, false
	}
	if run.State != policyRunning {
		if run.SettledAt != nil && now.Sub(*run.SettledAt) > hostPolicyKeep {
			delete(h.runs, host.ID)
			return hostPolicyRun{}, false, false
		}
		return run, true, false
	}
	hash := ""
	if host.Programs != nil {
		hash = host.Programs.PolicyHash
	}
	switch {
	case run.NewHash != "" && hash == run.NewHash:
		run.State = policyApplied
	case now.Before(run.StartedAt.Add(hostPolicySettle)):
		return run, true, false
	case hash == run.BaseHash:
		run.State = policyRolledBack
	default:
		run.State = policyUnknown
	}
	at := now
	run.SettledAt = &at
	h.runs[host.ID] = run
	return run, true, true
}

// hostPolicyApplyView is the apply as the route answers it.
type hostPolicyApplyView struct {
	State      string            `json:"state"`
	StartedAt  time.Time         `json:"started_at"`
	EndsBy     time.Time         `json:"ends_by"`
	StartedBy  userRef           `json:"started_by"`
	Change     hostpolicy.Change `json:"change"`
	FinishedAt *time.Time        `json:"finished_at"`
}

func policyApplyView(run hostPolicyRun) *hostPolicyApplyView {
	v := &hostPolicyApplyView{
		State: run.State, StartedAt: run.StartedAt.UTC(), EndsBy: run.StartedAt.Add(hostPolicySettle).UTC(),
		StartedBy: run.By, Change: run.Change,
	}
	if run.SettledAt != nil {
		t := run.SettledAt.UTC()
		v.FinishedAt = &t
	}
	return v
}

// hostPolicyView is GET /v1/orgs/{id}/hosts/{host_id}/policy.
type hostPolicyView struct {
	Managed    bool                 `json:"managed"`
	PolicyHash *string              `json:"policy_hash"`
	Edge       bool                 `json:"edge"`
	Images     []string             `json:"images"`
	Projects   []string             `json:"projects"`
	Apply      *hostPolicyApplyView `json:"apply"`
}

// currentPolicy is the host's console-editable values from hello. Images
// are known only on a managed host.
func currentPolicy(host store.Host) (hostpolicy.Policy, bool) {
	p := hostpolicy.Policy{Images: []string{}, Projects: []string{}}
	if host.Programs == nil {
		return p, false
	}
	pr := host.Programs
	p.Edge = pr.EdgeAvailable()
	if pr.Projects != nil {
		p.Projects = append(p.Projects, pr.Projects...)
	}
	if pr.ConsolePolicy {
		p.Images = append(p.Images, pr.ImagePatterns...)
	}
	return p, pr.ConsolePolicy && pr.PolicyHash != ""
}

// settleHostPolicy settles the host's apply and audits the outcome once.
func (s *Server) settleHostPolicy(ctx context.Context, host store.Host) *hostPolicyApplyView {
	run, ok, changed := s.hostPolicies.settle(host, s.now())
	if !ok {
		return nil
	}
	if changed {
		row := hostAuditRow(host, nil, audit.ActionHostPolicyResult)
		row.Via = audit.ViaSystem
		row.Decision = audit.DecisionAllowed
		row.Detail = map[string]any{"host": host.Name, "outcome": run.State}
		s.Audit.Record(ctx, row)
	}
	return policyApplyView(run)
}

// handleHostPolicyGet is GET /v1/orgs/{id}/hosts/{host_id}/policy: org
// owners and admins, console only.
func (s *Server) handleHostPolicyGet(w http.ResponseWriter, r *http.Request) {
	_, host, ok := s.hostFileGate(w, r, authz.CapHostAdmin, "")
	if !ok {
		return
	}
	cur, managed := currentPolicy(host)
	// Images stay empty unless the host is managed, and are sent as [] (not
	// null) as api-v1 v1.51 says; the console rejects null.
	v := hostPolicyView{Managed: managed, Edge: cur.Edge, Images: []string{}, Projects: cur.Projects}
	if managed {
		v.Images = cur.Images
		hash := host.Programs.PolicyHash
		v.PolicyHash = &hash
	}
	v.Apply = s.settleHostPolicy(r.Context(), host)
	writeOK(w, http.StatusOK, v)
}

// handleHostPolicyApply is POST /v1/orgs/{id}/hosts/{host_id}/policy:
// {base_hash, edge, images, projects}. 202 {state, ends_by} once the host
// accepted and staged the change.
func (s *Server) handleHostPolicyApply(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.hostFileGate(w, r, authz.CapHostAdmin, audit.ActionHostPolicyApply)
	if !ok {
		return
	}
	var body struct {
		BaseHash *string  `json:"base_hash"`
		Edge     *bool    `json:"edge"`
		Images   []string `json:"images"`
		Projects []string `json:"projects"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.BaseHash == nil || !hostpolicy.ValidHash(*body.BaseHash) || body.Edge == nil {
		writeCode(w, "bad_request", "base_hash, edge, images and projects are required")
		return
	}
	next := hostpolicy.Policy{Edge: *body.Edge, Images: body.Images, Projects: body.Projects}
	if msg := hostpolicy.Check(next); msg != "" {
		writeCode(w, "bad_request", msg)
		return
	}
	refuse := func(code, msg, reason string) {
		row := hostAuditRow(host, nil, audit.ActionHostPolicyApply)
		row.Decision = audit.DecisionDenied
		row.ErrorCode = &code
		row.Detail = map[string]any{"host": host.Name, "reason": reason}
		s.auditOK(r.Context(), p, row)
		writeCode(w, code, msg)
	}
	cur, managed := currentPolicy(host)
	switch {
	case host.RevokedAt != nil || s.hostStatus(host) != "online":
		refuse("host_offline", host.Name+" is offline", "host_offline")
		return
	case !managed:
		refuse("unsupported_agent", msgPolicyNotManaged, "not_managed")
		return
	case host.Programs.PolicyHash != *body.BaseHash:
		refuse("conflict", msgPolicyChanged, "conflict")
		return
	}
	change := hostpolicy.Diff(cur, next)
	if change.Empty() {
		writeCode(w, "bad_request", msgPolicyNoChange)
		return
	}
	s.settleHostPolicy(r.Context(), host)
	run, ok := s.hostPolicies.begin(host.ID, hostPolicyRun{
		BaseHash: *body.BaseHash, StartedAt: s.now(), By: userRef{ID: p.User.ID, Name: p.User.Name},
		Change: change, State: policyRunning,
	})
	if !ok {
		until := run.StartedAt.Add(hostPolicySettle).UTC()
		msg := fmt.Sprintf("A policy change on %s has been running since %s UTC. Try again after %s UTC.",
			host.Name, run.StartedAt.UTC().Format("15:04"), until.Format("15:04"))
		writeJSON(w, statusFor("conflict"), envelopeRaw("conflict", msg, map[string]string{
			"started_at": run.StartedAt.UTC().Format(time.RFC3339),
			"ends_by":    until.Format(time.RFC3339),
		}))
		return
	}
	detached := context.WithoutCancel(r.Context())
	wait, cancel := context.WithTimeout(detached, s.hostPolicyWaitFor())
	res, err := s.Hub.HostPolicyApply(wait, host, map[string]any{
		"base_hash": *body.BaseHash, "edge": next.Edge, "images": next.Images, "projects": next.Projects,
	})
	cancel()
	ctx, done := context.WithTimeout(detached, 10*time.Second)
	defer done()
	audRow := hostAuditRow(host, nil, audit.ActionHostPolicyApply)
	audRow.Detail = map[string]any{
		"host": host.Name, "edge_from": change.EdgeFrom, "edge_to": change.EdgeTo,
		"images_added": change.ImagesAdded, "images_removed": change.ImagesRemoved,
		"projects_added": change.ProjectsAdded, "projects_removed": change.ProjectsRemoved,
	}
	if err != nil {
		var ce *agentws.CallError
		if errors.As(err, &ce) && ce.Agent != "" {
			s.hostPolicies.drop(host.ID, run)
			ans, ok := policyTaskRefusals[ce.Agent]
			if !ok {
				ans = [2]string{"conflict", "the host refused the change"}
			}
			code := ans[0]
			audRow.Decision = audit.DecisionDenied
			audRow.ErrorCode = &code
			audRow.Detail["reason"] = ce.Agent
			s.auditOK(ctx, p, audRow)
			write(w, statusFor(code), code, ans[1], map[string]string{"reason": ce.Agent})
			return
		}
		// No answer: the host may have staged it. The run stays and settles
		// from the next hello like an accepted one, with no new hash known,
		// so it ends rolled_back or unknown.
		s.auditOK(ctx, p, audRow)
		writeCode(w, "unavailable", msgPolicyNoAnswer)
		return
	}
	var reply struct {
		NewHash string `json:"new_hash"`
	}
	if json.Unmarshal(res.Payload, &reply) != nil || !hostpolicy.ValidHash(reply.NewHash) {
		s.auditOK(ctx, p, audRow)
		writeCode(w, "unavailable", msgPolicyNoAnswer)
		return
	}
	s.hostPolicies.mu.Lock()
	if cur, ok := s.hostPolicies.runs[host.ID]; ok && cur.StartedAt.Equal(run.StartedAt) {
		cur.NewHash = reply.NewHash
		s.hostPolicies.runs[host.ID] = cur
		run = cur
	}
	s.hostPolicies.mu.Unlock()
	s.auditOK(ctx, p, audRow)
	writeOK(w, http.StatusAccepted, policyApplyView(run))
}

func (s *Server) hostPolicyWaitFor() time.Duration {
	if s.hostPolicyWaitTest > 0 {
		return s.hostPolicyWaitTest
	}
	return hostPolicyWait
}
