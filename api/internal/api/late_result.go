package api

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/more-than-code/bot-control-plane/api/internal/agentws"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Late deploy results (api-v1 v1.50 item 3, spec
// docs/spec-deploybot-on-containers.md §3.3).

// lateResultTimeout bounds the store work for one late result on the
// agent's read loop.
const lateResultTimeout = 10 * time.Second

// LateResultHook is the agentws.LateHook. A result no waiter in this
// process took is matched to its deploy or rollback row through the task id
// stored in Postgres (migration 0034), so it settles the row whichever API
// process receives it, including after a restart. Only a row still in
// flight or settled timeout is settled; it is audited like an on-time
// result with "late": true. An unknown task id, or a row already settled
// otherwise, is dropped and logged with the task id only. An error is a
// store failure: the hub then does not ack, and the agent resends.
func (s *Server) LateResultHook(ctx context.Context, hostID, taskID uuid.UUID, res agentws.LiveResult, callErr error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lateResultTimeout)
	defer cancel()
	task, err := s.Store.ImageDeployByTask(ctx, hostID, taskID)
	if errors.Is(err, store.ErrNotFound) {
		s.logger().Info("task result for an unknown task dropped", "task_id", taskID.String())
		return nil
	}
	if err != nil {
		return err
	}
	row := task.Deploy
	if !row.InFlight && row.Outcome != "timeout" {
		s.logger().Info("late task result for a settled deploy dropped", "task_id", taskID.String())
		return nil
	}
	forms, resolved := s.lateForms(ctx, task)
	got := takeDeploy(res, callErr, forms)
	finish := store.DeployFinish{At: s.now()}
	reason := got.reason
	if reason == "" {
		reason = got.msg
	}
	if got.request {
		// The host refused the task (policy, bad spec): as on time
		// (api-v1 v1.47 item 2) the row is kept as refused.
		finish.Outcome = "refused"
		finish.Reason = refusedReason(got.code, reason)
	} else {
		final := finalStatus(got.status)
		finish.Outcome = got.outcome
		finish.Digest = got.digest
		finish.ExitCode = got.exit
		finish.Reason = deployReason(got.outcome, got.agentReason, got.exit, final)
		finish.FinalStatus = final
		finish.LogTail = storedTail(got.lines)
		if !resolved {
			// The entry's secrets could not be resolved, so the agent's
			// text cannot be redacted against them: fail closed.
			finish.Reason = deployReason(got.outcome, "", got.exit, final)
			finish.LogTail = []string{lateTailWithheld}
		}
	}
	settled, err := s.Store.SettleLateImageDeploy(ctx, row.ID, finish)
	if errors.Is(err, store.ErrNotFound) {
		// Settled by someone else between the read and the write.
		s.logger().Info("late task result for a settled deploy dropped", "task_id", taskID.String())
		return nil
	}
	if err != nil {
		return err
	}
	if settled.Outcome == "refused" {
		s.auditLateDeny(ctx, task, settled, got.code, reason, forms)
	} else {
		s.auditSettled(ctx, task, settled, forms, "late")
	}
	s.DeployHook(ctx, task.ProjectID, row.EntryID, task.Entry, hostID)
	return nil
}

// lateTailWithheld is the stored log tail of a late result whose entry's
// secrets could not be resolved (api-v1 v1.50 item 9).
const lateTailWithheld = "[log lines withheld: the entry's secrets could not be resolved to redact them]"

// lateForms is the secret values to redact from a late result: those of
// the entry the row names by id (task.Entry is that entry's current name).
// resolved is false when they cannot all be known: the entry is gone or no
// longer parses, the secrets key is missing while the entry has secrets, or
// a secret does not decrypt.
func (s *Server) lateForms(ctx context.Context, task store.DeployTask) (forms []secretForm, resolved bool) {
	entry, err := s.Store.GetCatalogEntry(ctx, task.ProjectID, task.Entry)
	if err != nil || entry.ID != task.Deploy.EntryID {
		return nil, false
	}
	img, err := catalog.ParseStored(entry.ImageRaw)
	if err != nil {
		return nil, false
	}
	if s.Secrets == nil {
		needs, err := s.logsNeedKey(ctx, task.ProjectID, img)
		if err != nil || needs {
			return nil, false
		}
	}
	forms, failed := s.redactValuesCheck(ctx, task.ProjectID, img)
	return forms, !failed
}

