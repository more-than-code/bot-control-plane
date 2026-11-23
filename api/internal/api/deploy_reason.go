package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Why a deploy failed (api-v1 v1.47, spec docs/spec-deploy-reason.md).

const (
	// maxAgentReason caps the agent's own reason (agent-protocol v1.19).
	maxAgentReason = 512
	// maxStoredReason is the column check of migration 0031.
	maxStoredReason = 1024
	// maxStoredTail caps the stored log tail in bytes, counted like the
	// agent's line cap; the newest lines are kept.
	maxStoredTail = 16 << 10
)

// agentReasonOK accepts one printable line of at most maxAgentReason
// bytes. Anything else is dropped and the API works out its own reason.
func agentReasonOK(s *string) bool {
	if s == nil || *s == "" || len(*s) > maxAgentReason {
		return false
	}
	for _, r := range *s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// finalStatus keeps the part of the agent's status a row stores. A blank
// or unknown state (a timeout, an unreadable result) stores nothing.
func finalStatus(st imageStatusBody) *store.DeployFinalStatus {
	if !statusStateOK(st.State) {
		return nil
	}
	out := &store.DeployFinalStatus{State: st.State, RestartCount: st.RestartCount}
	if st.ExitCode != nil {
		n := *st.ExitCode
		out.ExitCode = &n
	}
	return out
}

// storedTail keeps the newest lines that fit maxStoredTail.
func storedTail(lines []string) []string {
	if len(lines) > maxAgentLines {
		lines = lines[len(lines)-maxAgentLines:]
	}
	total := 0
	start := len(lines)
	for start > 0 {
		n := len(lines[start-1]) + 1
		if total+n > maxStoredTail {
			break
		}
		total += n
		start--
	}
	return append([]string{}, lines[start:]...)
}

// deployReason is the reason a finished row stores. agent is the agent's
// own reason (already checked and redacted), or "". succeeded and
// unchanged have none.
func deployReason(outcome, agent string, exit *int, final *store.DeployFinalStatus) *string {
	var cause, after string
	switch outcome {
	case "succeeded", "unchanged", "in_flight", "":
		return nil
	case "rolled_back":
		cause = "the new container did not become healthy"
		after = "rolled back; the previous version is running again"
	case "rollback_failed":
		cause = "the new container did not become healthy"
		after = "the rollback failed too; the service may be down"
	case "failed":
		cause = failedCause(exit, final)
	default:
		cause = outcomeReason(outcome)
	}
	if agent != "" {
		cause = agent
	}
	text := cause
	if after != "" {
		text += "; " + after
	}
	return clipReason(text)
}

func failedCause(exit *int, final *store.DeployFinalStatus) string {
	if exit != nil && *exit != 0 {
		return fmt.Sprintf("the job exited with code %d", *exit)
	}
	if final == nil {
		return "the deploy failed"
	}
	switch {
	case final.State == "restarting" || final.RestartCount > 0:
		return fmt.Sprintf("the container kept restarting (%d restarts)", final.RestartCount)
	case (final.State == "exited" || final.State == "dead") && final.ExitCode != nil:
		return fmt.Sprintf("the container exited with code %d", *final.ExitCode)
	case final.State == "exited" || final.State == "dead":
		return "the container stopped"
	case final.State == "missing" || final.State == "created":
		return "the container did not start"
	case final.State == "running":
		return "the container did not pass its health checks in time"
	default:
		return "the deploy failed"
	}
}

// outcomeReason is the reason for an outcome on its own: what a row
// without a stored reason (older rows, agent rows) answers.
func outcomeReason(outcome string) string {
	switch outcome {
	case "failed":
		return "the deploy failed"
	case "timeout":
		return "the host did not report a result in time"
	case "rolled_back":
		return "the new container did not become healthy; rolled back; the previous version is running again"
	case "rollback_failed":
		return "the new container did not become healthy; the rollback failed too; the service may be down"
	case "image_unavailable":
		return "the image could not be pulled: the tag is missing or the registry refused the pull"
	case "forbidden_spec":
		return "the host policy refused this spec"
	case "conflict":
		return "another container is in the way on the host"
	case "refused":
		return "the host refused the task"
	case "auto_rolled_back":
		return "the service was unhealthy; the agent rolled back to the previous version"
	case "auto_gave_up":
		return "the service was unhealthy; the agent gave up restarting it"
	case "auto_failed":
		return "the service was unhealthy and the agent could not recover it"
	case "auto_restarted":
		return "the service was unhealthy; the agent restarted it"
	default:
		return ""
	}
}

// refusedReason is the reason of a row the host refused: the error code
// and the message the caller was shown (the fixed phrase when it held a
// secret).
func refusedReason(code, reason string) *string {
	text := phrase(code)
	if reason != "" && reason != text {
		text += ": " + reason
	}
	return clipReason(text)
}

func clipReason(text string) *string {
	if len(text) > maxStoredReason {
		cut := maxStoredReason - len("…")
		for cut > 0 && !utf8Start(text[cut]) {
			cut--
		}
		text = text[:cut] + "…"
	}
	return &text
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// withReason fills a stored null reason from the outcome.
func withReason(d store.ImageDeploy) store.ImageDeploy {
	if d.Reason != nil {
		return d
	}
	if text := outcomeReason(d.Outcome); text != "" && d.Outcome != "in_flight" {
		d.Reason = &text
	}
	return d
}

// redactDeployRead redacts a stored row for a read: the fields
// redactImageDeploy covers, and the reason.
func redactDeployRead(d store.ImageDeploy, forms []secretForm) store.ImageDeploy {
	d = redactImageDeploy(withReason(d), forms)
	d.Reason = redactText(d.Reason, forms)
	return d
}

// handleCatalogDeployRead is GET /v1/projects/{id}/catalog/{name}/deploys/{deploy_id}?host_id=
// (api-v1 v1.47): one row with its stored log tail. lines is null for a
// caller who holds neither logs nor deploy on the entry.
func (s *Server) handleCatalogDeployRead(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapRead); err != nil {
		s.writeAuth(w, err)
		return
	}
	p, projectID, ok := s.catalogRead(w, r)
	if !ok {
		return
	}
	hostID, ok := parseHostID(w, r.URL.Query().Get("host_id"))
	if !ok {
		return
	}
	deployID, err := uuid.Parse(r.PathValue("deploy_id"))
	if err != nil {
		writeCode(w, "bad_request", "deploy_id must be a UUID")
		return
	}
	entry, grants, ok := s.imageEntry(w, r, projectID, p)
	if !ok {
		return
	}
	if entry.Kind != "image" {
		writeCode(w, "bad_request", "kind must be image")
		return
	}
	role, err := s.callerRole(r.Context(), projectID, p.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	held := heldActions(entry.Kind, role, p.User.ID, grants)
	if len(held) == 0 {
		writeCode(w, "forbidden", "forbidden")
		return
	}
	row, err := s.Store.GetImageDeploy(r.Context(), entry.ID, hostID, deployID)
	if errors.Is(err, store.ErrNotFound) {
		writeCode(w, "not_found", "not found")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	// A running row reads like the list's first row; one past its
	// deadline is no longer running and the next begin records it as
	// timeout, so it reads as timeout here too.
	if row.InFlight {
		until := row.InFlightUntil.UTC()
		if row.InFlightUntil.After(s.now()) {
			row.Outcome = "in_flight"
			row.FinishedAt = nil
			row.EndsBy = &until
		} else {
			row.Outcome = "timeout"
			row.FinishedAt = &until
		}
	}
	img, err := catalog.ParseStored(entry.ImageRaw)
	if err != nil {
		s.fail(w, err)
		return
	}
	forms := s.redactValues(r.Context(), projectID, img)
	tail := row.LogTail
	out := redactDeployRead(row, forms)
	var lines []string
	if held["logs"] || held["deploy"] {
		lines = []string{}
		if tail != nil {
			lines = redactLines(tail, forms)
		}
	}
	writeOK(w, http.StatusOK, map[string]any{"deploy": out, "lines": lines})
}
