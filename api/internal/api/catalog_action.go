package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/agentws"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

const (
	reasonNoPrograms     = "host does not allow programs"
	reasonDisabled       = "programs are disabled on this host"
	reasonRunAs          = "run_as is not allowed on this host"
	reasonShell          = "shell is not allowed on this host"
	reasonNoDeployAccess = "this project has no deploy access to the host"
	reasonNoAllowlist    = "this host's agent does not enforce a projects allowlist; upgrade it to 0.2.8"
	reasonInProgress     = "request in progress"
	reasonNoSecrets      = "this host's agent does not support program secrets"
	// reasonProgramEnvTooLarge is api-v1 v1.22 item 4: the agent caps a
	// program task spec at 64 KiB (agent-protocol v1.3).
	reasonProgramEnvTooLarge = "the resolved environment is too large for one task"
	maxProgramSpecBytes      = 64 * 1024
)

// catalogAct is checks 1–6 for run, stop, restart, logs, and status.
// Membership was already checked. JSON and host_id are rejected before the
// grant. The idempotency key and lines are checked after the grant. The
// idempotency row is claimed after the host is online and before any agent
// call or confirmation.
func (s *Server) catalogAct(w http.ResponseWriter, r *http.Request, p principal, projectID uuid.UUID, action string) {
	hostID, key, lines, ok := s.catalogBody(w, r, action)
	if !ok {
		return
	}
	role, err := s.callerRole(r.Context(), projectID, p.User.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	entry, err := s.catalogByName(r, projectID, r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	grants, err := s.Store.ListCatalogGrants(r.Context(), entry.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	held := heldActions(entry.Kind, role, p.User.ID, grants)
	if !allowsAction(held, action) {
		if entry.Kind == "image" {
			writeCode(w, "forbidden", "forbidden")
			return
		}
		s.programDeny(w, r, p, projectID, hostID, entry.Name, action, "forbidden", "forbidden")
		return
	}
	if !actionInputOK(w, action, key, lines, entry.Kind) {
		return
	}
	host, ok := s.programHost(w, r, p, projectID, hostID, entry, action, entry.Kind == "image")
	if !ok {
		return
	}
	if entry.Kind == "image" {
		s.finishImageShort(w, r, p, projectID, host, entry, action, key, lines)
		return
	}
	code, msg, err := s.programGate(r.Context(), projectID, host, entry, action)
	if err != nil {
		s.fail(w, err)
		return
	}
	if code != "" {
		s.programDeny(w, r, p, projectID, host.ID, entry.Name, action, code, msg)
		return
	}
	if s.hostStatus(host) != "online" {
		s.programUnavailable(w, r, p, projectID, host.ID, entry, action)
		return
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
	s.performProgram(w, r, p, projectID, host, entry, action, lines, key, hash)
}

func (s *Server) handleConfirmation(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, uuid.Nil, authz.CapSession); err != nil {
		s.writeAuth(w, err)
		return
	}
	var body struct {
		Key string `json:"idempotency_key"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !catalog.ValidIdempotencyKey(body.Key) {
		writeCode(w, "bad_request", "invalid idempotency_key")
		return
	}
	token := r.PathValue("confirmation_id")
	hash := secret.Hash(token)
	c, err := s.Store.GetConfirmation(r.Context(), hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// v1.10: catalog plans win when both tables could match. They cannot:
			// each id is a fresh 32-byte token. A miss falls through.
			s.confirmServiceHealth(w, r, p, hash, body.Key)
			return
		}
		s.fail(w, err)
		return
	}
	now := s.now()
	if c.UserID != p.User.ID || c.UsedAt != nil || !c.ExpiresAt.After(now) {
		writeCode(w, "not_found", "not found")
		return
	}
	if body.Key != c.IdempotencyKey {
		// The confirm call reuses the key of the request that returned the 202.
		writeCode(w, "conflict", "idempotency_key does not match the request that issued this confirmation")
		return
	}
	role, err := s.callerRole(r.Context(), c.ProjectID, p.User.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeCode(w, "not_found", "not found")
			return
		}
		s.fail(w, err)
		return
	}
	entry, err := s.Store.GetCatalogEntry(r.Context(), c.ProjectID, c.EntryName)
	if err != nil {
		s.fail(w, err)
		return
	}
	grants, err := s.Store.ListCatalogGrants(r.Context(), entry.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if entry.ID != c.EntryID || !allowsAction(heldActions(entry.Kind, role, p.User.ID, grants), grantForAction(entry.Kind, c.Action)) {
		if entry.Kind == "image" {
			s.imageDeny(w, r, p, c.ProjectID, c.HostID, "", entry.Name, c.Action, "forbidden", "forbidden")
			return
		}
		s.programDeny(w, r, p, c.ProjectID, c.HostID, entry.Name, c.Action, "forbidden", "forbidden")
		return
	}
	if entry.Kind == "image" {
		s.confirmImage(w, r, p, c, entry)
		return
	}
	host, ok := s.programHost(w, r, p, c.ProjectID, c.HostID, entry, c.Action, false)
	if !ok {
		return
	}
	code, msg, err := s.programGate(r.Context(), c.ProjectID, host, entry, c.Action)
	if err != nil {
		s.fail(w, err)
		return
	}
	if code != "" {
		s.programDeny(w, r, p, c.ProjectID, host.ID, entry.Name, c.Action, code, msg)
		return
	}
	if s.hostStatus(host) != "online" {
		s.programUnavailable(w, r, p, c.ProjectID, host.ID, entry, c.Action)
		return
	}
	if entry.SpecHash != c.SpecHash {
		writeCode(w, "conflict", "conflict")
		return
	}
	if err := s.Store.MarkConfirmationUsed(r.Context(), hash, p.User.ID, now); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeCode(w, "not_found", "not found")
			return
		}
		s.fail(w, err)
		return
	}
	s.performProgram(w, r, p, c.ProjectID, host, entry, c.Action, 0, c.IdempotencyKey, c.RequestHash)
}

func (s *Server) catalogBody(w http.ResponseWriter, r *http.Request, action string) (uuid.UUID, string, int, bool) {
	switch action {
	case "logs":
		var body struct {
			HostID string `json:"host_id"`
			Lines  *int   `json:"lines"`
		}
		if !decodeJSON(w, r, &body) {
			return uuid.Nil, "", 0, false
		}
		id, ok := parseHostID(w, body.HostID)
		if !ok {
			return uuid.Nil, "", 0, false
		}
		lines := 200
		if body.Lines != nil {
			lines = *body.Lines
		}
		return id, "", lines, true
	case "status":
		var body struct {
			HostID string `json:"host_id"`
		}
		if !decodeJSON(w, r, &body) {
			return uuid.Nil, "", 0, false
		}
		id, ok := parseHostID(w, body.HostID)
		if !ok {
			return uuid.Nil, "", 0, false
		}
		return id, "", 0, true
	default:
		var body struct {
			HostID string `json:"host_id"`
			Key    string `json:"idempotency_key"`
		}
		if !decodeJSON(w, r, &body) {
			return uuid.Nil, "", 0, false
		}
		id, ok := parseHostID(w, body.HostID)
		if !ok {
			return uuid.Nil, "", 0, false
		}
		return id, body.Key, 0, true
	}
}

func actionInputOK(w http.ResponseWriter, action, key string, lines int, kind string) bool {
	switch action {
	case "logs":
		maxLines := 1000
		if kind == "image" {
			maxLines = 2000
		}
		if lines < 1 || lines > maxLines {
			writeCode(w, "bad_request", "invalid lines")
			return false
		}
	case "status":
	default:
		if !catalog.ValidIdempotencyKey(key) {
			writeCode(w, "bad_request", "invalid idempotency_key")
			return false
		}
	}
	return true
}

func parseHostID(w http.ResponseWriter, raw string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	if err != nil {
		writeCode(w, "bad_request", "invalid id")
		return uuid.Nil, false
	}
	return id, true
}

// programHost is the v1.24 item 4 host check for a catalog action: the
// entry's project needs deploy access to the host. Without it the answer is
// forbidden reasonNoDeployAccess when the caller can see the host, and
// not_found otherwise. The host's allowlist pre-check (v1.23) comes after
// this, in programGate or the image path.
func (s *Server) programHost(w http.ResponseWriter, r *http.Request, p principal, projectID, hostID uuid.UUID, entry store.CatalogEntry, action string, image bool) (store.Host, bool) {
	level, err := s.Store.HostProjectAccess(r.Context(), projectID, hostID)
	if err != nil {
		s.fail(w, err)
		return store.Host{}, false
	}
	if level != authz.LevelDeploy {
		rel, err := authz.HostRelation(r.Context(), p.User.ID, hostID)
		if err != nil {
			s.fail(w, err)
			return store.Host{}, false
		}
		if !rel.Sees() {
			writeCode(w, "not_found", "not found")
			return store.Host{}, false
		}
		if image {
			s.imageDeny(w, r, p, projectID, hostID, "", entry.Name, action, "forbidden", reasonNoDeployAccess)
			return store.Host{}, false
		}
		s.programDeny(w, r, p, projectID, hostID, entry.Name, action, "forbidden", reasonNoDeployAccess)
		return store.Host{}, false
	}
	host, err := s.Store.GetHost(r.Context(), hostID)
	if err != nil {
		s.fail(w, err)
		return store.Host{}, false
	}
	return host, true
}

// programsTrusted is spec-organizations D-10 (api-v1 v1.24 item 4): an agent
// older than 0.2.8 reports no programs.projects and enforces no projects
// allowlist, so only the host's single deploy project may run, stop or
// restart programs there.
func (s *Server) programsTrusted(ctx context.Context, host store.Host, projectID uuid.UUID) (bool, error) {
	if host.Programs != nil && host.Programs.Projects != nil {
		return true, nil
	}
	rows, err := s.Store.ListHostAccess(ctx, host.ID)
	if err != nil {
		return false, err
	}
	deploy := 0
	mine := false
	for _, a := range rows {
		if a.Level == authz.LevelDeploy {
			deploy++
			mine = mine || a.ProjectID == projectID
		}
	}
	return deploy == 1 && mine, nil
}

func policyReason(host store.Host, runAs, exec string, shell bool) string {
	if host.Programs == nil {
		return reasonNoPrograms
	}
	if !host.Programs.Enabled {
		return reasonDisabled
	}
	found := false
	for _, name := range host.Programs.RunAs {
		if name == runAs {
			found = true
			break
		}
	}
	if !found {
		return reasonRunAs
	}
	if !host.Programs.AllowShell && (shell || shellExec(exec)) {
		return reasonShell
	}
	return ""
}

// programGate is every program check against the host before a task is
// built: the v1.6 policy, then for a Run the host's projects allowlist
// (api-v1 v1.23 item 2), the secrets gates (v1.22 item 4) and the value
// origin gates (v1.22 amendments 2 and 3). Logs of an
// entry whose output may hold a secret need the secrets key, as for images.
// code is empty when the action may go on.
func (s *Server) programGate(ctx context.Context, projectID uuid.UUID, host store.Host, entry store.CatalogEntry, action string) (string, string, error) {
	if msg := policyReason(host, entry.Spec.RunAs, entry.Spec.Exec, entry.Spec.Shell); msg != "" {
		return "forbidden", msg, nil
	}
	if action == "run" || action == "stop" || action == "restart" {
		ok, err := s.programsTrusted(ctx, host, projectID)
		if err != nil || !ok {
			return "forbidden", reasonNoAllowlist, err
		}
	}
	refs := entry.Spec.Program().EnvRefs()
	if action == "run" || action == "restart" {
		// api-v1 v1.27 item 6: an unfilled placeholder never reaches a unit.
		msg, err := s.unfilledReason(ctx, projectID, refs)
		if err != nil || msg != "" {
			return "bad_request", msg, err
		}
	}
	switch action {
	case "run":
		msg, err := s.unlistedReason(ctx, host, projectID)
		if err != nil || msg != "" {
			return "forbidden", msg, err
		}
		if !refs.Any() {
			return "", "", nil
		}
		if !host.Programs.Secrets {
			return "bad_request", reasonNoSecrets, nil
		}
		if s.Secrets == nil {
			return "unavailable", errSecretsKey.Error(), nil
		}
		msg, err = s.originReason(ctx, projectID, refs)
		if err != nil || msg != "" {
			return "bad_request", msg, err
		}
	case "logs":
		if !refs.Any() || s.Secrets != nil {
			return "", "", nil
		}
		needs, err := s.envNeedsKey(ctx, projectID, refs)
		if err != nil {
			return "", "", err
		}
		if needs {
			return "unavailable", errSecretsKey.Error(), nil
		}
	}
	return "", "", nil
}

// unlistedReason is the api-v1 v1.23 pre-check: when the host reported its
// projects allowlist and the project's slug is not in it, the refusal
// message. An older agent (list unknown) is never refused here.
func (s *Server) unlistedReason(ctx context.Context, host store.Host, projectID uuid.UUID) (string, error) {
	if host.Programs == nil || host.Programs.Projects == nil {
		return "", nil
	}
	project, err := s.Store.GetProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	if slices.Contains(host.Programs.Projects, project.Slug) {
		return "", nil
	}
	return "this host's allowlist does not include project " + project.Slug, nil
}

func shellExec(exec string) bool {
	base := exec
	if i := strings.LastIndex(exec, "/"); i >= 0 {
		base = exec[i+1:]
	}
	switch base {
	case "sh", "bash", "dash":
		return true
	default:
		return false
	}
}

// claimOrReplay reserves the idempotency key. false means the response was
// already written: a replay, a conflict, or an in-progress claim.
func (s *Server) claimOrReplay(w http.ResponseWriter, r *http.Request, userID uuid.UUID, key string, hash []byte) bool {
	rec, claimed, err := s.Store.ClaimIdempotency(r.Context(), userID, key, hash, s.now().UTC())
	if errors.Is(err, store.ErrPending) {
		writeCode(w, "conflict", reasonInProgress)
		return false
	}
	if errors.Is(err, store.ErrConflict) {
		writeCode(w, "conflict", "conflict")
		return false
	}
	if err != nil {
		s.fail(w, err)
		return false
	}
	if !claimed {
		writeJSON(w, rec.Status, rec.Body)
		return false
	}
	return true
}

func (s *Server) issueConfirmation(w http.ResponseWriter, r *http.Request, p principal, projectID uuid.UUID, host store.Host, entry store.CatalogEntry, action, key string, hash []byte, imageDigest string, imageForce *bool, imageNetworks []string) {
	plain, tokenHash, err := secret.NewToken()
	if err != nil {
		s.failClaimed(w, r, p.User.ID, key, err)
		return
	}
	now := s.now().UTC()
	expires := now.Add(store.ConfirmationTTL)
	if err := s.Store.PutConfirmation(r.Context(), tokenHash, store.CatalogConfirmation{
		UserID:         p.User.ID,
		ProjectID:      projectID,
		EntryID:        entry.ID,
		EntryName:      entry.Name,
		HostID:         host.ID,
		Action:         action,
		SpecHash:       entry.SpecHash,
		IdempotencyKey: key,
		RequestHash:    hash,
		CreatedAt:      now,
		ExpiresAt:      expires,
		ImageDigest:    confirmationDigest(imageDigest),
		ImageForce:     imageForce,
	}); err != nil {
		s.failClaimed(w, r, p.User.ID, key, err)
		return
	}
	plan := programPlan(action, entry.Name, entry.CommandDisplay, entry.Spec.RunAs, host.Name)
	if entry.Kind == "image" {
		shown := entry.CommandDisplay
		if action == "rollback" && imageDigest != "" {
			shown = imageDigest
		}
		plan = imagePlan(action, entry.Name, shown, host.Name, imageNetworks)
	}
	payload := map[string]any{
		"confirmation_id": plain,
		"plan":            plan,
		"expires_at":      expires.Format(time.RFC3339),
	}
	body := envelopeRaw("ok", "ok", payload)
	s.finishIdempotency(r.Context(), p.User.ID, key, http.StatusAccepted, body)
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(projectID),
		HostID:    idPtr(host.ID),
		Action:    audit.ActionProgramConfirmation,
		Detail:    map[string]any{"entry": entry.Name, "action": action, "spec_hash": entry.SpecHash},
	})
	writeJSON(w, http.StatusAccepted, body)
}

func (s *Server) performProgram(w http.ResponseWriter, r *http.Request, p principal, projectID uuid.UUID, host store.Host, entry store.CatalogEntry, action string, lines int, key string, _ []byte) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	res, forms, callErr := s.dispatchProgram(ctx, projectID, host, entry, action, lines)
	var taskID *uuid.UUID
	if callErr == nil && res.TaskID != uuid.Nil {
		taskID = idPtr(res.TaskID)
	}
	status, body, _ := liveEnvelopeForms(res, callErr, forms)
	if key != "" {
		s.finishIdempotency(r.Context(), p.User.ID, key, status, body)
	}
	s.programOutcome(w, r, p, projectID, host.ID, entry, action, status, body, taskID, callErr)
}

func (s *Server) finishIdempotency(ctx context.Context, userID uuid.UUID, key string, status int, body []byte) {
	if err := s.Store.ReplaceIdempotencyResponse(ctx, userID, key, status, body); err != nil {
		s.logErr(err)
	}
}

func (s *Server) failClaimed(w http.ResponseWriter, r *http.Request, userID uuid.UUID, key string, err error) {
	status, body, _ := liveEnvelope(agentws.LiveResult{}, err)
	s.finishIdempotency(r.Context(), userID, key, status, body)
	writeJSON(w, status, body)
}

// dispatchProgram builds and sends the program task. It also returns the
// secret value forms used to redact the result lines (api-v1 v1.22 item 6).
func (s *Server) dispatchProgram(ctx context.Context, projectID uuid.UUID, host store.Host, entry store.CatalogEntry, action string, lines int) (agentws.LiveResult, []secretForm, error) {
	if s.Hub == nil {
		return agentws.LiveResult{}, nil, &agentws.CallError{Code: "error", Msg: "internal error"}
	}
	project, err := s.Store.GetProject(ctx, projectID)
	if err != nil {
		return agentws.LiveResult{}, nil, err
	}
	kind, spec := programTask(project.Slug, entry, action, lines)
	refs := entry.Spec.Program().EnvRefs()
	var forms []secretForm
	if refs.Any() && action == "run" {
		merged, err := s.mergeEnv(ctx, projectID, refs)
		if err != nil {
			return agentws.LiveResult{}, nil, s.programEnvErr(err)
		}
		env, secretKeys := merged.env, merged.secretKeys
		forms = merged.forms()
		if len(env) > catalog.MaxProgramEnv {
			return agentws.LiveResult{}, forms, &agentws.CallError{Code: "bad_request", Msg: "too many env keys"}
		}
		spec["env"] = env
		spec["secret_keys"] = secretKeys
		raw, err := json.Marshal(spec)
		if err != nil {
			return agentws.LiveResult{}, forms, err
		}
		if len(raw) > maxProgramSpecBytes {
			return agentws.LiveResult{}, forms, &agentws.CallError{Code: "bad_request", Msg: reasonProgramEnvTooLarge}
		}
	} else if refs.Any() && s.Secrets != nil {
		typed, generated, failed := s.redactEnvValues(ctx, projectID, refs)
		if failed {
			s.logger().Error("decrypt failed")
		}
		forms = envSecretForms(typed, generated)
	}
	res, err := s.Hub.Program(ctx, host, kind, spec)
	return res, forms, err
}

// programEnvErr maps a resolve failure to the envelope with imageFail's
// mapping. Nothing it logs holds a value.
func (s *Server) programEnvErr(err error) error {
	switch {
	case errors.Is(err, errDecrypt):
		s.logger().Error("decrypt failed")
		return &agentws.CallError{Code: "error", Msg: "internal error"}
	case errors.Is(err, errSecretsKey):
		s.logger().Error("secrets key not configured")
		return &agentws.CallError{Code: "unavailable", Msg: errSecretsKey.Error()}
	case errors.Is(err, store.ErrNotFound):
		return &agentws.CallError{Code: "not_found", Msg: "not found"}
	}
	s.logErr(err)
	return &agentws.CallError{Code: "error", Msg: "internal error"}
}

func programTask(slug string, entry store.CatalogEntry, action string, lines int) (string, map[string]any) {
	base := map[string]any{"project": slug, "entry": entry.Name}
	switch action {
	case "run":
		env := entry.Spec.Env
		if env == nil {
			env = map[string]string{}
		}
		args := entry.Spec.Args
		if args == nil {
			args = []string{}
		}
		return "program_start", map[string]any{
			"project": slug,
			"entry":   entry.Name,
			"dir":     entry.Spec.Dir,
			"exec":    entry.Spec.Exec,
			"args":    args,
			"shell":   entry.Spec.Shell,
			"run_as":  entry.Spec.RunAs,
			"env":     env,
			"restart": entry.Spec.Restart,
		}
	case "stop":
		return "program_stop", base
	case "restart":
		return "program_restart", base
	case "logs":
		out := map[string]any{"project": slug, "entry": entry.Name, "lines": lines}
		return "program_logs", out
	default:
		return "program_status", base
	}
}

// programUnavailable is the offline result. It is checked after policy and
// does not consume a confirmation.
func (s *Server) programUnavailable(w http.ResponseWriter, r *http.Request, p principal, projectID, hostID uuid.UUID, entry store.CatalogEntry, action string) {
	callErr := &agentws.CallError{Code: "unavailable", Msg: "host unavailable"}
	status, body, _ := liveEnvelope(agentws.LiveResult{}, callErr)
	s.programOutcome(w, r, p, projectID, hostID, entry, action, status, body, nil, callErr)
}

// programOutcome writes the task result that was already stored for
// idempotency. run, stop, and restart are audited. logs and status are not.
func (s *Server) programOutcome(w http.ResponseWriter, r *http.Request, p principal, projectID, hostID uuid.UUID, entry store.CatalogEntry, action string, status int, body []byte, taskID *uuid.UUID, callErr error) {
	if action == "run" || action == "stop" || action == "restart" {
		row := store.AuditRow{
			ProjectID: idPtr(projectID),
			HostID:    idPtr(hostID),
			Action:    programAction(action),
			Decision:  audit.DecisionAllowed,
			Detail:    map[string]any{"entry": entry.Name, "spec_hash": entry.SpecHash},
			TaskID:    taskID,
		}
		code := "ok"
		if callErr != nil {
			var call *agentws.CallError
			if errors.As(callErr, &call) {
				code = call.Code
			} else {
				code = "error"
			}
			row.Result = &code
			row.ErrorCode = &code
		} else {
			ok := "succeeded"
			row.Result = &ok
		}
		s.auditOK(r.Context(), p, row)
	}
	writeJSON(w, status, body)
}

func (s *Server) programDeny(w http.ResponseWriter, r *http.Request, p principal, projectID, hostID uuid.UUID, entryName, action, code, msg string) {
	if action == "run" || action == "stop" || action == "restart" {
		var host *uuid.UUID
		if hostID != uuid.Nil {
			host = idPtr(hostID)
		}
		result := code
		s.Audit.Record(r.Context(), store.AuditRow{
			ActorUserID: idPtr(p.User.ID),
			Via:         viaKind(p.Session.Kind),
			ProjectID:   idPtr(projectID),
			HostID:      host,
			Action:      programAction(action),
			Decision:    audit.DecisionDenied,
			Result:      &result,
			ErrorCode:   &result,
			Detail:      map[string]any{"reason": msg, "entry": entryName},
		})
	}
	writeCode(w, code, msg)
}

// grantForAction is the grant a confirmed action needs. For an image entry the
// deploy grant covers deploy, rollback and remove (api-v1 v1.8), as on the
// direct routes; every other action needs the grant of its own name.
func grantForAction(kind, action string) string {
	if kind == "image" && (action == "deploy" || action == "rollback" || action == "remove") {
		return "deploy"
	}
	return action
}

func programPlan(action, entry, display, runAs, host string) string {
	return fmt.Sprintf("will %s program %s (%s, as %s) on %s", action, entry, display, runAs, host)
}

// imagePlan is the delegated confirm sentence. A deploy or rollback of an
// entry with networks adds the shared networks it joins (api-v1 v1.25 item 5).
func imagePlan(action, entry, shown, host string, networks []string) string {
	switch action {
	case "remove":
		return fmt.Sprintf("will remove %s on %s", entry, host)
	case "deploy", "rollback":
		plan := fmt.Sprintf("will deploy %s (%s) on %s", entry, shown, host)
		if len(networks) > 0 {
			plan += "; also joins shared network(s): " + listPhrase(networks)
		}
		return plan
	default:
		return fmt.Sprintf("will %s %s on %s", action, entry, host)
	}
}

func confirmationDigest(digest string) *string {
	if digest == "" {
		return nil
	}
	return &digest
}

func programRequestHash(action string, projectID, hostID uuid.UUID, entry string) []byte {
	raw := `{"action":` + jsonQuote(action) +
		`,"entry":` + jsonQuote(entry) +
		`,"host_id":` + jsonQuote(hostID.String()) +
		`,"project_id":` + jsonQuote(projectID.String()) + `}`
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}

func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

func envelopeRaw(code, msg string, payload any) []byte {
	if msg == "" {
		msg = phrase(code)
	}
	raw, err := json.Marshal(envelope{Code: code, Msg: msg, Payload: payload})
	if err != nil {
		return []byte(`{"code":"error","msg":"internal error","payload":null}`)
	}
	return raw
}

func liveEnvelope(res agentws.LiveResult, err error) (int, []byte, string) {
	return liveEnvelopeForms(res, err, nil)
}

// liveEnvelopeForms is liveEnvelope with the entry's secret value forms
// redacted from result lines, before the keyword rule.
func liveEnvelopeForms(res agentws.LiveResult, err error, forms []secretForm) (int, []byte, string) {
	if err != nil {
		var call *agentws.CallError
		if errors.As(err, &call) {
			msg := call.Msg
			if msg == "" {
				msg = phrase(call.Code)
			}
			var payload any
			if call.Code == "error" && len(call.Payload) > 0 && json.Valid(call.Payload) && string(call.Payload) != "null" {
				payload = jsonRaw{redactProgramLines(call.Payload, forms)}
			}
			if len(forms) == 0 {
				return statusFor(call.Code), envelopeRaw(call.Code, msg, payload), call.Code
			}
			// An entry with secrets: the message and every string of the
			// envelope get the exact-value redaction, as imageFail does.
			body := redactBody(envelopeRaw(call.Code, redactExact(msg, forms), payload), forms)
			return statusFor(call.Code), body, call.Code
		}
		return http.StatusInternalServerError, envelopeRaw("error", "internal error", nil), "error"
	}
	payload := redactProgramLines(res.Payload, forms)
	if len(payload) == 0 || !json.Valid(payload) {
		payload = []byte("null")
	}
	return http.StatusOK, envelopeRaw("ok", "ok", jsonRaw{payload}), "ok"
}

// redactProgramLines replaces each secret value form (api-v1 v1.22 item 6),
// then applies the v1.0.1 cmdline redactor, on each journal line. Other
// fields are left as the agent sent them.
func redactProgramLines(raw json.RawMessage, forms []secretForm) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	m, ok := v.(map[string]any)
	if !ok {
		return raw
	}
	lines, ok := m["lines"].([]any)
	if !ok {
		return raw
	}
	changed := false
	for i, item := range lines {
		s, ok := item.(string)
		if !ok {
			continue
		}
		next := redactExact(s, forms)
		if next != s {
			lines[i] = next
			changed = true
		}
	}
	if !changed {
		return raw
	}
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}
