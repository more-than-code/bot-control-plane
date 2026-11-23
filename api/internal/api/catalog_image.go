package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/agentws"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/ingest"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

const (
	reasonNoContainers  = "this host does not run containers"
	reasonNoFiles       = "this host's agent does not support files"
	reasonNoHealthcheck = "this host's agent does not support healthcheck"
	reasonNoLogging     = "this host's agent does not support log settings"
	reasonNoPolicy      = "this host's agent does not support health policies"
	reasonNoNetworks    = "this host's agent does not support shared networks; upgrade it to 0.2.9"
	// reasonNoHostNetwork is the api-v1 v1.50 item 1 refusal (409
	// unsupported_agent) for network "host" on a host without
	// programs.host_network.
	reasonNoHostNetwork = "this host does not allow host networking (runner.toml host_network_projects)"
	// msgMayStillFinish is the 503 for a deploy or rollback whose frame
	// reached the host before the connection dropped (api-v1 v1.50 item 8).
	msgMayStillFinish  = "host unavailable; the host may still finish this deploy. It shows as running until its ends_by time; check the deploy history"
	invalidAgentResult = "invalid agent result"
	settleTimeout      = 5 * time.Second
	staleGraceSec      = 120
	// resultMarginSec is the API's wait past the deploy or rollback task
	// budget (api-v1 v1.42 item 3).
	resultMarginSec   = 30
	maxAgentLines     = 50
	maxLogLines       = 2000
	maxAgentLineChars = 2048     // agent-protocol: each line cut to 2048 characters
	maxAgentLineTotal = 64 << 10 // and at most 64 KiB in all, counted the same way
	maxStatusBytes    = 2048
	secretRedactMin   = 6
	redactedMarker    = "[REDACTED]"
	secretLineMin     = 16
	secretPieceMax    = 4096
)

// reasonNetworkClosed is the refusal for a shared network the host does not
// define or does not open to the project. name passed the network name rule
// (catalog.ParseStored re-checks stored names), so it is safe in the reason.
func reasonNetworkClosed(name string) string {
	return "shared network " + name + " is not open to this project on this host"
}

var (
	errSecretsKey = errors.New("secrets key not configured")
	errDecrypt    = errors.New("decrypt failed")
)

func (s *Server) handleCatalogDeploy(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapRead); err != nil {
		s.writeAuth(w, err)
		return
	}
	s.imageMutate(w, r, "deploy")
}

func (s *Server) handleCatalogRollback(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapRead); err != nil {
		s.writeAuth(w, err)
		return
	}
	s.imageMutate(w, r, "rollback")
}

func (s *Server) handleCatalogRemove(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapRead); err != nil {
		s.writeAuth(w, err)
		return
	}
	s.imageMutate(w, r, "remove")
}