// auditLateDeny is the late form of a host refusal: a deny row with the
// code as result and error code, like recordImageDeny, plus "late": true.
func (s *Server) auditLateDeny(ctx context.Context, task store.DeployTask, row store.ImageDeploy, code, reason string, forms []secretForm) {
	name := catalogActionName(task.Action)
	if name == "" {
		return
	}
	detail := map[string]any{"reason": redactExact(reason, forms), "entry": task.Entry, "late": true}
	if h, err := s.Store.GetHost(ctx, row.HostID); err == nil {
		detail["host"] = h.Name
	}
	s.recordAudit(ctx, store.AuditRow{
		ActorUserID: row.By,
		Via:         row.Via,
		ProjectID:   idPtr(task.ProjectID),
		HostID:      idPtr(row.HostID),
		Action:      name,
		Decision:    audit.DecisionDenied,
		Result:      &code,
		ErrorCode:   &code,
		Detail:      redactDetail(detail, forms),
	})
}

// auditSettled is auditImageOK for the row's own actor and channel, with
// flag ("late" or "swept") set to true in the detail.
func (s *Server) auditSettled(ctx context.Context, task store.DeployTask, row store.ImageDeploy, forms []secretForm, flag string) {
	name := catalogActionName(task.Action)
	if name == "" {
		return
	}
	host := ""
	if h, err := s.Store.GetHost(ctx, row.HostID); err == nil {
		host = h.Name
	}
	result := row.Outcome
	detail := map[string]any{
		"entry":   task.Entry,
		"host":    host,
		"outcome": row.Outcome,
		"via":     row.Via,
		flag:      true,
	}
	if row.Digest != nil {
		detail["digest"] = *row.Digest
	}
	audRow := store.AuditRow{
		ActorUserID: row.By,
		Via:         row.Via,
		ProjectID:   idPtr(task.ProjectID),
		HostID:      idPtr(row.HostID),
		Action:      name,
		Decision:    audit.DecisionAllowed,
		Result:      &result,
		Detail:      redactDetail(detail, forms),
	}
	if row.Outcome != "succeeded" && row.Outcome != "unchanged" {
		audRow.ErrorCode = &result
	}
	s.recordAudit(ctx, audRow)
}

// imageOpSweepEvery is how often SweepImageOps runs.
const imageOpSweepEvery = time.Minute

// RunImageOpSweep runs SweepImageOps every minute until ctx ends. It runs
// with BOT_API_PROBLEMS=off too.
func (s *Server) RunImageOpSweep(ctx context.Context) {
	tick := time.NewTicker(imageOpSweepEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.SweepImageOps(ctx)
		}
	}
}

// SweepImageOps settles, in the store, every deploy, rollback or remove row
// still in flight past its in_flight_until (the task budget, the result
// margin and the 120 s grace) as timeout (api-v1 v1.50). Such a row was
// left in flight because its frame reached the host and no result came: the
// waiting request ended on a dropped connection, or the API process ended.
// A late result can still settle it afterwards. A deploy or rollback row is
// audited with "swept": true and its deploy problem evaluated. The sweep is
// stateless, so several API processes may run it.
func (s *Server) SweepImageOps(ctx context.Context) {
	wctx, cancel := context.WithTimeout(ctx, lateResultTimeout)
	defer cancel()
	rows, err := s.Store.TimeoutStaleImageOps(wctx, s.now())
	if err != nil {
		s.logger().Error("image operation sweep failed")
		return
	}
	for _, task := range rows {
		s.auditSettled(wctx, task, task.Deploy, nil, "swept")
		s.DeployHook(wctx, task.ProjectID, task.Deploy.EntryID, task.Entry, task.Deploy.HostID)
	}
}
