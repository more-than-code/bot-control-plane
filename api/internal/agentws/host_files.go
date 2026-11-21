package agentws

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Host config files (agent-protocol v1.18, api-v1 v1.44).

// HostFileTimeoutSec is the host_file_apply task budget (agent-protocol
// v1.18 item 4, spec amendment A4): check 20 + idle 15 + restart 60 +
// settle 180 + rollback restart 60 + settle 180 = 515 s, and slack.
const HostFileTimeoutSec = 600

// HostFileApply asks a live host to run host_file_apply. service is null.
func (h *Hub) HostFileApply(ctx context.Context, host store.Host, spec map[string]any) (LiveResult, error) {
	return h.live(ctx, host, "host_file_apply", nil, spec, HostFileTimeoutSec)
}

var (
	hostFileUnitRe = regexp.MustCompile(`^[a-zA-Z0-9@._-]{1,128}\.service$`)
	hostFileSHARe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// validHostFiles checks hello.programs.host_files (agent-protocol v1.18
// item 3): at most 8, unique valid names, a /etc/<dir>/<file> path and a
// .service unit. nil (key absent) is valid.
func validHostFiles(files []store.HostFile) bool {
	if len(files) > store.MaxHostFiles {
		return false
	}
	names := map[string]bool{}
	for _, f := range files {
		if !store.ValidHostFileName(f.Name) || names[f.Name] || !validHostFilePath(f.Path) || !hostFileUnitRe.MatchString(f.Unit) {
			return false
		}
		names[f.Name] = true
	}
	return true
}

// validHostFilePath is exactly /etc/<dir>/<file>: two components after
// /etc, each 1 to 128 bytes, not "." or "..", without NUL.
func validHostFilePath(p string) bool {
	parts := strings.Split(p, "/")
	if len(parts) != 4 || parts[0] != "" || parts[1] != "etc" {
		return false
	}
	for _, c := range parts[2:] {
		if len(c) < 1 || len(c) > 128 || c == "." || c == ".." || strings.ContainsRune(c, 0) {
			return false
		}
	}
	return true
}

// Status frames per connection: 1 per second, burst 5 (agent-protocol
// v1.18 item 5 sends one at hello, after each apply and every 10 minutes).
const (
	statusPerSec = 1
	statusBurst  = 5
)

// allowStatus is a token bucket like allowMetrics.
func (ac *agentConn) allowStatus(now time.Time) bool {
	if ac.statusAt.IsZero() {
		ac.statusTokens = statusBurst
	} else if elapsed := now.Sub(ac.statusAt); elapsed > 0 {
		ac.statusTokens = min(statusBurst, ac.statusTokens+elapsed.Seconds()*statusPerSec)
	}
	ac.statusAt = now
	if ac.statusTokens < 1 {
		return false
	}
	ac.statusTokens--
	return true
}

// onHostFilesStatus stores a host_files_status frame (agent-protocol v1.18
// item 5): only files the host's hello declared, never a size or hash for
// a secret one, with bad_since carried from the stored status while the
// file stays different, missing or wrong_owner. A malformed frame is
// dropped with one log line; the socket stays up.
func (h *Hub) onHostFilesStatus(ac *agentConn, f frame) {
	if !ac.allowStatus(h.now()) {
		return
	}
	var wire []struct {
		Name           string `json:"name"`
		Exists         bool   `json:"exists"`
		OwnerOK        bool   `json:"owner_ok"`
		Reason         string `json:"reason"`
		MatchesApplied *bool  `json:"matches_applied"`
		Size           *int   `json:"size"`
		SHA256         string `json:"sha256"`
	}
	if err := json.Unmarshal(f.Files, &wire); err != nil || len(wire) > store.MaxHostFiles {
		h.log.Warn("bad host_files_status", "host_id", ac.hostID.String())
		return
	}
	host, err := h.st.GetHost(ac.parent, ac.hostID)
	if err != nil {
		h.log.Error("host files status failed", "host_id", ac.hostID.String())
		return
	}
	declared := map[string]store.HostFile{}
	if host.Programs != nil {
		for _, hf := range host.Programs.HostFiles {
			declared[hf.Name] = hf
		}
	}
	prev, err := h.st.GetHostFilesStatus(ac.parent, ac.hostID)
	if err != nil {
		h.log.Error("host files status failed", "host_id", ac.hostID.String())
		return
	}
	before := map[string]store.HostFileStatus{}
	if prev != nil {
		for _, p := range prev.Files {
			before[p.Name] = p
		}
	}
	now := h.now()
	out := store.HostFilesStatus{At: now, Files: []store.HostFileStatus{}}
	seen := map[string]bool{}
	for _, w := range wire {
		hf, ok := declared[w.Name]
		if !ok || seen[w.Name] {
			continue
		}
		seen[w.Name] = true
		st := store.HostFileStatus{Name: w.Name, Exists: w.Exists, OwnerOK: w.OwnerOK, MatchesApplied: w.MatchesApplied}
		if store.ValidHostFileStatusReason(w.Reason) {
			st.Reason = w.Reason
		}
		if !hf.Secret {
			if w.Size != nil && *w.Size >= 0 {
				n := *w.Size
				st.Size = &n
			}
			if hostFileSHARe.MatchString(w.SHA256) {
				st.SHA256 = w.SHA256
			}
		}
		if store.Drifted(st.Word()) {
			since := now
			if p, ok := before[w.Name]; ok && p.BadSince != nil && store.Drifted(p.Word()) {
				since = *p.BadSince
			}
			st.BadSince = &since
		}
		out.Files = append(out.Files, st)
	}
	if err := h.st.SetHostFilesStatus(ac.parent, ac.hostID, out); err != nil {
		h.log.Error("host files status failed", "host_id", ac.hostID.String())
	}
}
