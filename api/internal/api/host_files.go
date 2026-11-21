package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/agentws"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/catalog"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Host config files (api-v1 v1.44, spec docs/spec-host-config-files.md
// §2.7). Every route is console only. The list is for anyone who sees the
// host; everything else is for org owners and admins. No answer, audit row
// or log line ever carries a secret file's value, preview or hash.

const (
	// hostFileWaitMargin is how long past the agent's task budget the apply
	// route waits for its result.
	hostFileWaitMargin = 10 * time.Second
	// hostFileWait is the apply route's wait (610 s, spec amendment A4).
	hostFileWait = time.Duration(agentws.HostFileTimeoutSec)*time.Second + hostFileWaitMargin
	// hostFileRecordTimeout bounds the writes after an apply returns.
	hostFileRecordTimeout = 10 * time.Second
	// hostFileSettleMin and Max bound a settle_sec the caller sends.
	hostFileSettleMin = 30
	hostFileSettleMax = 180
	// maxHostFileKeys is the most keys a secret file holds (api-v1 v1.44 item 5).
	maxHostFileKeys = 128
)

var (
	applyReasonRe = regexp.MustCompile(`^[a-z0-9_]{1,70}$`)
)

// applyStages are the stage tokens of an apply result (spec amendment A5).
var applyStages = map[string]bool{
	"lookup": true, "settle": true, "exec": true, "target": true, "check": true,
	"idle": true, "restart": true, "health": true, "rollback": true,
}

// msgFileChanged answers a stale base_version, or a version whose secret
// flag no longer matches the host's runner.toml.
const (
	msgFileChanged    = "the file changed; reload and try again"
	msgSecretFlag     = "this version was saved when the host declared the file with a different secret setting; save a new version"
	msgApplyNoResult  = "the host did not return an apply result"
	msgApplyTimeout   = "the apply did not finish in time; check the file's status on the host"
	msgApplyForbidden = "the host refused this apply"
)

// hostFileRun is one apply in flight: the file and the version it applies.
type hostFileRun struct {
	started time.Time
	name    string
	version int
}

// hostFileRuns is the in-process set of applies in flight, per host and
// unit (spec amendment A1: two files that restart one unit never
// interleave). One API process serves production; a restart drops the lock and
// the result (the host still finishes, and its next status shows it).
type hostFileRuns struct {
	mu   sync.Mutex
	runs map[string]hostFileRun
}

func (h *hostFileRuns) begin(key string, run hostFileRun) (hostFileRun, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.runs == nil {
		h.runs = map[string]hostFileRun{}
	}
	if cur, busy := h.runs[key]; busy {
		return cur, false
	}
	h.runs[key] = run
	return run, true
}

func (h *hostFileRuns) end(key string) {
	h.mu.Lock()
	delete(h.runs, key)
	h.mu.Unlock()
}

// version is the version an apply in flight on the host targets for the
// named file, 0 for none.
func (h *hostFileRuns) version(hostID uuid.UUID, name string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	prefix := hostID.String() + "|"
	for key, run := range h.runs {
		if strings.HasPrefix(key, prefix) && run.name == name {
			return run.version
		}
	}
	return 0
}

// hostUnitKey keys the apply lock: one apply per host and unit.
func hostUnitKey(hostID uuid.UUID, unit string) string { return hostID.String() + "|" + unit }

// hostFileItem is one item of the list (api-v1 v1.44 item 2).
type hostFileItem struct {
	Name           string             `json:"name"`
	Path           string             `json:"path"`
	Unit           string             `json:"unit"`
	Secret         bool               `json:"secret"`
	AppliedVersion *int               `json:"applied_version"`
	LatestVersion  *int               `json:"latest_version"`
	Status         string             `json:"status"`
	LastApply      *hostFileApplyView `json:"last_apply"`
}

type hostFileApplyView struct {
	Version int       `json:"version"`
	Outcome string    `json:"outcome"`
	Stage   *string   `json:"stage"`
	Reason  *string   `json:"reason"`
	At      time.Time `json:"at"`
}

type hostFileVersionView struct {
	Version int       `json:"version"`
	Size    int       `json:"size"`
	SHA256  *string   `json:"sha256,omitempty"`
	By      uuid.UUID `json:"by"`
	At      time.Time `json:"at"`
	Applied bool      `json:"applied"`
}

func optText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// hostFileGate checks a console session, the org and host in the path (the
// host must belong to that org) and the capability. action names the audit
// row of a refused write.
func (s *Server) hostFileGate(w http.ResponseWriter, r *http.Request, capability, action string) (principal, store.Host, bool) {
	p, ok := s.require(w, r)
	if !ok || !requireConsole(w, p) {
		return principal{}, store.Host{}, false
	}
	orgID, ok := s.pathID(w, r, "id")
	if !ok {
		return principal{}, store.Host{}, false
	}
	hostID, ok := s.pathID(w, r, "host_id")
	if !ok {
		return principal{}, store.Host{}, false
	}
	host, err := s.Store.GetHost(r.Context(), hostID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && host.OrgID != orgID) {
		writeCode(w, "not_found", "not found")
		return principal{}, store.Host{}, false
	}
	if err != nil {
		s.fail(w, err)
		return principal{}, store.Host{}, false
	}
	if _, ok := s.gateHost(w, r, p, host, capability, nil, nil, action); !ok {
		return principal{}, store.Host{}, false
	}
	return p, host, true
}

// declaredFile is the hello entry for the path's file name, or a written
// answer: an agent without host_files is unsupported_agent, an unknown
// name not_found.
func declaredFile(w http.ResponseWriter, r *http.Request, host store.Host) (store.HostFile, bool) {
	name := r.PathValue("name")
	if !store.ValidHostFileName(name) {
		writeCode(w, "bad_request", "invalid name")
		return store.HostFile{}, false
	}
	if host.Programs == nil || host.Programs.HostFiles == nil {
		writeCode(w, "unsupported_agent", "this host's agent does not support config files; upgrade bot-agent")
		return store.HostFile{}, false
	}
	for _, f := range host.Programs.HostFiles {
		if f.Name == name {
			return f, true
		}
	}
	writeCode(w, "not_found", "not found")
	return store.HostFile{}, false
}

func stateByName(states []store.HostFileState) map[string]store.HostFileState {
	out := make(map[string]store.HostFileState, len(states))
	for _, st := range states {
		out[st.Name] = st
	}
	return out
}

// handleHostFileList is GET /v1/orgs/{id}/hosts/{host_id}/files.
func (s *Server) handleHostFileList(w http.ResponseWriter, r *http.Request) {
	_, host, ok := s.hostFileGate(w, r, authz.CapHostView, "")
	if !ok {
		return
	}
	items := []hostFileItem{}
	if host.Programs != nil && len(host.Programs.HostFiles) > 0 {
		states, err := s.Store.HostFileStates(r.Context(), host.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		status, err := s.Store.GetHostFilesStatus(r.Context(), host.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		byName := stateByName(states)
		now := s.now()
		for _, f := range host.Programs.HostFiles {
			st := byName[f.Name]
			item := hostFileItem{
				Name: f.Name, Path: f.Path, Unit: f.Unit, Secret: f.Secret,
				AppliedVersion: st.AppliedVersion, LatestVersion: st.LatestVersion,
				Status: store.FileWord(status, f.Name, now),
			}
			if a := st.LastApply; a != nil {
				item.LastApply = &hostFileApplyView{Version: a.Version, Outcome: a.Outcome, Stage: optText(a.Stage), Reason: optText(a.Reason), At: a.At.UTC()}
			}
			items = append(items, item)
		}
	}
	writeOK(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) appliedVersion(ctx context.Context, hostID uuid.UUID, name string) (*int, error) {
	states, err := s.Store.HostFileStates(ctx, hostID)
	if err != nil {
		return nil, err
	}
	return stateByName(states)[name].AppliedVersion, nil
}

// handleHostFileGet is GET …/files/{name} (admin).
func (s *Server) handleHostFileGet(w http.ResponseWriter, r *http.Request) {
	_, host, ok := s.hostFileGate(w, r, authz.CapHostAdmin, "")
	if !ok {
		return
	}
	f, ok := declaredFile(w, r, host)
	if !ok {
		return
	}
	rows, err := s.Store.ListHostFileVersions(r.Context(), host.ID, f.Name)
	if err != nil {
		s.fail(w, err)
		return
	}
	applied, err := s.appliedVersion(r.Context(), host.ID, f.Name)
	if err != nil {
		s.fail(w, err)
		return
	}
	versions := make([]hostFileVersionView, 0, len(rows))
	for _, v := range rows {
		view := hostFileVersionView{Version: v.Version, Size: v.Size, By: v.CreatedBy, At: v.CreatedAt.UTC(), Applied: applied != nil && *applied == v.Version}
		if !f.Secret && !v.Secret {
			sum := v.SHA256
			view.SHA256 = &sum
		}
		versions = append(versions, view)
	}
	latest, err := s.Store.GetHostFileVersion(r.Context(), host.ID, f.Name, 0)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}
	found := err == nil && latest.Secret == f.Secret
	if !f.Secret {
		content := ""
		if found {
			content = latest.Plain
		}
		writeOK(w, http.StatusOK, map[string]any{"content": content, "versions": versions})
		return
	}
	type keyView struct {
		Name string `json:"name"`
		Set  bool   `json:"set"`
	}
	keys := []keyView{}
	if found {
		env, err := s.openSecretFile(latest)
		if err != nil {
			s.failSecret(w, err)
			return
		}
		for _, k := range sortedKeys(env) {
			keys = append(keys, keyView{Name: k, Set: env[k] != ""})
		}
	}
	writeOK(w, http.StatusOK, map[string]any{"keys": keys, "versions": versions})
}

// parseVersion is a version number from 1 to store.MaxHostFileVersion.
func parseVersion(raw string) (int, bool) {
	v, err := strconv.Atoi(raw)
	return v, err == nil && v >= 1 && v <= store.MaxHostFileVersion
}

// handleHostFileVersion is GET …/files/{name}/versions/{v} (admin, plain only).
func (s *Server) handleHostFileVersion(w http.ResponseWriter, r *http.Request) {
	_, host, ok := s.hostFileGate(w, r, authz.CapHostAdmin, "")
	if !ok {
		return
	}
	f, ok := declaredFile(w, r, host)
	if !ok {
		return
	}
	version, ok := parseVersion(r.PathValue("v"))
	if !ok {
		writeCode(w, "bad_request", "invalid version")
		return
	}
	if f.Secret {
		writeCode(w, "forbidden", "a secret file's content is never shown")
		return
	}
	v, err := s.Store.GetHostFileVersion(r.Context(), host.ID, f.Name, version)
	if err != nil {
		s.fail(w, err)
		return
	}
	if v.Secret {
		writeCode(w, "forbidden", "a secret file's content is never shown")
		return
	}
	writeOK(w, http.StatusOK, map[string]any{"content": v.Plain})
}

// handleHostFilePut is PUT …/files/{name} (admin): a new version.
func (s *Server) handleHostFilePut(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.hostFileGate(w, r, authz.CapHostAdmin, audit.ActionHostFileSave)
	if !ok {
		return
	}
	f, ok := declaredFile(w, r, host)
	if !ok {
		return
	}
	save := store.HostFileSave{
		HostID: host.ID, Name: f.Name, Secret: f.Secret, CreatedBy: p.User.ID, At: s.now(),
		Protect: func() int { return s.hostFiles.version(host.ID, f.Name) },
	}
	if f.Secret {
		ok = s.secretSave(w, r, host, f, &save)
	} else {
		ok = plainSave(w, r, &save)
	}
	if !ok {
		return
	}
	version, err := s.Store.SaveHostFileVersion(r.Context(), save)
	switch {
	case errors.Is(err, store.ErrConflict):
		writeCode(w, "conflict", msgFileChanged)
		return
	case err != nil:
		s.writeErr(w, err, false)
		return
	}
	row := hostAuditRow(host, nil, audit.ActionHostFileSave)
	row.Detail = map[string]any{"host": host.Name, "name": f.Name, "version": version, "secret": f.Secret, "size": save.Size}
	s.auditOK(context.WithoutCancel(r.Context()), p, row)
	writeOK(w, http.StatusOK, map[string]any{"version": version})
}

// plainSave reads {"content"}: 1 to 65536 bytes of UTF-8 without NUL,
// saved exactly.
func plainSave(w http.ResponseWriter, r *http.Request, save *store.HostFileSave) bool {
	var body struct {
		Content *string `json:"content"`
	}
	if !decodeFileJSON(w, r, &body) {
		return false
	}
	if body.Content == nil || !catalog.ValidFileContent(*body.Content) {
		writeCode(w, "bad_request", "content must be 1 to 65536 bytes of text without NUL")
		return false
	}
	sum := sha256.Sum256([]byte(*body.Content))
	save.Plain, save.Size, save.SHA256 = *body.Content, len(*body.Content), hex.EncodeToString(sum[:])
	return true
}

// secretSave reads {"base_version","set","remove"}, applies it to the base
// version's keys and sets save's size and seal. Messages name keys, never
// values.
func (s *Server) secretSave(w http.ResponseWriter, r *http.Request, host store.Host, f store.HostFile, save *store.HostFileSave) bool {
	var body struct {
		BaseVersion *int              `json:"base_version"`
		Set         map[string]string `json:"set"`
		Remove      []string          `json:"remove"`
	}
	if !decodeFileJSON(w, r, &body) {
		return false
	}
	if body.BaseVersion == nil || *body.BaseVersion < 0 || *body.BaseVersion > store.MaxHostFileVersion {
		writeCode(w, "bad_request", "base_version is required")
		return false
	}
	if msg := secretEditProblem(body.Set, body.Remove); msg != "" {
		writeCode(w, "bad_request", msg)
		return false
	}
	if s.Secrets == nil {
		writeCode(w, "unavailable", errSecretsKey.Error())
		return false
	}
	env := map[string]string{}
	if *body.BaseVersion > 0 {
		base, err := s.Store.GetHostFileVersion(r.Context(), host.ID, f.Name, *body.BaseVersion)
		if errors.Is(err, store.ErrNotFound) {
			writeCode(w, "conflict", msgFileChanged)
			return false
		}
		if err != nil {
			s.fail(w, err)
			return false
		}
		if !base.Secret {
			writeCode(w, "conflict", msgSecretFlag)
			return false
		}
		if env, err = s.openSecretFile(base); err != nil {
			s.failSecret(w, err)
			return false
		}
	}
	for _, k := range body.Remove {
		if _, ok := env[k]; !ok {
			writeCode(w, "bad_request", "unknown key "+k)
			return false
		}
		delete(env, k)
	}
	for k, v := range body.Set {
		env[k] = v
	}
	if len(env) == 0 {
		writeCode(w, "bad_request", "a secret file needs at least one key")
		return false
	}
	if len(env) > maxHostFileKeys {
		writeCode(w, "bad_request", "at most 128 keys")
		return false
	}
	content := renderSecretFile(env)
	if len(content) > store.MaxHostFileSize {
		writeCode(w, "bad_request", "the file would be larger than 65536 bytes")
		return false
	}
	hostID := host.ID.String()
	save.Size, save.BaseVersion = len(content), body.BaseVersion
	save.Seal = func(version int) ([]byte, []byte, int, error) {
		nonce, ct, err := s.Secrets.Seal(seal.HostFileAAD(hostID, f.Name, version), content)
		return nonce, ct, seal.KeyVersion, err
	}
	return true
}

// secretEditProblem checks set and remove (api-v1 v1.44 item 5) with the
// env key and value rules. The message names a key, never a value.
func secretEditProblem(set map[string]string, remove []string) string {
	if len(set)+len(remove) == 0 {
		return "nothing to change"
	}
	if len(set) > maxHostFileKeys || len(remove) > maxHostFileKeys {
		return "at most 128 keys"
	}
	for k, v := range set {
		if !catalog.ValidEnvKey(k) {
			return "invalid key name"
		}
		if !catalog.ValidLineValue(v) {
			return "the value of " + k + " must be one line, without NUL, at most 4096 bytes"
		}
	}
	seen := map[string]bool{}
	for _, k := range remove {
		if !catalog.ValidEnvKey(k) {
			return "invalid key name"
		}
		if _, both := set[k]; both || seen[k] {
			return "key " + k + " is listed twice"
		}
		seen[k] = true
	}
	return ""
}

func sortedKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// renderSecretFile is KEY=value lines in key order.
func renderSecretFile(env map[string]string) []byte {
	var b bytes.Buffer
	for _, k := range sortedKeys(env) {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(env[k])
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// parseSecretFile reads what renderSecretFile wrote. A value may hold '='.
func parseSecretFile(content []byte) (map[string]string, error) {
	env := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(content), "\n"), "\n") {
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || !catalog.ValidEnvKey(k) {
			return nil, errDecrypt
		}
		env[k] = v
	}
	return env, nil
}

func (s *Server) openSecretBytes(v store.HostFileVersion) ([]byte, error) {
	if s.Secrets == nil {
		return nil, errSecretsKey
	}
	plain, err := s.Secrets.Open(seal.HostFileAAD(v.HostID.String(), v.Name, v.Version), v.Nonce, v.Ciphertext)
	if err != nil {
		return nil, errDecrypt
	}
	return plain, nil
}

func (s *Server) openSecretFile(v store.HostFileVersion) (map[string]string, error) {
	plain, err := s.openSecretBytes(v)
	if err != nil {
		return nil, err
	}
	return parseSecretFile(plain)
}

func (s *Server) failSecret(w http.ResponseWriter, err error) {
	if errors.Is(err, errSecretsKey) {
		writeCode(w, "unavailable", errSecretsKey.Error())
		return
	}
	s.logger().Error("host file open failed")
	writeCode(w, "error", "internal error")
}

// hostFileResult is the agent's apply result (agent-protocol v1.18 item 4)
// as the route answers it: never the sha256.
type hostFileResult struct {
	Outcome string  `json:"outcome"`
	Stage   *string `json:"stage"`
	Reason  *string `json:"reason"`
	TookMS  int     `json:"took_ms"`
}

// parseApplyResult reads an apply result; false for anything else.
// Unknown stage or reason tokens are dropped, never echoed; took_ms is
// clamped to 0..MaxHostFileVersion.
func parseApplyResult(raw json.RawMessage) (hostFileResult, bool) {
	var wire struct {
		Outcome string `json:"outcome"`
		Stage   string `json:"stage"`
		Reason  string `json:"reason"`
		TookMS  int64  `json:"took_ms"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &wire) != nil || !store.ValidHostFileOutcome(wire.Outcome) {
		return hostFileResult{}, false
	}
	res := hostFileResult{Outcome: wire.Outcome, TookMS: int(min(max(0, wire.TookMS), store.MaxHostFileVersion))}
	if applyStages[wire.Stage] {
		res.Stage = &wire.Stage
	}
	if applyReasonRe.MatchString(wire.Reason) {
		res.Reason = &wire.Reason
	}
	return res, true
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// helperRefusals are the agent's failures without an apply outcome
// (agent-protocol v1.18 item 4): the helper refused or could not run. Each
// answers error with a fixed message; the audit row carries the code.
var helperRefusals = map[string]string{
	"bad_spec":           "The host refused the request.",
	"policy_denied":      "The host's policy does not allow this file.",
	"io_error":           "The host could not write the file.",
	"helper_unavailable": "The host's helper is not running.",
}

// applyFailure maps a dispatch error without a result to the answer code,
// a fixed message and the audit outcome: nothing the agent wrote is echoed.
func applyFailure(err error, host store.Host) (code, msg, outcome string) {
	var ce *agentws.CallError
	if errors.As(err, &ce) {
		if msg, ok := helperRefusals[ce.Agent]; ok {
			return "error", msg, ce.Agent
		}
		switch ce.Code {
		case "unavailable":
			return "host_offline", host.Name + " is offline", "host_offline"
		case "timeout":
			return "timeout", msgApplyTimeout, "timeout"
		case "forbidden":
			return "forbidden", msgApplyForbidden, "forbidden"
		}
	}
	return "error", msgApplyNoResult, "error"
}

// handleHostFileApply is POST …/files/{name}/apply (admin).
func (s *Server) handleHostFileApply(w http.ResponseWriter, r *http.Request) {
	p, host, ok := s.hostFileGate(w, r, authz.CapHostAdmin, audit.ActionHostFileApply)
	if !ok {
		return
	}
	f, ok := declaredFile(w, r, host)
	if !ok {
		return
	}
	var body struct {
		Version   *int  `json:"version"`
		SettleSec *int  `json:"settle_sec"`
		Force     *bool `json:"force"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Version == nil || *body.Version < 1 || *body.Version > store.MaxHostFileVersion {
		writeCode(w, "bad_request", "version is required")
		return
	}
	// Only a settle_sec the caller sent goes to the agent: its default is 60
	// raised to the health command's --wait + 15 (agent-protocol v1.18).
	if body.SettleSec != nil && (*body.SettleSec < hostFileSettleMin || *body.SettleSec > hostFileSettleMax) {
		writeCode(w, "bad_request", "settle_sec must be 30 to 180")
		return
	}
	force := body.Force != nil && *body.Force
	if host.RevokedAt != nil || s.hostStatus(host) != "online" {
		writeCode(w, "host_offline", host.Name+" is offline")
		return
	}
	key := hostUnitKey(host.ID, f.Unit)
	run, ok := s.hostFiles.begin(key, hostFileRun{started: s.now(), name: f.Name, version: *body.Version})
	if !ok {
		until := run.started.Add(s.hostFileWaitFor()).UTC()
		msg := fmt.Sprintf("An apply of %s on %s has been running since %s UTC. Try again after %s UTC.",
			run.name, host.Name, run.started.UTC().Format("15:04"), until.Format("15:04"))
		writeJSON(w, statusFor("conflict"), envelopeRaw("conflict", msg, map[string]string{
			"started_at": run.started.UTC().Format(time.RFC3339),
			"ends_by":    until.Format(time.RFC3339),
		}))
		return
	}
	defer s.hostFiles.end(key)
	// Read the version under the lock, so a save's prune spares it.
	v, err := s.Store.GetHostFileVersion(r.Context(), host.ID, f.Name, *body.Version)
	if err != nil {
		s.fail(w, err)
		return
	}
	if v.Secret != f.Secret {
		writeCode(w, "conflict", msgSecretFlag)
		return
	}
	content := []byte(v.Plain)
	if v.Secret {
		if content, err = s.openSecretBytes(v); err != nil {
			s.failSecret(w, err)
			return
		}
	}
	// The apply goes on when the caller leaves: its result is recorded.
	detached := context.WithoutCancel(r.Context())
	wait, cancel := context.WithTimeout(detached, s.hostFileWaitFor())
	spec := map[string]any{"name": f.Name, "content_b64": base64.StdEncoding.EncodeToString(content), "force": force}
	if body.SettleSec != nil {
		spec["settle_sec"] = *body.SettleSec
	}
	res, err := s.Hub.HostFileApply(wait, host, spec)
	cancel()
	payload := res.Payload
	var ce *agentws.CallError
	if err != nil && errors.As(err, &ce) && len(ce.Payload) > 0 {
		payload = ce.Payload
	}
	result, parsed := parseApplyResult(payload)
	// The writes get their own budget, never the spent wait.
	ctx, done := context.WithTimeout(detached, hostFileRecordTimeout)
	defer done()
	row := hostAuditRow(host, nil, audit.ActionHostFileApply)
	if !parsed {
		code, msg, outcome := applyFailure(err, host)
		row.Detail = map[string]any{"host": host.Name, "name": f.Name, "version": v.Version, "outcome": outcome, "stage": nil, "reason": nil}
		s.auditOK(ctx, p, row)
		writeCode(w, code, msg)
		return
	}
	if err := s.Store.RecordHostFileApply(ctx, store.HostFileApply{
		HostID: host.ID, Name: f.Name, Version: v.Version, Outcome: result.Outcome,
		Stage: deref(result.Stage), Reason: deref(result.Reason), TookMS: result.TookMS, By: p.User.ID, At: s.now(),
	}); err != nil {
		s.logger().Error("host file apply record failed", "host_id", host.ID.String(), "name", f.Name)
	}
	row.Detail = map[string]any{"host": host.Name, "name": f.Name, "version": v.Version, "outcome": result.Outcome, "stage": result.Stage, "reason": result.Reason}
	s.auditOK(ctx, p, row)
	s.hostFileApplied(ctx, host, f.Name, result.Outcome)
	if result.Outcome == store.HostFileWorkerBusy {
		write(w, statusFor("worker_busy"), "worker_busy", "the worker is running a job; apply anyway to interrupt it", result)
		return
	}
	writeOK(w, http.StatusOK, result)
}

func (s *Server) hostFileWaitFor() time.Duration {
	if s.hostFileWaitTest > 0 {
		return s.hostFileWaitTest
	}
	return hostFileWait
}
