package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/agentversion"
)

// Agent release check intervals (api-v1 v1.62, spec
// docs/spec-agent-latest-version.md §4.2).
const (
	agentReleasePoll        = 15 * time.Minute
	agentReleasePollWebhook = 60 * time.Minute
	agentReleaseJitter      = 60 * time.Second
	agentReleaseMinGap      = 30 * time.Second
	agentReleaseWebhookMax  = 1 << 20
)

// AgentReleases is the API's view of the newest published bot-agent
// release (api-v1 v1.62). Nil on the Server means the check is off
// (BOT_API_AGENT_RELEASE_CHECK=off): only BOT_API_LATEST_AGENT_VERSION
// counts and the webhook route answers not_found.
type AgentReleases struct {
	// Check is agentrelease.Checker.Check in production.
	Check func(context.Context) (string, error)
	// WebhookSecret is BOT_API_AGENT_RELEASE_WEBHOOK_SECRET; empty turns the
	// webhook route off and polls every 15 minutes instead of hourly.
	WebhookSecret []byte

	checked atomic.Pointer[string]
	ring    chan struct{}
	// minGap is the least time between two checks; 0 is
	// agentReleaseMinGap. Tests shorten it.
	minGap time.Duration

	mu        sync.Mutex
	failing   bool
	badSigs   int
	badLogged time.Time
}

// NewAgentReleases returns the release state for a checker.
func NewAgentReleases(check func(context.Context) (string, error), webhookSecret string) *AgentReleases {
	a := &AgentReleases{Check: check, ring: make(chan struct{}, 1)}
	if webhookSecret != "" {
		a.WebhookSecret = []byte(webhookSecret)
	}
	return a
}

// Checked is the last verified release version, or "".
func (a *AgentReleases) Checked() string {
	if a == nil {
		return ""
	}
	if p := a.checked.Load(); p != nil {
		return *p
	}
	return ""
}

func (a *AgentReleases) setChecked(v string) { a.checked.Store(&v) }

// Ring asks the checker for a check now. Rings while one is pending fold
// into it.
func (a *AgentReleases) Ring() {
	select {
	case a.ring <- struct{}{}:
	default:
	}
}

// latestAgentVersion is the higher of BOT_API_LATEST_AGENT_VERSION (the
// floor) and the verified GitHub release, or "" when neither is known
// (api-v1 v1.62). Read it once per request or sweep.
func (s *Server) latestAgentVersion() string {
	floor := s.LatestAgentVersion
	checked := s.AgentReleases.Checked()
	if !agentversion.ValidRelease(floor) {
		floor = ""
	}
	switch {
	case !agentversion.ValidRelease(checked):
		return floor
	case !agentversion.ValidRelease(floor):
		return checked
	case agentversion.Older(floor, checked):
		return checked
	default:
		return floor
	}
}

// pollInterval is 15 minutes, or 60 with a webhook secret.
func (a *AgentReleases) pollInterval() time.Duration {
	if len(a.WebhookSecret) > 0 {
		return agentReleasePollWebhook
	}
	return agentReleasePoll
}

// RunAgentReleaseCheck checks at start, then every 15 minutes (hourly with
// a webhook secret) with up to 60 s of jitter, and when the webhook rings,
// at most once per 30 s. It returns when ctx ends.
func (s *Server) RunAgentReleaseCheck(ctx context.Context) {
	a := s.AgentReleases
	if a == nil {
		return
	}
	interval := a.pollInterval()
	gap := a.minGap
	if gap == 0 {
		gap = agentReleaseMinGap
	}
	var last time.Time
	for {
		if wait := gap - time.Since(last); !last.IsZero() && wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		last = time.Now()
		s.checkAgentRelease(ctx)
		timer := time.NewTimer(interval + rand.N(agentReleaseJitter))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-a.ring:
			timer.Stop()
		}
	}
}

// checkAgentRelease runs one check and logs only changes: a new version,
// checks starting to fail, checks recovering.
func (s *Server) checkAgentRelease(ctx context.Context) {
	a := s.AgentReleases
	v, err := a.Check(ctx)
	if ctx.Err() != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		if !a.failing {
			a.failing = true
			s.logger().Warn("agent release check failed; keeping the last good version", "err", err.Error())
		}
		return
	}
	if a.failing {
		a.failing = false
		s.logger().Info("agent release check works again")
	}
	if v != a.Checked() {
		a.setChecked(v)
		s.logger().Info("agent release check: latest bot-agent is " + v)
	}
}

var deliveryRe = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

// handleAgentReleaseWebhook is POST /hooks/github/agent-release (api-v1
// v1.62, spec §4.3): GitHub's release events for bot-agent, signed with
// the webhook secret. A valid delivery only rings the checker; nothing in
// the payload is read.
func (s *Server) handleAgentReleaseWebhook(w http.ResponseWriter, r *http.Request) {
	a := s.AgentReleases
	if a == nil || len(a.WebhookSecret) == 0 {
		s.handleNotFound(w, r)
		return
	}
	// A delivery with no well-formed signature header is refused before
	// the body is read.
	sigHeader := r.Header.Get("X-Hub-Signature-256")
	if !hubSignatureForm(sigHeader) {
		s.countBadWebhook()
		writeCode(w, "unauthorized", "unauthorized")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, agentReleaseWebhookMax+1))
	if err != nil {
		writeCode(w, "bad_request", "unreadable body")
		return
	}
	if len(body) > agentReleaseWebhookMax {
		write(w, http.StatusRequestEntityTooLarge, "bad_request", "body too large", nil)
		return
	}
	if !validHubSignature(a.WebhookSecret, body, sigHeader) {
		s.countBadWebhook()
		writeCode(w, "unauthorized", "unauthorized")
		return
	}
	switch r.Header.Get("X-GitHub-Event") {
	case "ping":
		writeOK(w, http.StatusOK, nil)
	case "release":
		delivery := r.Header.Get("X-GitHub-Delivery")
		if !deliveryRe.MatchString(delivery) {
			delivery = ""
		}
		s.logger().Info("agent release webhook: checking", "delivery", delivery)
		a.Ring()
		writeOK(w, http.StatusAccepted, nil)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

var hubSigRe = regexp.MustCompile(`^sha256=[0-9a-f]{64}$`)

// hubSignatureForm reports whether header looks like GitHub's
// X-Hub-Signature-256 (lowercase hex), before any body is read.
func hubSignatureForm(header string) bool { return hubSigRe.MatchString(header) }

// validHubSignature checks GitHub's X-Hub-Signature-256: "sha256=" and the
// hex HMAC-SHA256 of body under secret, compared in constant time.
func validHubSignature(secret, body []byte, header string) bool {
	hexSig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil || len(got) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// countBadWebhook counts refused deliveries and writes at most one Warn line
// a minute, with the count since the last line.
func (s *Server) countBadWebhook() {
	a := s.AgentReleases
	a.mu.Lock()
	defer a.mu.Unlock()
	a.badSigs++
	now := time.Now()
	if now.Sub(a.badLogged) < time.Minute {
		return
	}
	s.logger().Warn("agent release webhook: refused deliveries with a bad signature", "count", a.badSigs)
	a.badSigs = 0
	a.badLogged = now
}