func (s *Server) handleCatalogDeploys(w http.ResponseWriter, r *http.Request) {
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
	if len(heldActions(entry.Kind, role, p.User.ID, grants)) == 0 {
		writeCode(w, "forbidden", "forbidden")
		return
	}
	items, err := s.Store.ListImageDeploys(r.Context(), entry.ID, hostID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if items == nil {
		items = []store.ImageDeploy{}
	}
	// A running operation comes first (api-v1 v1.42 item 1). A row whose
	// deadline has passed is no longer running: the next begin records it
	// as timeout.
	running, err := s.Store.RunningImageOp(r.Context(), entry.ID, hostID)
	switch {
	case err == nil && running.InFlightUntil.After(s.now()):
		until := running.InFlightUntil.UTC()
		running.Outcome = "in_flight"
		running.FinishedAt = nil
		running.EndsBy = &until
		items = append([]store.ImageDeploy{running}, items...)
	case err != nil && !errors.Is(err, store.ErrNotFound):
		s.fail(w, err)
		return
	}
	img, err := catalog.ParseStored(entry.ImageRaw)
	if err != nil {
		s.fail(w, err)
		return
	}
	// The stored digest stays so rollback can match it. This is a copy.
	forms := s.redactValues(r.Context(), projectID, img)
	for i := range items {
		items[i] = redactDeployRead(items[i], forms)
	}
	writeOK(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) imageMutate(w http.ResponseWriter, r *http.Request, action string) {
	p, projectID, ok := s.catalogRead(w, r)
	if !ok {
		return
	}
	body, ok := s.imageBody(w, r, action)
	if !ok {
		return
	}
	entry, grants, ok := s.imageEntry(w, r, projectID, p)
	if !ok {
		return
	}
	role, err := s.callerRole(r.Context(), projectID, p.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !allowsAction(heldActions(entry.Kind, role, p.User.ID, grants), "deploy") {
		s.imageDeny(w, r, p, projectID, body.HostID, "", entry.Name, action, "forbidden", "forbidden")
		return
	}
	if entry.Kind != "image" {
		writeCode(w, "bad_request", "kind must be image")
		return
	}
	if !catalog.ValidIdempotencyKey(body.Key) {
		writeCode(w, "bad_request", "invalid idempotency_key")
		return
	}
	if action == "rollback" && !catalog.ValidDigest(body.Digest) {
		writeCode(w, "bad_request", "invalid digest")
		return
	}
	host, ok := s.programHost(w, r, p, projectID, body.HostID, entry, action, true)
	if !ok {
		return
	}
	if !containersReady(host) {
		s.imageDeny(w, r, p, projectID, host.ID, host.Name, entry.Name, action, "bad_request", reasonNoContainers)
		return
	}
	if !s.imageListed(w, r, p, projectID, host, entry, action) {
		return
	}
	if s.hostStatus(host) != "online" {
		s.imageDeny(w, r, p, projectID, host.ID, host.Name, entry.Name, action, "unavailable", "host unavailable")
		return
	}
	img, err := catalog.ParseStored(entry.ImageRaw)
	if err != nil {
		s.fail(w, err)
		return
	}
	if (action == "deploy" || action == "rollback") && !filesReady(host, img) {
		s.imageDeny(w, r, p, projectID, host.ID, host.Name, entry.Name, action, "bad_request", reasonNoFiles)
		return
	}
	if (action == "deploy" || action == "rollback") && !healthcheckReady(host, img) {
		s.imageDeny(w, r, p, projectID, host.ID, host.Name, entry.Name, action, "bad_request", reasonNoHealthcheck)
		return
	}
	if (action == "deploy" || action == "rollback") && !loggingReady(host, img) {
		s.imageDeny(w, r, p, projectID, host.ID, host.Name, entry.Name, action, "bad_request", reasonNoLogging)
		return
	}
	if (action == "deploy" || action == "rollback") && !policyReady(host, img) {
		s.imageDeny(w, r, p, projectID, host.ID, host.Name, entry.Name, action, "bad_request", reasonNoPolicy)
		return
	}
	if (action == "deploy" || action == "rollback") && !s.networksAllowed(w, r, p, projectID, host, entry.Name, action, img) {
		return
	}
	if !hostNetworkReady(host, img, action) {
		s.imageDeny(w, r, p, projectID, host.ID, host.Name, entry.Name, action, "unsupported_agent", reasonNoHostNetwork)
		return
	}
	if action == "rollback" && !s.digestAllowed(w, r, entry.ID, host.ID, body.Digest) {
		return
	}
	if action != "remove" {
		needs, err := s.imageNeedsKey(r.Context(), projectID, img)
		if err != nil {
			s.fail(w, err)
			return
		}
		if needs && s.Secrets == nil {
			s.imageDeny(w, r, p, projectID, host.ID, host.Name, entry.Name, action, "unavailable", errSecretsKey.Error())
			return
		}
	}
	var force *bool
	digest := ""
	if action == "deploy" {
		force = &body.Force
	}
	if action == "rollback" {
		digest = body.Digest
	}
	hash := imageRequestHash(action, projectID, host.ID, entry.Name, body.Force, digest)
	if !s.claimOrReplay(w, r, p.User.ID, body.Key, hash) {
		return
	}
	if p.Session.Kind == store.KindDelegated {
		if !s.envRefsOK(w, r, p, projectID, host, entry, img, action, body.Key) {
			return
		}
		s.issueConfirmation(w, r, p, projectID, host, entry, action, body.Key, hash, digest, force, img.Networks)
		return
	}
	s.performImage(w, r, p, projectID, host, entry, img, action, body.Force, digest, 0, body.Key)
}

// envRefsOK refuses a delegated deploy or rollback plan whose env has a bad
// ${KEY} reference (api-v1 v1.27 item 7), so no plan is issued that confirm
// would refuse. Other mergeEnv errors (decrypt, store) are left to the
// confirm, which reports and audits them as for a console call.
// The idempotency key was claimed: the refusal is stored under it.
func (s *Server) envRefsOK(w http.ResponseWriter, r *http.Request, p principal, projectID uuid.UUID, host store.Host, entry store.CatalogEntry, img catalog.Image, action, key string) bool {
	if action != "deploy" && action != "rollback" {
		return true
	}
	_, err := s.mergeImageEnv(r.Context(), projectID, img)
	if refCall(err) == nil {
		return true
	}
	s.auditTaskBuildErr(r.Context(), p, projectID, host, entry.Name, action, err)
	s.imageFail(r.Context(), w, p.User.ID, key, err, nil)
	return false
}

// confirmEnvRefsOK refuses a deploy or rollback confirm whose env has a bad
// ${KEY} reference before the plan is used, like the other confirm-time
// checks, so the user can fix the value and confirm the same plan again
// (api-v1 v1.27 item 7). Nothing is stored under the idempotency key. Other
// merge errors are left to performImage.
func (s *Server) confirmEnvRefsOK(w http.ResponseWriter, r *http.Request, p principal, c store.CatalogConfirmation, host store.Host, entry store.CatalogEntry, img catalog.Image) bool {
	if c.Action != "deploy" && c.Action != "rollback" {
		return true
	}
	_, err := s.mergeImageEnv(r.Context(), c.ProjectID, img)
	call := refCall(err)
	if call == nil {
		return true
	}
	s.auditTaskBuildErr(r.Context(), p, c.ProjectID, host, entry.Name, c.Action, err)
	writeCode(w, call.Code, call.Msg)
	return false
}

func (s *Server) finishImageShort(w http.ResponseWriter, r *http.Request, p principal, projectID uuid.UUID, host store.Host, entry store.CatalogEntry, action, key string, lines int) {
	if !containersReady(host) {
		writeCode(w, "bad_request", reasonNoContainers)
		return
	}
	if !s.imageListed(w, r, p, projectID, host, entry, action) {
		return
	}
	if s.hostStatus(host) != "online" {
		writeCode(w, "unavailable", "host unavailable")
		return
	}
	img, err := catalog.ParseStored(entry.ImageRaw)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !hostNetworkReady(host, img, action) {
		s.imageDeny(w, r, p, projectID, host.ID, host.Name, entry.Name, action, "unsupported_agent", reasonNoHostNetwork)
		return
	}
	// Logs are not audited and are not idempotent. Refuse before the hub
	// call so a missing key cannot return unredacted lines.
	if action == "logs" && s.Secrets == nil {
		needs, err := s.logsNeedKey(r.Context(), projectID, img)
		if err != nil {
			s.fail(w, err)
			return
		}
		if needs {
			writeCode(w, "unavailable", errSecretsKey.Error())
			return
		}
	}
	hash := programRequestHash(action, projectID, host.ID, entry.Name)
	if action == "run" || action == "stop" || action == "restart" {
		if !s.claimOrReplay(w, r, p.User.ID, key, hash) {
			return
		}
		if p.Session.Kind == store.KindDelegated {
			s.issueConfirmation(w, r, p, projectID, host, entry, action, key, hash, "", nil, nil)
			return
		}
	}
	s.performImage(w, r, p, projectID, host, entry, img, action, false, "", lines, key)
}

func (s *Server) confirmImage(w http.ResponseWriter, r *http.Request, p principal, c store.CatalogConfirmation, entry store.CatalogEntry) {
	host, ok := s.programHost(w, r, p, c.ProjectID, c.HostID, entry, c.Action, true)
	if !ok {
		return
	}
	if !containersReady(host) {
		s.imageDeny(w, r, p, c.ProjectID, host.ID, host.Name, entry.Name, c.Action, "bad_request", reasonNoContainers)
		return
	}
	if !s.imageListed(w, r, p, c.ProjectID, host, entry, c.Action) {
		return
	}
	if s.hostStatus(host) != "online" {
		s.imageDeny(w, r, p, c.ProjectID, host.ID, host.Name, entry.Name, c.Action, "unavailable", "host unavailable")
		return
	}
	img, err := catalog.ParseStored(entry.ImageRaw)
	if err != nil {
		s.fail(w, err)
		return
	}
	digest := ""
	if c.ImageDigest != nil {
		digest = *c.ImageDigest
	}
	if (c.Action == "deploy" || c.Action == "rollback") && !filesReady(host, img) {
		s.imageDeny(w, r, p, c.ProjectID, host.ID, host.Name, entry.Name, c.Action, "bad_request", reasonNoFiles)
		return
	}
	if (c.Action == "deploy" || c.Action == "rollback") && !healthcheckReady(host, img) {
		s.imageDeny(w, r, p, c.ProjectID, host.ID, host.Name, entry.Name, c.Action, "bad_request", reasonNoHealthcheck)
		return
	}
	if (c.Action == "deploy" || c.Action == "rollback") && !loggingReady(host, img) {
		s.imageDeny(w, r, p, c.ProjectID, host.ID, host.Name, entry.Name, c.Action, "bad_request", reasonNoLogging)
		return
	}
	if (c.Action == "deploy" || c.Action == "rollback") && !policyReady(host, img) {
		s.imageDeny(w, r, p, c.ProjectID, host.ID, host.Name, entry.Name, c.Action, "bad_request", reasonNoPolicy)
		return
	}
	if (c.Action == "deploy" || c.Action == "rollback") && !s.networksAllowed(w, r, p, c.ProjectID, host, entry.Name, c.Action, img) {
		return
	}
	if !hostNetworkReady(host, img, c.Action) {
		s.imageDeny(w, r, p, c.ProjectID, host.ID, host.Name, entry.Name, c.Action, "unsupported_agent", reasonNoHostNetwork)
		return
	}
	if c.Action == "rollback" && !s.digestAllowed(w, r, entry.ID, host.ID, digest) {
		return
	}
	if c.Action == "deploy" || c.Action == "rollback" {
		needs, err := s.imageNeedsKey(r.Context(), c.ProjectID, img)
		if err != nil {
			s.fail(w, err)
			return
		}
		if needs && s.Secrets == nil {
			s.imageDeny(w, r, p, c.ProjectID, host.ID, host.Name, entry.Name, c.Action, "unavailable", errSecretsKey.Error())
			return
		}
	}
	if entry.SpecHash != c.SpecHash {
		s.planChanged(w, p.User.ID, c.IdempotencyKey)
		return
	}
	if !s.confirmEnvRefsOK(w, r, p, c, host, entry, img) {
		return
	}
	if err := s.Store.MarkConfirmationUsed(r.Context(), secretHash(r), p.User.ID, s.now()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.catalogConfirmationLost(w, r, p, secretHash(r))
			return
		}
		s.fail(w, err)
		return
	}
	force := c.ImageForce != nil && *c.ImageForce
	s.performImage(w, r, p, c.ProjectID, host, entry, img, c.Action, force, digest, 0, c.IdempotencyKey)
}

// secretHash is the confirmation token hash already checked by the caller.
// confirmImage receives the request so it can mark that row used.
func secretHash(r *http.Request) []byte {
	return secret.Hash(r.PathValue("confirmation_id"))
}

func (s *Server) performImage(w http.ResponseWriter, r *http.Request, p principal, projectID uuid.UUID, host store.Host, entry store.CatalogEntry, img catalog.Image, action string, force bool, digest string, lines int, key string) {
	timeout := imageTimeout(action, img)
	if s.imageBudget > 0 {
		timeout = s.imageBudget
	}
	// The API waits past the agent's budget, so the agent's own outcome is
	// recorded rather than a blind timeout (api-v1 v1.42 item 3).
	wait := timeout + s.resultMargin(action)
	ctx, cancel := imageCtx(r, action, wait)
	defer cancel()
	mutate := action == "deploy" || action == "rollback" || action == "remove"
	var opID uuid.UUID
	hold := false
	if mutate {
		at := s.now()
		until := at.Add(time.Duration(wait+staleGraceSec) * time.Second)
		row, err := s.Store.BeginImageOp(ctx, entry.ID, host.ID, entry.SpecHash, p.User.ID, viaKind(p.Session.Kind), at, until)
		var running *store.ImageOpRunning
		if errors.As(err, &running) {
			s.imageRunning(w, p.User.ID, key, entry.Name, host.Name, running)
			return
		}
		if errors.Is(err, store.ErrConflict) {
			// A begin lost the race and the winner could not be named.
			s.releaseKey(p.User.ID, key)
			writeCode(w, "conflict", fmt.Sprintf(msgJustStarted, entry.Name, host.Name))
			return
		}
		if err != nil {
			s.imageFail(ctx, w, p.User.ID, key, err, nil)
			return
		}
		opID = row.ID
		hold = true
		defer func() {
			if hold {
				_ = s.settle(func(c context.Context) error {
					return s.Store.ReleaseImageOp(c, opID)
				})
			}
		}()
	}
	spec, forms, err := s.imageTask(ctx, projectID, host, entry, img, action, force, digest, lines)
	if err != nil {
		s.auditTaskBuildErr(ctx, p, projectID, host, entry.Name, action, err)
		s.imageFail(ctx, w, p.User.ID, key, err, forms)
		return
	}
	// Deploy and rollback already returned the forms of the values they put
	// in the frame. Other actions keep that frame and load forms only to redact.
	if action != "deploy" && action != "rollback" {
		forms = s.redactValues(ctx, projectID, img)
	}
	if s.Hub == nil {
		s.imageFail(ctx, w, p.User.ID, key, &agentws.CallError{Code: "error", Msg: "internal error"}, forms)
		return
	}
	// api-v1 v1.50 item 3: the task id is stored on the deploy or rollback
	// row before the frame goes out, so a result that outlives this request
	// (or this process) can still settle the row.
	taskID := uuid.New()
	if action == "deploy" || action == "rollback" {
		if err := s.Store.SetImageOpTask(ctx, opID, taskID, action); err != nil {
			s.imageFail(ctx, w, p.User.ID, key, err, forms)
			return
		}
	}
	res, callErr := s.Hub.ImageWithID(ctx, host, imageKind(action), spec, timeout, taskID)
	got := takeImage(action, res, callErr, forms)
	// api-v1 v1.50 item 9: a deploy or rollback result the agent sent (and
	// keeps until acked) is acked only after its row is written.
	ackStored := func() {
		var ce *agentws.CallError
		if (action == "deploy" || action == "rollback") && (callErr == nil || (errors.As(callErr, &ce) && ce.FromResult)) {
			s.Hub.AckResult(host.ID, taskID)
		}
	}
	if action == "remove" && got.request && got.code == "timeout" {
		hold = false
		_ = s.settle(func(c context.Context) error {
			return s.Store.ReleaseImageOp(c, opID)
		})
		s.auditImageOK(ctx, p, projectID, host, entry.Name, action, "timeout", nil, forms)
		s.imageFail(ctx, w, p.User.ID, key, &agentws.CallError{Code: "timeout", Msg: phrase("timeout")}, forms)
		return
	}
	if got.request {
		// An oversized deploy frame is not a deploy. The row is released by the defer.
		// Audit keeps the fixed phrase when the HTTP msg was redacted in place.
		if got.msg != agentws.EnvFrameTooLarge {
			reason := got.reason
			if reason == "" {
				reason = got.msg
			}
			s.auditImageDeny(ctx, p, projectID, host, entry.Name, action, got.code, reason, forms)
			// A deploy or rollback the host refused stays in history as
			// refused (api-v1 v1.47 item 2). A failed write keeps the row in
			// flight for the sweep and sends no ack (api-v1 v1.50 item 9).
			if (action == "deploy" || action == "rollback") && hostRejection(got.code) && !got.maybeSent {
				err := s.settle(func(c context.Context) error {
					_, ferr := s.Store.FinishImageDeployWith(c, opID, store.DeployFinish{
						Outcome: "refused", Reason: refusedReason(got.code, reason), At: s.now(),
					})
					return ferr
				})
				hold = false
				if err == nil {
					ackStored()
				}
			}
			// api-v1 v1.50: the frame reached the host, and an agent that
			// finishes deploys on the host (agent-protocol v1.21 item 5)
			// resends the result after it reconnects. The row stays in
			// flight for that late result; SweepImageOps settles it timeout
			// after in_flight_until when none comes. Only a frame never
			// written releases the row (the defer), as in v1.47.
			if (action == "deploy" || action == "rollback") && got.maybeSent {
				hold = false
				got.msg = msgMayStillFinish
			}
		}
		s.imageFail(ctx, w, p.User.ID, key, &agentws.CallError{Code: got.code, Msg: got.msg}, forms)
		return
	}
	if action == "logs" {
		s.imageDone(w, p.User.ID, "", got.logs)
		return
	}
	if action == "run" || action == "stop" || action == "restart" || action == "status" {
		s.imageDone(w, p.User.ID, key, got.status)
		return
	}
	if action == "remove" {
		hold = false
		_ = s.settle(func(c context.Context) error {
			return s.Store.ReleaseImageOp(c, opID)
		})
		s.auditImageOK(ctx, p, projectID, host, entry.Name, action, got.outcome, nil, forms)
		if got.outcome == "succeeded" {
			s.entryRemoved(ctx, entry, host)
		}
		s.imageDone(w, p.User.ID, key, map[string]any{"status": got.status})
		return
	}
	var finished store.ImageDeploy
	err = s.settle(func(c context.Context) error {
		var ferr error
		final := finalStatus(got.status)
		finished, ferr = s.Store.FinishImageDeployWith(c, opID, store.DeployFinish{
			Outcome: got.outcome, Digest: got.digest, ExitCode: got.exit,
			Reason:      deployReason(got.outcome, got.agentReason, got.exit, final),
			FinalStatus: final,
			LogTail:     storedTail(got.lines),
			At:          s.now(),
		})
		return ferr
	})
	if errors.Is(err, store.ErrNotFound) {
		// A late result (another process, or this one through the late
		// hook) settled the row first: answer from the stored row; that
		// path already audited it (api-v1 v1.50 item 9).
		stored, rerr := s.Store.GetImageDeploy(ctx, entry.ID, host.ID, opID)
		if rerr == nil && !stored.InFlight {
			hold = false
			ackStored()
			s.imageDone(w, p.User.ID, key, map[string]any{
				"deploy": redactDeployRead(stored, forms),
				"status": got.status,
				"lines":  got.lines,
			})
			return
		}
	}
	if err != nil {
		// The row stays in flight for the sweep; no ack, so the agent
		// resends the result (api-v1 v1.50 item 9).
		hold = false
		s.imageFail(ctx, w, p.User.ID, key, err, forms)
		return
	}
	hold = false
	ackStored()
	s.auditImageOK(ctx, p, projectID, host, entry.Name, action, finished.Outcome, finished.Digest, forms)
	s.DeployHook(ctx, projectID, entry.ID, entry.Name, host.ID)
	s.imageDone(w, p.User.ID, key, map[string]any{
		"deploy": redactDeployRead(finished, forms),
		"status": got.status,
		"lines":  got.lines,
	})
}

func (s *Server) writeImage(w http.ResponseWriter, r *http.Request, p principal, projectID uuid.UUID, name string, raw json.RawMessage, key string, replace bool) {
	names, err := s.Store.ListEnvKeyNames(r.Context(), projectID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if names == nil {
		names = map[string][]string{}
	}
	known, err := s.projectFileNames(r.Context(), projectID)
	if err != nil {
		s.fail(w, err)
		return
	}
	// api-v1 v1.33 item 6: secret_env may give private keys as objects.
	draft, err := draftPrivate(name, catalog.SecretEnvField(raw), names)
	if err != nil {
		s.writeField(w, err)
		return
	}
	img, err := catalog.BuildImage(name, draft.spec(raw), names, known)
	if err != nil {
		s.writeField(w, err)
		return
	}
	final := draft.finish(img.SecretEnv, names)
	hash := catalog.ImageHash(img, digestMap(names, img))
	if p.Session.Kind == store.KindDelegated {
		s.issueCatalogImage(w, r, p, projectID, name, img, draft, final, key, replace)
		return
	}
	private, err := s.privateWrite(projectID, draft, final)
	if err != nil {
		s.secretWriteErr(w, err)
		return
	}
	in := store.CatalogWrite{
		Name: name, Kind: "image", ImageRaw: json.RawMessage(catalog.SpecJSON(img)),
		SpecHash: hash, CommandDisplay: img.Image, Actor: p.User.ID, At: s.now(), Private: private,
	}
	var entry store.CatalogEntry
	var old string
	if replace {
		old, entry, err = s.Store.ReplaceCatalogEntry(r.Context(), projectID, name, in)
	} else {
		entry, err = s.Store.CreateCatalogEntry(r.Context(), projectID, in)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	// Audit right after the commit, before any read that could fail.
	detail := withPrivateAudit(map[string]any{"name": entry.Name, "spec_hash": entry.SpecHash}, entry.Name, private)
	action := audit.ActionCatalogCreate
	status := http.StatusCreated
	if replace {
		action = audit.ActionCatalogUpdate
		status = http.StatusOK
		detail["old_spec_hash"] = old
	}
	s.auditOK(r.Context(), p, store.AuditRow{ProjectID: idPtr(projectID), Action: action, Detail: detail})
	actions, err := s.callerActions(r, projectID, p.User.ID, entry)
	if err != nil {
		s.fail(w, err)
		return
	}
	entry.MyActions = actions
	if replace {
		entry = s.replacedLastDeployed(r, projectID, entry)
	}
	writeOK(w, status, entry)
}

type imageActBody struct {
	HostID uuid.UUID
	Key    string
	Force  bool
	Digest string
}

func (s *Server) imageBody(w http.ResponseWriter, r *http.Request, action string) (imageActBody, bool) {
	switch action {
	case "deploy":
		var body struct {
			HostID string `json:"host_id"`
			Force  bool   `json:"force"`
			Key    string `json:"idempotency_key"`
		}
		if !decodeJSON(w, r, &body) {
			return imageActBody{}, false
		}
		id, ok := parseHostID(w, body.HostID)
		if !ok {
			return imageActBody{}, false
		}
		return imageActBody{HostID: id, Key: body.Key, Force: body.Force}, true
	case "rollback":
		var body struct {
			HostID string `json:"host_id"`
			Digest string `json:"digest"`
			Key    string `json:"idempotency_key"`
		}
		if !decodeJSON(w, r, &body) {
			return imageActBody{}, false
		}
		id, ok := parseHostID(w, body.HostID)
		if !ok {
			return imageActBody{}, false
		}
		return imageActBody{HostID: id, Key: body.Key, Digest: body.Digest}, true
	default:
		var body struct {
			HostID string `json:"host_id"`
			Key    string `json:"idempotency_key"`
		}
		if !decodeJSON(w, r, &body) {
			return imageActBody{}, false
		}
		id, ok := parseHostID(w, body.HostID)
		if !ok {
			return imageActBody{}, false
		}
		return imageActBody{HostID: id, Key: body.Key}, true
	}
}

func (s *Server) catalogRead(w http.ResponseWriter, r *http.Request) (principal, uuid.UUID, bool) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return principal{}, uuid.Nil, false
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapRead); err != nil {
		s.writeAuth(w, err)
		return principal{}, uuid.Nil, false
	}
	return p, id, true
}

func (s *Server) imageEntry(w http.ResponseWriter, r *http.Request, projectID uuid.UUID, _ principal) (store.CatalogEntry, []store.CatalogGrant, bool) {
	entry, err := s.catalogByName(r, projectID, r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return store.CatalogEntry{}, nil, false
	}
	grants, err := s.Store.ListCatalogGrants(r.Context(), entry.ID)
	if err != nil {
		s.fail(w, err)
		return store.CatalogEntry{}, nil, false
	}
	return entry, grants, true
}

func (s *Server) digestAllowed(w http.ResponseWriter, r *http.Request, entryID, hostID uuid.UUID, digest string) bool {
	items, err := s.Store.RollbackDigests(r.Context(), entryID, hostID)
	if err != nil {
		s.fail(w, err)
		return false
	}
	for _, item := range items {
		if item == digest {
			return true
		}
	}
	writeCode(w, "bad_request", "invalid digest")
	return false
}

func (s *Server) imageNeedsKey(ctx context.Context, projectID uuid.UUID, img catalog.Image) (bool, error) {
	if img.ReferencesEnv() {
		return true, nil
	}
	needs, err := s.filesNeedKey(ctx, projectID, img)
	if err != nil || needs {
		return needs, err
	}
	_, err = s.Store.GetRegistryMaterial(ctx, projectID, img.RegistryHost)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Server) imageTask(ctx context.Context, projectID uuid.UUID, host store.Host, entry store.CatalogEntry, img catalog.Image, action string, force bool, digest string, lines int) (map[string]any, []secretForm, error) {
	project, err := s.Store.GetProject(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	switch action {
	case "deploy", "rollback":
		merged, err := s.mergeImageEnv(ctx, projectID, img)
		if err != nil {
			return nil, nil, err
		}
		env, secretKeys := merged.env, merged.secretKeys
		spec, err := storedSpec(img, host)
		if err != nil {
			return nil, nil, err
		}
		// api-v1 v1.43: the token sent must outlive the agent's pull.
		reg, err := s.registryAuth(ctx, projectID, img.RegistryHost, refreshOnTask)
		if err != nil {
			return nil, nil, err
		}
		files, fileSecrets, err := s.resolveFiles(ctx, projectID, img)
		other := append([]string{}, fileSecrets...)
		other = append(other, secretValues(nil, nil, reg)...)
		forms := mergeSecretForms(merged.forms(), redactedValueForms(other))
		if err != nil {
			return nil, forms, err
		}
		out := map[string]any{
			"project":       project.Slug,
			"entry":         entry.Name,
			"spec":          spec,
			"env":           env,
			"registry_auth": reg,
			"force":         force,
			"spec_hash":     entry.SpecHash,
		}
		// A wave 2a agent rejects an unknown key. Absent files means [].
		// secret_keys is sent only when this host stored programs.files,
		// and then always, including an empty list.
		if len(files) > 0 {
			out["files"] = files
		}
		if host.Programs != nil && host.Programs.Files {
			out["secret_keys"] = secretKeys
		}
		// agent-protocol v1.14: env_hash only to an agent that advertised it.
		// Without the secrets key there is no hash key; the task is then the
		// v1.9 shape, which the agent handles as before.
		if host.Programs != nil && host.Programs.EnvHash && s.Secrets != nil {
			hash, err := s.Secrets.EnvHash(env)
			if err != nil {
				return nil, forms, err
			}
			out["env_hash"] = hash
		}
		if action == "rollback" {
			out["digest"] = digest
		}
		return out, forms, nil
	case "logs":
		return map[string]any{"project": project.Slug, "entry": entry.Name, "lines": lines}, nil, nil
	default:
		return map[string]any{"project": project.Slug, "entry": entry.Name}, nil, nil
	}
}

// redactValues loads secret env values that opened, the matching registry
// token when it opens, and secret file pieces. Each source is collected on
// its own: an env or registry failure does not drop file pieces that opened.
// Plain env-set values and inline env are not included unless they
// reference a secret key (api-v1 v1.27 item 7). The task frame is
// not changed. A missing key returns nothing; image_logs is refused before
// this runs when the entry references a secret env key or a secret file. A
// decrypt failure logs once, with no value. A missing or undecryptable file
// is skipped; every secret that opened is still returned. The same set is
// used whether or not the frame sends secret_keys.
func (s *Server) redactValues(ctx context.Context, projectID uuid.UUID, img catalog.Image) []secretForm {
	forms, _ := s.redactValuesCheck(ctx, projectID, img)
	return forms
}

// redactValuesCheck is redactValues that also reports whether a secret
// could not be opened (a decrypt failure), so a caller can fail closed.
func (s *Server) redactValuesCheck(ctx context.Context, projectID uuid.UUID, img catalog.Image) ([]secretForm, bool) {
	var envForms []secretForm
	var other []string
	failed := false
	if s.Secrets != nil {
		// A successful merge gives exactly the deploy's secret values,
		// including a resolved value that references a secret (v1.27 item 7).
		// When it fails (a decrypt, a missing source, a bad reference), the
		// best-effort walk still hides every secret that opens.
		if merged, err := s.mergeImageEnv(ctx, projectID, img); err == nil {
			envForms = merged.forms()
		} else {
			typed, generated, envFailed := s.redactEnvValues(ctx, projectID, img.EnvRefs())
			envForms = envSecretForms(typed, generated)
			failed = envFailed
		}
		reg, err := s.registryAuth(ctx, projectID, img.RegistryHost, refreshNever)
		if errors.Is(err, errDecrypt) {
			failed = true
		} else if err == nil {
			other = append(other, secretValues(nil, nil, reg)...)
		}
	}
	more, ferr := s.secretFileValues(ctx, projectID, img)
	if errors.Is(ferr, errDecrypt) {
		failed = true
	}
	if failed {
		s.logger().Error("decrypt failed")
	}
	other = append(other, more...)
	return mergeSecretForms(envForms, redactedValueForms(other)), failed
}

// redactEnvValues returns secret env values whose winning material opened.
// Precedence matches mergeEnv. A later set, secret_env, or inline env
// replaces an earlier key. A winning secret that does not decrypt is omitted.
// A store error skips that set and keeps values already opened. Deploy still
// uses mergeEnv, which fails closed. Values are split into typed and
// generated ones, whose replacement is four bullets (v1.27 item 2).
func (s *Server) redactEnvValues(ctx context.Context, projectID uuid.UUID, refs catalog.EnvRefs) ([]string, []string, bool) {
	out := map[string]string{}
	gen := map[string]bool{}
	failed := false
	for _, set := range refs.Sets {
		mats, err := s.Store.ListEnvMaterial(ctx, projectID, set)
		if err != nil {
			continue
		}
		for _, m := range mats {
			if !m.IsSecret {
				delete(out, m.Key)
				continue
			}
			val, err := s.openEnv(projectID, set, m)
			if err != nil {
				if errors.Is(err, errDecrypt) {
					failed = true
				}
				delete(out, m.Key)
				continue
			}
			out[m.Key] = val
			gen[m.Key] = m.Generated
		}
	}
	for key, ref := range refs.Secret {
		set, name, ok := strings.Cut(ref, ".")
		if !ok {
			failed = true
			delete(out, key)
			continue
		}
		mats, err := s.Store.ListEnvMaterial(ctx, projectID, set)
		if err != nil {
			delete(out, key)
			continue
		}
		found := false
		for _, m := range mats {
			if m.Key != name {
				continue
			}
			found = true
			if !m.IsSecret {
				delete(out, key)
				break
			}
			val, err := s.openEnv(projectID, set, m)
			if err != nil {
				if errors.Is(err, errDecrypt) {
					failed = true
				}
				delete(out, key)
				break
			}
			out[key] = val
			gen[key] = m.Generated
			break
		}
		if !found {
			failed = true
			delete(out, key)
		}
	}
	for key := range refs.Inline {
		delete(out, key)
	}
	var typed, generated []string
	for key, val := range out {
		if gen[key] {
			generated = append(generated, val)
		} else {
			typed = append(typed, val)
		}
	}
	return typed, generated, failed
}

// envMerge is the merged env of one image task or program Run.
type envMerge struct {
	env map[string]string
	// secretKeys is sorted and never nil.
	secretKeys []string
	// generated marks secret keys whose value is, or references, an
	// API-generated value. Their replacement in task output is four
	// bullets, never a preview (v1.27 item 2).
	generated map[string]bool
}

// forms is the redaction set for the merged secret values.
func (m envMerge) forms() []secretForm {
	var typed, generated []string
	for _, key := range m.secretKeys {
		if m.generated[key] {
			generated = append(generated, m.env[key])
		} else {
			typed = append(typed, m.env[key])
		}
	}
	return envSecretForms(typed, generated)
}

// envSecretForms redacts typed secrets with their preview and generated
// ones with four bullets. A form in both groups keeps the bullets.
func envSecretForms(typed, generated []string) []secretForm {
	return mergeSecretForms(valueForms(generated, catalog.SecretPreview("")), envValueForms(typed))
}

// mergeImageEnv is mergeEnv followed by one pass of ${KEY} references
// (api-v1 v1.27 item 7). secretKeys and generated then also cover the keys
// whose value references a secret or generated key. A bad reference is a
// *catalog.RefError (see refCall). Program env stays literal: program Runs
// call mergeEnv.
func (s *Server) mergeImageEnv(ctx context.Context, projectID uuid.UUID, img catalog.Image) (envMerge, error) {
	msg, err := s.unfilledReason(ctx, projectID, img.EnvRefs())
	if err != nil {
		return envMerge{}, err
	}
	if msg != "" {
		return envMerge{}, &catalog.RefError{Msg: msg}
	}
	merged, err := s.mergeEnv(ctx, projectID, img.EnvRefs())
	if err != nil {
		return envMerge{}, err
	}
	secret := make(map[string]bool, len(merged.env))
	for _, key := range merged.secretKeys {
		secret[key] = true
	}
	resolved, err := catalog.ResolveEnvRefs(merged.env, secret)
	if err != nil {
		return envMerge{}, err
	}
	return newEnvMerge(resolved.Env, resolved.Spread(secret), resolved.Spread(merged.generated)), nil
}

// mergeEnv resolves env sets in order, then secret_env, then inline env,
// with no reference resolution. secretKeys are the keys whose winning value
// came from an env-set key with is_secret, and which inline env did not
// override. Image deploys (through mergeImageEnv) and program Runs share it
// (api-v1 v1.22 item 3).
func (s *Server) mergeEnv(ctx context.Context, projectID uuid.UUID, refs catalog.EnvRefs) (envMerge, error) {
	return s.mergeEnvWith(ctx, projectID, refs, false)
}

// mergeEnvWith is mergeEnv. With tolerant (the readiness check, api-v1 v1.29
// item 7) a missing set or secret_env source is skipped, and a needs_value
// placeholder counts as set with an empty value instead of being opened, so
// the ${KEY} check sees the same precedence as a deploy without failing on
// what the other checks already report.
func (s *Server) mergeEnvWith(ctx context.Context, projectID uuid.UUID, refs catalog.EnvRefs, tolerant bool) (envMerge, error) {
	out := map[string]string{}
	secret := map[string]bool{}
	generated := map[string]bool{}
	material := func(set string) ([]store.EnvMaterial, bool, error) {
		mats, err := s.Store.ListEnvMaterial(ctx, projectID, set)
		if tolerant && errors.Is(err, store.ErrNotFound) {
			return nil, false, nil
		}
		return mats, err == nil, err
	}
	open := func(set string, m store.EnvMaterial) (string, error) {
		if tolerant && m.NeedsValue {
			return "", nil
		}
		return s.openEnv(projectID, set, m)
	}
	for _, set := range refs.Sets {
		mats, ok, err := material(set)
		if err != nil {
			return envMerge{}, err
		}
		if !ok {
			continue
		}
		for _, m := range mats {
			val, err := open(set, m)
			if err != nil {
				return envMerge{}, err
			}
			out[m.Key] = val
			secret[m.Key] = m.IsSecret
			generated[m.Key] = m.Generated
		}
	}
	for key, ref := range refs.Secret {
		set, name, ok := strings.Cut(ref, ".")
		if !ok {
			return envMerge{}, errDecrypt
		}
		mats, exists, err := material(set)
		if err != nil {
			return envMerge{}, err
		}
		if !exists {
			continue
		}
		found := false
		for _, m := range mats {
			if m.Key != name {
				continue
			}
			val, err := open(set, m)
			if err != nil {
				return envMerge{}, err
			}
			out[key] = val
			secret[key] = m.IsSecret
			generated[key] = m.Generated
			found = true
			break
		}
		if !found && !tolerant {
			return envMerge{}, errDecrypt
		}
	}
	for key, val := range refs.Inline {
		out[key] = val
		secret[key] = false
		generated[key] = false
	}
	return newEnvMerge(out, secret, generated), nil
}

// unfilledReason is the api-v1 v1.27 item 6 refusal for an entry whose env
// sets or secret_env sources hold a needs_value placeholder, or "". It
// names the first such key in env_sets order (keys sorted), else the
// source of the first secret_env key in key order. Nothing is decrypted.
// Image deploys and rollbacks (mergeImageEnv) and program Runs and
// Restarts (programGate) refuse it. A missing set or source is left to the
// merge, which reports it.
func (s *Server) unfilledReason(ctx context.Context, projectID uuid.UUID, refs catalog.EnvRefs) (string, error) {
	for _, set := range refs.Sets {
		mats, err := s.Store.ListEnvMaterial(ctx, projectID, set)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return "", err
		}
		for _, m := range mats {
			if m.NeedsValue {
				return needsValueMsg(m.Key, set), nil
			}
		}
	}
	keys := make([]string, 0, len(refs.Secret))
	for key := range refs.Secret {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		set, name, ok := strings.Cut(refs.Secret[key], ".")
		if !ok {
			continue
		}
		mats, err := s.Store.ListEnvMaterial(ctx, projectID, set)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return "", err
		}
		for _, m := range mats {
			if m.Key == name && m.NeedsValue {
				return needsValueMsg(m.Key, set), nil
			}
		}
	}
	return "", nil
}

// needsValueMsg is the api-v1 v1.27 item 6 refusal. It names the key and
// the set only.
func needsValueMsg(key, set string) string {
	return fmt.Sprintf("env %s in set %s has no value yet; set it in the console", key, set)
}

// newEnvMerge builds an envMerge from the merged env and its per-key flags.
func newEnvMerge(env map[string]string, secret, generated map[string]bool) envMerge {
	keys := make([]string, 0)
	for key, isSecret := range secret {
		if isSecret {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return envMerge{env: env, secretKeys: keys, generated: generated}
}

func secretValues(env map[string]string, keys []string, reg any) []string {
	secrets := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		secrets = append(secrets, env[key])
	}
	if regMap, ok := reg.(map[string]any); ok {
		if tok, ok := regMap["token"].(string); ok {
			secrets = append(secrets, tok)
		}
	}
	return secrets
}

func (s *Server) projectFileNames(ctx context.Context, projectID uuid.UUID) (map[string]struct{}, error) {
	items, err := s.Store.ListProjectFiles(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := map[string]struct{}{}
	for _, item := range items {
		out[item.Name] = struct{}{}
	}
	return out, nil
}

func filesReady(host store.Host, img catalog.Image) bool {
	if len(img.Files) == 0 {
		return true
	}
	return host.Programs != nil && host.Programs.Files
}

func healthcheckReady(host store.Host, img catalog.Image) bool {
	if img.Healthcheck == nil {
		return true
	}
	return host.Programs != nil && host.Programs.Healthcheck
}

func loggingReady(host store.Host, img catalog.Image) bool {
	if img.Logging == nil {
		return true
	}
	return host.Programs != nil && host.Programs.Logging
}

// policyReady gates only a health_policy that differs from the defaults
// (v1.15 item 2). An entry at the defaults deploys everywhere.
func policyReady(host store.Host, img catalog.Image) bool {
	if catalog.PolicyIsDefault(img) {
		return true
	}
	return policySupported(host)
}

// networksAllowed is the api-v1 v1.25 item 3 gate for deploy and rollback
// of an entry with networks. It looks up the project slug only when the
// entry has networks. false means the refusal was written and audited.
func (s *Server) networksAllowed(w http.ResponseWriter, r *http.Request, p principal, projectID uuid.UUID, host store.Host, entryName, action string, img catalog.Image) bool {
	if len(img.Networks) == 0 {
		return true
	}
	project, err := s.Store.GetProject(r.Context(), projectID)
	if err != nil {
		s.fail(w, err)
		return false
	}
	if msg := networksReason(host, img, project.Slug); msg != "" {
		s.imageDeny(w, r, p, projectID, host.ID, host.Name, entryName, action, "bad_request", msg)
		return false
	}
	return true
}

// networksReason is the refusal for an entry's networks on host, or "" when
// they are allowed: the host's agent must report shared_networks, and each
// name must be defined there with slug as a member.
func networksReason(host store.Host, img catalog.Image, slug string) string {
	if len(img.Networks) == 0 {
		return ""
	}
	if host.Programs == nil || host.Programs.SharedNetworks == nil {
		return reasonNoNetworks
	}
	if name := closedNetwork(host.Programs.SharedNetworks, img.Networks, slug); name != "" {
		return reasonNetworkClosed(name)
	}
	return ""
}

// closedNetwork is the first name in want that the host does not define or
// that does not list slug, or "" when all are open.
func closedNetwork(defined []store.SharedNetwork, want []string, slug string) string {
	for _, name := range want {
		open := false
		for _, n := range defined {
			if n.Name == name {
				open = slices.Contains(n.Projects, slug)
				break
			}
		}
		if !open {
			return name
		}
	}
	return ""
}

// hostNetworkReady is the api-v1 v1.50 item 1 gate: a deploy, rollback or
// run of an entry with network "host" needs the host's hello
// programs.host_network. Other actions and entries pass.
func hostNetworkReady(host store.Host, img catalog.Image, action string) bool {
	if img.Network != catalog.NetworkHost {
		return true
	}
	if action != "deploy" && action != "rollback" && action != "run" {
		return true
	}
	return host.Programs != nil && host.Programs.HostNetwork
}

func policySupported(host store.Host) bool {
	return host.Programs != nil && host.Programs.HealthPolicy
}

// logsNeedKey reports whether image_logs would have to hide a secret env
// value or a secret file. Precedence matches mergeEnv and does not decrypt.
// A plain winning value, a missing secret_env reference, and a registry
// credential do not count. Inline env clears the flag.
func (s *Server) logsNeedKey(ctx context.Context, projectID uuid.UUID, img catalog.Image) (bool, error) {
	needs, err := s.envNeedsKey(ctx, projectID, img.EnvRefs())
	if err != nil || needs {
		return needs, err
	}
	return s.filesNeedKey(ctx, projectID, img)
}

// envNeedsKey reports whether a winning value of the merge comes from an
// env-set key marked secret, so redacting output needs the secrets key.
func (s *Server) envNeedsKey(ctx context.Context, projectID uuid.UUID, refs catalog.EnvRefs) (bool, error) {
	secret := map[string]bool{}
	for _, set := range refs.Sets {
		mats, err := s.Store.ListEnvMaterial(ctx, projectID, set)
		if err != nil {
			return false, err
		}
		for _, m := range mats {
			secret[m.Key] = m.IsSecret
		}
	}
	for key, ref := range refs.Secret {
		set, name, ok := strings.Cut(ref, ".")
		if !ok {
			continue
		}
		mats, err := s.Store.ListEnvMaterial(ctx, projectID, set)
		if err != nil {
			return false, err
		}
		for _, m := range mats {
			if m.Key != name {
				continue
			}
			secret[key] = m.IsSecret
			break
		}
	}
	for key := range refs.Inline {
		secret[key] = false
	}
	for _, isSecret := range secret {
		if isSecret {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) filesNeedKey(ctx context.Context, projectID uuid.UUID, img catalog.Image) (bool, error) {
	if len(img.Files) == 0 {
		return false, nil
	}
	mats, err := s.Store.ListFileMaterial(ctx, projectID)
	if err != nil {
		return false, err
	}
	by := map[string]store.FileMaterial{}
	for _, mat := range mats {
		by[mat.Name] = mat
	}
	for _, row := range img.Files {
		mat, ok := by[row.File]
		if ok && mat.IsSecret {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) resolveFiles(ctx context.Context, projectID uuid.UUID, img catalog.Image) ([]map[string]any, []string, error) {
	if len(img.Files) == 0 {
		return []map[string]any{}, nil, nil
	}
	mats, err := s.Store.ListFileMaterial(ctx, projectID)
	if err != nil {
		return nil, nil, err
	}
	by := map[string]store.FileMaterial{}
	for _, mat := range mats {
		by[mat.Name] = mat
	}
	out := make([]map[string]any, 0, len(img.Files))
	var secrets []string
	for _, row := range img.Files {
		mat, ok := by[row.File]
		if !ok {
			return nil, secrets, &agentws.CallError{Code: "bad_request", Msg: "file " + row.File + " no longer exists"}
		}
		content, err := s.openFile(projectID, mat)
		if err != nil {
			return nil, secrets, err
		}
		if mat.IsSecret {
			secrets = append(secrets, secretFilePieces(content)...)
		}
		out = append(out, map[string]any{
			"name":    row.File,
			"target":  row.Target,
			"content": content,
			"secret":  mat.IsSecret,
		})
	}
	return out, secrets, nil
}

func (s *Server) secretFileValues(ctx context.Context, projectID uuid.UUID, img catalog.Image) ([]string, error) {
	if len(img.Files) == 0 || s.Secrets == nil {
		return nil, nil
	}
	mats, err := s.Store.ListFileMaterial(ctx, projectID)
	if err != nil {
		return nil, err
	}
	by := map[string]store.FileMaterial{}
	for _, mat := range mats {
		by[mat.Name] = mat
	}
	var secrets []string
	var failed error
	for _, row := range img.Files {
		mat, ok := by[row.File]
		if !ok || !mat.IsSecret {
			continue
		}
		content, err := s.openFile(projectID, mat)
		if err != nil {
			if errors.Is(err, errDecrypt) {
				failed = err
			}
			continue
		}
		secrets = append(secrets, secretFilePieces(content)...)
	}
	return secrets, failed
}

func (s *Server) openFile(projectID uuid.UUID, m store.FileMaterial) (string, error) {
	if !m.IsSecret {
		return m.Plain, nil
	}
	if s.Secrets == nil {
		return "", errSecretsKey
	}
	plain, err := s.Secrets.Open(seal.FileAAD(projectID.String(), m.Name), m.Nonce, m.Ciphertext)
	if err != nil {
		return "", errDecrypt
	}
	return string(plain), nil
}

func (s *Server) openEnv(projectID uuid.UUID, set string, m store.EnvMaterial) (string, error) {
	if !m.IsSecret {
		return m.Plain, nil
	}
	if s.Secrets == nil {
		return "", errSecretsKey
	}
	plain, err := s.Secrets.Open(seal.EnvAAD(projectID.String(), set, m.Key), m.Nonce, m.Ciphertext)
	if err != nil {
		return "", errDecrypt
	}
	return string(plain), nil
}

// registryAuth is the task frame's registry_auth, or nil without a stored
// login. A github token with less than within left is refreshed first
// (api-v1 v1.43); refreshNever opens it as stored.
func (s *Server) registryAuth(ctx context.Context, projectID uuid.UUID, host string, within time.Duration) (any, error) {
	mat, token, err := s.registryToken(ctx, projectID, host, within)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"host": host, "username": mat.Username, "token": token}, nil
}

func (s *Server) openRegistry(projectID uuid.UUID, m store.RegistryMaterial) (string, error) {
	if s.Secrets == nil {
		return "", errSecretsKey
	}
	plain, err := s.Secrets.Open(seal.RegistryAAD(projectID.String(), m.Host), m.Nonce, m.Ciphertext)
	if err != nil {
		return "", errDecrypt
	}
	return string(plain), nil
}

// storedSpec is the entry's ImageSpec for the task frame. Inline env, env_sets,
// and secret_env stay references. Resolved values travel only in the task's
// top-level env. An empty files list is omitted; a wave 2a agent rejects the key.
// health_policy is dropped for an agent without programs.health_policy; the
// gate has already refused a policy that differs from the defaults (v1.15).
func storedSpec(img catalog.Image, host store.Host) (map[string]any, error) {
	var spec map[string]any
	if err := json.Unmarshal([]byte(catalog.SpecJSON(img)), &spec); err != nil {
		return nil, err
	}
	if len(img.Files) == 0 {
		delete(spec, "files")
	}
	if !policySupported(host) {
		delete(spec, "health_policy")
	}
	agentSecretRefs(spec)
	return spec, nil
}

// agentSecretRefs rewrites entry-private secret_env references
// ("@<entry>.KEY", api-v1 v1.33 item 6) to "<entry>.KEY" in the frame spec.
// Every released agent checks each reference against the shared set-name
// grammar and refuses "@" with bad_spec. The agent never resolves a
// reference (resolved values travel only in the top-level env), so the
// rewritten name only has to pass that check. spec_hash stays the stored
// entry's hash; the agent keeps it as a label and does not recompute it.
func agentSecretRefs(spec map[string]any) {
	refs, ok := spec["secret_env"].(map[string]any)
	if !ok {
		return
	}
	for k, v := range refs {
		ref, ok := v.(string)
		if !ok {
			continue
		}
		if set, key, ok := strings.Cut(ref, "."); ok && catalog.IsPrivateSet(set) {
			refs[k] = catalog.PrivateEntry(set) + "." + key
		}
	}
}

// imageListed is the api-v1 v1.23 pre-check for image deploy, rollback and
// restart: a host whose projects allowlist is known and lacks the project is
// refused before any task. false means the response was written.
func (s *Server) imageListed(w http.ResponseWriter, r *http.Request, p principal, projectID uuid.UUID, host store.Host, entry store.CatalogEntry, action string) bool {
	if action != "deploy" && action != "rollback" && action != "restart" {
		return true
	}
	msg, err := s.unlistedReason(r.Context(), host, projectID)
	if err != nil {
		s.fail(w, err)
		return false
	}
	if msg != "" {
		s.imageDeny(w, r, p, projectID, host.ID, host.Name, entry.Name, action, "forbidden", msg)
		return false
	}
	return true
}

func containersReady(host store.Host) bool {
	return host.Programs != nil && host.Programs.Images && host.Programs.Docker
}

// resultMargin is how long past the task budget the API waits for a
// deploy or rollback result (api-v1 v1.42 item 3). Other actions get none.
func (s *Server) resultMargin(action string) int {
	if action != "deploy" && action != "rollback" {
		return 0
	}
	if s.imageBudget > 0 {
		return s.imageMargin
	}
	return resultMarginSec
}

// imageRunning answers a begin refused because another deploy, rollback or
// remove holds the entry on that host (api-v1 v1.42 item 2). The names are
// the caller's own entry and host; no value enters the message. Nothing
// started, so the answer is not stored: the key is released and a retry with
// it after the operation ended runs. On a confirm the key held the 202 plan,
// whose confirmation is now used; releasing it lets that key plan again.
func (s *Server) imageRunning(w http.ResponseWriter, userID uuid.UUID, key, entryName, hostName string, running *store.ImageOpRunning) {
	started := running.StartedAt.UTC()
	until := running.Until.UTC()
	msg := fmt.Sprintf("A deploy, rollback or remove of %s on %s has been running since %s UTC. Try again after %s UTC.",
		entryName, hostName, started.Format("15:04"), until.Format("15:04"))
	body := envelopeRaw("conflict", msg, map[string]string{
		"started_at": started.Format(time.RFC3339),
		"ends_by":    until.Format(time.RFC3339),
	})
	s.releaseKey(userID, key)
	writeJSON(w, statusFor("conflict"), body)
}

func imageTimeout(action string, img catalog.Image) int {
	switch action {
	case "deploy":
		return catalog.DeployTimeoutSec(img)
	case "rollback":
		return catalog.RollbackTimeoutSec(img)
	default:
		return catalog.ShortTimeoutSec()
	}
}

func imageKind(action string) string {
	switch action {
	case "deploy":
		return "image_deploy"
	case "rollback":
		return "image_rollback"
	case "remove":
		return "image_remove"
	case "run":
		return "image_start"
	case "stop":
		return "image_stop"
	case "restart":
		return "image_restart"
	case "logs":
		return "image_logs"
	default:
		return "image_status"
	}
}

func imageCtx(r *http.Request, action string, timeoutSec int) (context.Context, context.CancelFunc) {
	parent := r.Context()
	if action == "deploy" || action == "rollback" || action == "remove" {
		parent = context.WithoutCancel(r.Context())
	}
	return context.WithTimeout(parent, time.Duration(timeoutSec)*time.Second)
}

func imageRequestHash(action string, projectID, hostID uuid.UUID, entry string, force bool, digest string) []byte {
	if action == "rollback" {
		raw := `{"action":` + jsonQuote(action) +
			`,"digest":` + jsonQuote(digest) +
			`,"entry":` + jsonQuote(entry) +
			`,"host_id":` + jsonQuote(hostID.String()) +
			`,"project_id":` + jsonQuote(projectID.String()) + `}`
		return hashBytes(raw)
	}
	if action == "deploy" {
		flag := "false"
		if force {
			flag = "true"
		}
		raw := `{"action":` + jsonQuote(action) +
			`,"entry":` + jsonQuote(entry) +
			`,"force":` + flag +
			`,"host_id":` + jsonQuote(hostID.String()) +
			`,"project_id":` + jsonQuote(projectID.String()) + `}`
		return hashBytes(raw)
	}
	return programRequestHash(action, projectID, hostID, entry)
}

func hashBytes(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

type imageStatusBody struct {
	Container    *string `json:"container"`
	State        string  `json:"state"`
	ExitCode     *int    `json:"exit_code"`
	RestartCount int     `json:"restart_count"`
	StartedAt    *string `json:"started_at"`
	FinishedAt   *string `json:"finished_at"`
	Digest       *string `json:"digest"`
	Ports        []int   `json:"ports"`
}

type imageLogsBody struct {
	Container *string  `json:"container"`
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}

type imageCall struct {
	request bool
	code    string
	msg     string
	// reason is what an audit row stores. It is the fixed phrase when msg was redacted.
	reason  string
	outcome string
	digest  *string
	exit    *int
	status  imageStatusBody
	lines   []string
	logs    imageLogsBody
	// agentReason is the agent's own reason for the outcome, checked and
	// redacted (agent-protocol v1.19); "" when it sent none.
	agentReason string
	// maybeSent: the host became unavailable after the frame may have
	// been sent, so the task may have run. Such a row is not refused.
	maybeSent bool
}

func takeImage(action string, res agentws.LiveResult, err error, forms []secretForm) imageCall {
	if action == "logs" {
		return takeLogs(res, err, forms)
	}
	if action == "run" || action == "stop" || action == "restart" || action == "status" || action == "remove" {
		return takeStatus(action == "remove", res, err, forms)
	}
	return takeDeploy(res, err, forms)
}

// msgSpecRefused is the deploy and rollback message for the agent's
// task_reject bad_spec, which carries only the code.
const msgSpecRefused = "the host's agent refused the task as malformed (bad_spec): it did not accept a field of this entry's spec, env or files; the agent does not say which. Check that the agent is up to date (upgrade_agent), then compare the spec with the ImageSpec rules"

func takeDeploy(res agentws.LiveResult, err error, forms []secretForm) imageCall {
	if err == nil {
		if got, ok := acceptedDeploy(res.Payload, forms); ok {
			return got
		}
		return failedFixed()
	}
	var call *agentws.CallError
	if !errors.As(err, &call) {
		return imageCall{request: true, code: "error", msg: "internal error"}
	}
	if hostRejection(call.Code) {
		if call.Agent == "bad_spec" {
			// The agent's task_reject names no field; say what the code means.
			return imageCall{request: true, code: call.Code, msg: msgSpecRefused, reason: msgSpecRefused, maybeSent: call.MaybeSent}
		}
		return requestCall(call, forms)
	}
	if got, ok := acceptedDeploy(call.Payload, forms); ok {
		return got
	}
	if call.Code == "timeout" {
		return imageCall{outcome: "timeout", status: timeoutStatus(), lines: []string{}}
	}
	return failedFixed()
}

func takeStatus(remove bool, res agentws.LiveResult, err error, forms []secretForm) imageCall {
	if err == nil {
		p, ok := parseAgentImage(res.Payload)
		st, stOK := normalizeStatus(p.Status)
		if !ok || !stOK {
			return imageCall{request: true, code: "error", msg: invalidAgentResult}
		}
		return imageCall{outcome: "succeeded", status: redactStatus(st, forms), lines: []string{}}
	}
	var call *agentws.CallError
	if !errors.As(err, &call) {
		return imageCall{request: true, code: "error", msg: "internal error"}
	}
	if call.Code == "timeout" {
		return imageCall{request: true, code: "timeout", msg: phrase("timeout")}
	}
	if hostRejection(call.Code) {
		return requestCall(call, forms)
	}
	if p, ok := parseAgentImage(call.Payload); ok {
		if st, stOK := normalizeStatus(p.Status); stOK {
			outcome := "failed"
			if imageOutcomeOK(call.Msg) && call.Msg != "succeeded" {
				outcome = call.Msg
			}
			return imageCall{outcome: outcome, status: redactStatus(st, forms), lines: []string{}}
		}
	}
	return agentError(call, forms)
}

func takeLogs(res agentws.LiveResult, err error, forms []secretForm) imageCall {
	if err != nil {
		var call *agentws.CallError
		if !errors.As(err, &call) {
			return imageCall{request: true, code: "error", msg: "internal error"}
		}
		if call.Code == "timeout" {
			return imageCall{request: true, code: "timeout", msg: phrase("timeout")}
		}
		if hostRejection(call.Code) {
			return requestCall(call, forms)
		}
		return agentError(call, forms)
	}
	p, ok := parseAgentImage(res.Payload)
	lines, linesOK := boundLines(p.Lines, maxLogLines)
	if !ok || !linesOK || !statusTextOK(p.Container) {
		return imageCall{request: true, code: "error", msg: invalidAgentResult}
	}
	return imageCall{logs: imageLogsBody{Container: redactText(p.Container, forms), Lines: redactLines(lines, forms), Truncated: p.Truncated}}
}

func hostRejection(code string) bool {
	switch code {
	case "unavailable", "forbidden", "bad_request", "not_found":
		return true
	default:
		return false
	}
}

func requestCall(call *agentws.CallError, forms []secretForm) imageCall {
	original := call.Msg
	if original == "" {
		original = phrase(call.Code)
	}
	shown := redactExact(original, forms)
	reason := shown
	if shown != original {
		reason = phrase(call.Code)
	}
	code := call.Code
	if code == "" {
		code = "error"
	}
	return imageCall{request: true, code: code, msg: shown, reason: reason, maybeSent: call.MaybeSent}
}

// agentError returns the redacted agent message when it contains a known secret.
// Anything else stays the fixed invalid-result line and is not echoed.
func agentError(call *agentws.CallError, forms []secretForm) imageCall {
	if call != nil && call.Msg != "" && redactExact(call.Msg, forms) != call.Msg {
		return requestCall(call, forms)
	}
	return imageCall{request: true, code: "error", msg: invalidAgentResult}
}

func acceptedDeploy(raw json.RawMessage, forms []secretForm) (imageCall, bool) {
	p, ok := parseAgentImage(raw)
	if !ok || !imageOutcomeOK(p.Outcome) {
		return imageCall{}, false
	}
	st, stOK := normalizeStatus(p.Status)
	if !stOK {
		return imageCall{}, false
	}
	if p.Digest != nil && !catalog.ValidDigest(*p.Digest) {
		return imageCall{}, false
	}
	lines, linesOK := boundLines(p.Lines, maxAgentLines)
	if !linesOK {
		return imageCall{}, false
	}
	got := imageCall{
		outcome: p.Outcome,
		digest:  cleanDigest(p.Digest),
		exit:    p.ExitCode,
		status:  redactStatus(st, forms),
		lines:   redactLines(lines, forms),
	}
	if agentReasonOK(p.Reason) {
		got.agentReason = redactExact(*p.Reason, forms)
	}
	return got, true
}

func failedFixed() imageCall {
	return imageCall{outcome: "failed", status: blankStatus(), lines: []string{invalidAgentResult}}
}

type agentImage struct {
	Outcome   string          `json:"outcome"`
	Digest    *string         `json:"digest"`
	ExitCode  *int            `json:"exit_code"`
	Status    json.RawMessage `json:"status"`
	Lines     []string        `json:"lines"`
	Container *string         `json:"container"`
	Truncated bool            `json:"truncated"`
	Reason    *string         `json:"reason"`
}

func parseAgentImage(raw json.RawMessage) (agentImage, bool) {
	if len(raw) == 0 || !json.Valid(raw) || string(raw) == "null" {
		return agentImage{}, false
	}
	var p agentImage
	if err := json.Unmarshal(raw, &p); err != nil {
		return agentImage{}, false
	}
	return p, true
}

func imageOutcomeOK(s string) bool {
	switch s {
	case "succeeded", "unchanged", "failed", "timeout", "rolled_back", "rollback_failed", "image_unavailable", "forbidden_spec", "conflict":
		return true
	default:
		return false
	}
}

func cleanDigest(d *string) *string {
	if d == nil || !catalog.ValidDigest(*d) {
		return nil
	}
	cp := *d
	return &cp
}

func blankStatus() imageStatusBody {
	return imageStatusBody{Ports: []int{}}
}

// timeoutStatus is the deploy or rollback result when the task times out
// with no usable agent payload. The agent cannot report this state.
func timeoutStatus() imageStatusBody {
	return imageStatusBody{State: "unknown", Ports: []int{}}
}

// Agent results stay on the v1.8 enum. "unknown" is only timeoutStatus.
func statusStateOK(state string) bool {
	switch state {
	case "running", "exited", "created", "paused", "restarting", "dead", "missing":
		return true
	default:
		return false
	}
}

func normalizeStatus(raw json.RawMessage) (imageStatusBody, bool) {
	if len(raw) == 0 || string(raw) == "null" || !json.Valid(raw) {
		return imageStatusBody{}, false
	}
	var st imageStatusBody
	if err := json.Unmarshal(raw, &st); err != nil {
		return imageStatusBody{}, false
	}
	if !statusStateOK(st.State) {
		return imageStatusBody{}, false
	}
	if st.Ports == nil {
		st.Ports = []int{}
	}
	for _, port := range st.Ports {
		if port < 1 || port > 65535 {
			return imageStatusBody{}, false
		}
	}
	if len(st.Ports) > 64 {
		return imageStatusBody{}, false
	}
	if st.Digest != nil && !catalog.ValidDigest(*st.Digest) {
		return imageStatusBody{}, false
	}
	if !statusTextOK(st.Container) || !statusTextOK(st.StartedAt) || !statusTextOK(st.FinishedAt) {
		return imageStatusBody{}, false
	}
	if st.RestartCount < 0 {
		return imageStatusBody{}, false
	}
	return st, true
}

func statusTextOK(s *string) bool {
	if s == nil {
		return true
	}
	if len(*s) > maxStatusBytes {
		return false
	}
	for i := 0; i < len(*s); i++ {
		if (*s)[i] < 0x20 || (*s)[i] > 0x7e {
			return false
		}
	}
	return true
}

func boundLines(lines []string, maxCount int) ([]string, bool) {
	if lines == nil {
		return []string{}, true
	}
	if len(lines) > maxCount {
		return nil, false
	}
	total := 0
	for _, line := range lines {
		n := utf8.RuneCountInString(line)
		if n > maxAgentLineChars {
			return nil, false
		}
		total += n
		if total > maxAgentLineTotal {
			return nil, false
		}
	}
	return lines, true
}

// secretFilePieces is agent-protocol v1.5 exact-value redaction for one secret
// file, before raw, base64, and JSON-escaped forms. Whole content is kept at
// 6–4096 bytes. Trimmed lines, JSON string values, and trimmed lines inside
// those values are kept at 16–4096 bytes. A shorter secret file adds nothing.
func secretFilePieces(content string) []string {
	var out []string
	if n := len(content); n >= secretRedactMin && n <= secretPieceMax {
		out = append(out, content)
	}
	out = append(out, trimmedSecretLines(content)...)
	var doc any
	if json.Unmarshal([]byte(content), &doc) == nil {
		out = append(out, jsonSecretPieces(doc)...)
	}
	return out
}

func trimmedSecretLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if n := len(line); n >= secretLineMin && n <= secretPieceMax {
			out = append(out, line)
		}
	}
	return out
}

func jsonSecretPieces(v any) []string {
	var out []string
	switch x := v.(type) {
	case string:
		if n := len(x); n >= secretLineMin && n <= secretPieceMax {
			out = append(out, x)
		}
		out = append(out, trimmedSecretLines(x)...)
	case map[string]any:
		for _, child := range x {
			out = append(out, jsonSecretPieces(child)...)
		}
	case []any:
		for _, child := range x {
			out = append(out, jsonSecretPieces(child)...)
		}
	}
	return out
}

func redactLines(lines []string, forms []secretForm) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = redactExact(line, forms)
	}
	return out
}

// secretForm is one exact string and the text that replaces it.
// An env-set secret uses catalog.SecretPreview. A registry token or a
// secret-file piece uses redactedMarker. Forms are longest-first.
// The same form from both sources keeps redactedMarker.
type secretForm struct {
	form string
	with string
}

func secretForms(values []string) []secretForm {
	return redactedValueForms(values)
}

func envValueForms(values []string) []secretForm {
	return valueForms(values, "")
}

func redactedValueForms(values []string) []secretForm {
	return valueForms(values, redactedMarker)
}

func valueForms(values []string, with string) []secretForm {
	var out []secretForm
	for _, value := range values {
		repl := with
		if repl == "" {
			repl = catalog.SecretPreview(value)
		}
		out = append(out, expandForms(value, repl)...)
	}
	return mergeSecretForms(out)
}

func expandForms(value, with string) []secretForm {
	if len(value) < secretRedactMin {
		return nil
	}
	var out []secretForm
	add := func(form string) {
		if len(form) < secretRedactMin {
			return
		}
		out = append(out, secretForm{form: form, with: with})
	}
	add(value)
	add(base64.StdEncoding.EncodeToString([]byte(value)))
	add(base64.RawStdEncoding.EncodeToString([]byte(value)))
	add(base64.URLEncoding.EncodeToString([]byte(value)))
	add(base64.RawURLEncoding.EncodeToString([]byte(value)))
	quoted, err := json.Marshal(value)
	if err == nil && len(quoted) >= 2 {
		add(string(quoted[1 : len(quoted)-1]))
	}
	return out
}

func mergeSecretForms(groups ...[]secretForm) []secretForm {
	with := map[string]string{}
	order := []string{}
	for _, group := range groups {
		for _, item := range group {
			if len(item.form) < secretRedactMin {
				continue
			}
			prev, ok := with[item.form]
			if !ok {
				with[item.form] = item.with
				order = append(order, item.form)
				continue
			}
			if prev != redactedMarker && item.with == redactedMarker {
				with[item.form] = redactedMarker
			}
		}
	}
	sort.Slice(order, func(i, j int) bool { return len(order[i]) > len(order[j]) })
	out := make([]secretForm, 0, len(order))
	for _, form := range order {
		out = append(out, secretForm{form: form, with: with[form]})
	}
	return out
}

// redactExact replaces each secret form, then applies the keyword rule.
// Env-set secrets use their preview. Registry tokens and secret-file pieces
// use [REDACTED], the marker the agent writes. A different case makes Logs
// and the deploy tail disagree.
func redactExact(s string, forms []secretForm) string {
	for _, item := range forms {
		if item.form == "" {
			continue
		}
		repl := item.with
		if repl == "" {
			repl = redactedMarker
		}
		s = strings.ReplaceAll(s, item.form, repl)
	}
	return ingest.RedactCmdline(s)
}

func redactText(s *string, forms []secretForm) *string {
	if s == nil {
		return nil
	}
	out := redactExact(*s, forms)
	return &out
}

// redactStatus runs after statusTextOK. Preview markers are not ASCII, so
// they must not be applied before that check.
func redactStatus(st imageStatusBody, forms []secretForm) imageStatusBody {
	st.Container = redactText(st.Container, forms)
	st.State = redactExact(st.State, forms)
	st.StartedAt = redactText(st.StartedAt, forms)
	st.FinishedAt = redactText(st.FinishedAt, forms)
	st.Digest = redactText(st.Digest, forms)
	return st
}

// redactImageDeploy redacts an HTTP copy. FinishImageDeploy stores the digest
// unchanged. History redacts another copy on read.
func redactImageDeploy(d store.ImageDeploy, forms []secretForm) store.ImageDeploy {
	d.Digest = redactText(d.Digest, forms)
	d.SpecHash = redactExact(d.SpecHash, forms)
	d.Outcome = redactExact(d.Outcome, forms)
	d.Via = redactExact(d.Via, forms)
	return d
}

// redactBody applies exact and keyword redaction to each JSON string of an
// error envelope. Success bodies are redacted before they are stored: lines,
// and every string field of ImageStatus and ImageDeploy, after the agent
// text has passed statusTextOK. The keyword rule treats every following
// non-space byte as the value, so running it on the raw document would
// swallow the rest of the object.
func redactBody(body []byte, forms []secretForm) []byte {
	if len(body) == 0 {
		return body
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return []byte(redactExact(string(body), forms))
	}
	out, err := json.Marshal(redactJSON(v, forms))
	if err != nil {
		return []byte(redactExact(string(body), forms))
	}
	return out
}

func redactJSON(v any, forms []secretForm) any {
	switch t := v.(type) {
	case string:
		return redactExact(t, forms)
	case []any:
		for i := range t {
			t[i] = redactJSON(t[i], forms)
		}
		return t
	case map[string]any:
		for k, val := range t {
			t[k] = redactJSON(val, forms)
		}
		return t
	default:
		return v
	}
}

func redactDetail(detail map[string]any, forms []secretForm) map[string]any {
	if len(forms) == 0 || detail == nil {
		return detail
	}
	out := make(map[string]any, len(detail))
	for k, v := range detail {
		s, ok := v.(string)
		if !ok {
			out[k] = v
			continue
		}
		out[k] = redactExact(s, forms)
	}
	return out
}

func (s *Server) settle(fn func(context.Context) error) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
		err = fn(ctx)
		cancel()
		if err == nil {
			return nil
		}
	}
	return err
}

// releaseKey drops the key's claim or stored answer so the next request
// with it runs as new (api-v1 v1.42 item 2).
func (s *Server) releaseKey(userID uuid.UUID, key string) {
	if key == "" {
		return
	}
	if err := s.settle(func(ctx context.Context) error {
		return s.Store.ReleaseIdempotency(ctx, userID, key)
	}); err != nil {
		s.logErr(err)
	}
}

func (s *Server) finishImageKey(userID uuid.UUID, key string, status int, body []byte) {
	if key == "" {
		return
	}
	_ = s.settle(func(ctx context.Context) error {
		return s.Store.ReplaceIdempotencyResponse(ctx, userID, key, status, body)
	})
}

func (s *Server) recordAudit(ctx context.Context, row store.AuditRow) {
	write := func(c context.Context) error {
		s.Audit.Record(c, row)
		return nil
	}
	if ctx != nil && ctx.Err() == nil {
		_ = write(ctx)
		return
	}
	_ = s.settle(write)
}

func (s *Server) imageDone(w http.ResponseWriter, userID uuid.UUID, key string, payload any) {
	body := envelopeRaw("ok", "ok", payload)
	s.finishImageKey(userID, key, http.StatusOK, body)
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) imageFail(ctx context.Context, w http.ResponseWriter, userID uuid.UUID, key string, err error, forms []secretForm) {
	call := refCall(err)
	switch {
	case call != nil: // a bad ${KEY} reference; refCall built the answer
	case errors.Is(err, errDecrypt):
		s.logger().Error("decrypt failed")
		call = &agentws.CallError{Code: "error", Msg: "internal error"}
	case errors.Is(err, errSecretsKey):
		s.logger().Error("secrets key not configured")
		call = &agentws.CallError{Code: "unavailable", Msg: errSecretsKey.Error()}
	case errors.Is(err, store.ErrConflict):
		call = &agentws.CallError{Code: "conflict", Msg: "conflict"}
	case errors.Is(err, store.ErrNotFound):
		call = &agentws.CallError{Code: "not_found", Msg: "not found"}
	case !errors.As(err, &call):
		s.logErr(err)
		call = &agentws.CallError{Code: "error", Msg: "internal error"}
	}
	if call.Msg == "" {
		call.Msg = phrase(call.Code)
	}
	// A RefError message holds key names and fixed text only, and is sent
	// as is (see recordImageDeny); every other message is redacted.
	ref := refCall(err) != nil
	if !ref {
		call.Msg = redactExact(call.Msg, forms)
	}
	status, body, _ := liveEnvelope(agentws.LiveResult{}, call)
	if !ref {
		body = redactBody(body, forms)
	}
	s.finishImageKey(userID, key, status, body)
	writeJSON(w, status, body)
}

func (s *Server) imageDeny(w http.ResponseWriter, r *http.Request, p principal, projectID, hostID uuid.UUID, hostName, entryName, action, code, msg string) {
	s.auditImageDeny(r.Context(), p, projectID, store.Host{ID: hostID, Name: hostName}, entryName, action, code, msg, nil)
	writeCode(w, code, msg)
}

// refCall is the answer to a bad ${KEY} reference (api-v1 v1.27 item 7):
// bad_request with the RefError message, which names keys only. It is nil
// for any other error.
func refCall(err error) *agentws.CallError {
	var ref *catalog.RefError
	if !errors.As(err, &ref) {
		return nil
	}
	return &agentws.CallError{Code: "bad_request", Msg: ref.Msg}
}

// auditTaskBuildErr audits a deploy or rollback refused while the task was
// built: a bad reference, a decrypt failure, or a missing secrets key.
func (s *Server) auditTaskBuildErr(ctx context.Context, p principal, projectID uuid.UUID, host store.Host, entry, action string, err error) {
	call := refCall(err)
	switch {
	case call != nil: // a bad ${KEY} reference; refCall built the answer
		s.recordImageDeny(ctx, p, projectID, host, entry, action, call.Code, call.Msg, nil, false)
	case errors.Is(err, errDecrypt):
		s.auditImageDeny(ctx, p, projectID, host, entry, action, "error", "decrypt failed", nil)
	case errors.Is(err, errSecretsKey):
		s.auditImageDeny(ctx, p, projectID, host, entry, action, "unavailable", errSecretsKey.Error(), nil)
	}
}

func (s *Server) auditImageDeny(ctx context.Context, p principal, projectID uuid.UUID, host store.Host, entry, action, code, msg string, forms []secretForm) {
	s.recordImageDeny(ctx, p, projectID, host, entry, action, code, msg, forms, true)
}

// recordImageDeny writes one deny row. With redact false the reason is
// stored as given: only for a RefError message, which is built from
// validated key names and fixed text. RedactCmdline would otherwise read a
// key such as SECRET_URL followed by a word as a secret pair.
func (s *Server) recordImageDeny(ctx context.Context, p principal, projectID uuid.UUID, host store.Host, entry, action, code, msg string, forms []secretForm, redact bool) {
	name := catalogActionName(action)
	if name == "" {
		return
	}
	result := code
	reason := msg
	if redact {
		reason = redactExact(msg, forms)
	}
	detail := map[string]any{"reason": reason, "entry": entry}
	if host.Name != "" {
		detail["host"] = host.Name
	}
	if redact {
		detail = redactDetail(detail, forms)
	}
	var hostID *uuid.UUID
	if host.ID != uuid.Nil {
		hostID = idPtr(host.ID)
	}
	s.recordAudit(ctx, store.AuditRow{
		ActorUserID: idPtr(p.User.ID),
		Via:         viaKind(p.Session.Kind),
		ProjectID:   idPtr(projectID),
		HostID:      hostID,
		Action:      name,
		Decision:    audit.DecisionDenied,
		Result:      &result,
		ErrorCode:   &result,
		Detail:      detail,
	})
}

func (s *Server) auditImageOK(ctx context.Context, p principal, projectID uuid.UUID, host store.Host, entry, action, outcome string, digest *string, forms []secretForm) {
	name := catalogActionName(action)
	if name == "" {
		return
	}
	result := outcome
	detail := map[string]any{
		"entry":   entry,
		"host":    host.Name,
		"outcome": outcome,
		"via":     viaKind(p.Session.Kind),
	}
	if digest != nil {
		detail["digest"] = *digest
	}
	row := store.AuditRow{
		ActorUserID: idPtr(p.User.ID),
		Via:         viaKind(p.Session.Kind),
		ProjectID:   idPtr(projectID),
		HostID:      idPtr(host.ID),
		Action:      name,
		Decision:    audit.DecisionAllowed,
		Result:      &result,
		Detail:      redactDetail(detail, forms),
	}
	if outcome != "succeeded" && outcome != "unchanged" {
		row.ErrorCode = &result
	}
	s.recordAudit(ctx, row)
}

func catalogActionName(action string) string {
	switch action {
	case "deploy":
		return audit.ActionCatalogDeploy
	case "rollback":
		return audit.ActionCatalogRollback
	case "remove":
		return audit.ActionCatalogRemove
	default:
		return ""
	}
}

func (s *Server) sealValue(projectID uuid.UUID, set, key, plain string) (store.EnvSecret, error) {
	if s.Secrets == nil {
		return store.EnvSecret{}, errSecretsKey
	}
	nonce, ciphertext, err := s.Secrets.Seal(seal.EnvAAD(projectID.String(), set, key), []byte(plain))
	if err != nil {
		return store.EnvSecret{}, err
	}
	return store.EnvSecret{Key: key, Nonce: nonce, Ciphertext: ciphertext, KeyVersion: seal.KeyVersion}, nil
}
