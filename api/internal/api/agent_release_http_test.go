package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// api-v1 v1.62: the API learns the latest bot-agent release by itself.

const hookSecret = "0123456789abcdef0123456789abcdef-test"

func TestLatestAgentVersionFloor(t *testing.T) {
	cases := []struct{ floor, checked, want string }{
		{"", "", ""},
		{"0.2.17", "", "0.2.17"},
		{"", "0.2.19", "0.2.19"},
		{"0.2.17", "0.2.19", "0.2.19"},
		{"0.2.20", "0.2.19", "0.2.20"},
		{"0.2.19", "0.2.19", "0.2.19"},
		{"0.10.0", "0.9.9", "0.10.0"},
	}
	for _, tc := range cases {
		s := &Server{LatestAgentVersion: tc.floor}
		if tc.checked != "" {
			s.AgentReleases = NewAgentReleases(nil, "")
			s.AgentReleases.setChecked(tc.checked)
		}
		if got := s.latestAgentVersion(); got != tc.want {
			t.Fatalf("floor %q checked %q: got %q want %q", tc.floor, tc.checked, got, tc.want)
		}
	}
	// Check off (nil AgentReleases) is the setting alone.
	s := &Server{LatestAgentVersion: "0.2.17"}
	if s.latestAgentVersion() != "0.2.17" {
		t.Fatal("nil releases")
	}
}

// fakeCheck answers versions in order, then the last one; an empty entry
// is a failed check.
type fakeCheck struct {
	answers []string
	calls   atomic.Int32
}

func (f *fakeCheck) check(context.Context) (string, error) {
	n := int(f.calls.Add(1)) - 1
	if n >= len(f.answers) {
		n = len(f.answers) - 1
	}
	if f.answers[n] == "" {
		return "", errors.New("lookup failed")
	}
	return f.answers[n], nil
}

func TestCheckAgentReleaseKeepsLastGoodAndFollowsDowngrade(t *testing.T) {
	var logs bytes.Buffer
	f := &fakeCheck{answers: []string{"0.2.19", "", "", "0.2.18"}}
	s := &Server{Log: slog.New(slog.NewTextHandler(&logs, nil)), AgentReleases: NewAgentReleases(f.check, "")}
	ctx := context.Background()
	s.checkAgentRelease(ctx)
	if s.latestAgentVersion() != "0.2.19" {
		t.Fatalf("first %q", s.latestAgentVersion())
	}
	s.checkAgentRelease(ctx)
	s.checkAgentRelease(ctx)
	if s.latestAgentVersion() != "0.2.19" {
		t.Fatal("a failed check dropped the last good version")
	}
	if n := strings.Count(logs.String(), "agent release check failed"); n != 1 {
		t.Fatalf("%d failure lines:\n%s", n, logs.String())
	}
	// D3: GitHub's latest moved back, so the API follows it.
	s.checkAgentRelease(ctx)
	if s.latestAgentVersion() != "0.2.18" {
		t.Fatalf("downgrade %q", s.latestAgentVersion())
	}
	if !strings.Contains(logs.String(), "works again") || !strings.Contains(logs.String(), "latest bot-agent is 0.2.18") {
		t.Fatalf("logs:\n%s", logs.String())
	}
}

func TestAgentUpgradeUsesCheckedRelease(t *testing.T) {
	c := newUpgradeCast(t, upgradePrograms(true))
	c.w.srv.LatestAgentVersion = "0.2.13" // the floor, below the release
	f := &fakeCheck{answers: []string{upgradeLatest}}
	c.w.srv.AgentReleases = NewAgentReleases(f.check, "")
	// Before the first check only the floor counts.
	c.upgrade(t, c.ownerTok, http.StatusBadRequest, "bad_request")
	c.w.srv.checkAgentRelease(context.Background())
	code, env := c.w.do(c.ownerTok, http.MethodGet, c.base, "")
	if code != http.StatusOK {
		t.Fatalf("host %d", code)
	}
	if h := objectPayload(t, env); h["latest_agent_version"] != upgradeLatest || h["update_available"] != true {
		t.Fatalf("host %#v", h)
	}
	c.setReply(accepted())
	c.upgrade(t, c.ownerTok, http.StatusAccepted, "ok")
	if rows := c.rows(t); len(rows) != 1 || rows[len(rows)-1].To != upgradeLatest {
		t.Fatalf("rows %+v", rows)
	}
}

func signHook(body string) string {
	mac := hmac.New(sha256.New, []byte(hookSecret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (w *world) hook(event, sig, body string) int {
	w.t.Helper()
	req, err := http.NewRequest(http.MethodPost, w.http.URL+"/hooks/github/agent-release", strings.NewReader(body))
	if err != nil {
		w.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
	}
	if sig != "" {
		req.Header.Set("X-Hub-Signature-256", sig)
	}
	req.Header.Set("X-GitHub-Delivery", "72d3162e-cc78-11e3-81ab-4c9367dc0958")
	res, err := w.client.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func rung(a *AgentReleases) bool {
	select {
	case <-a.ring:
		return true
	default:
		return false
	}
}

func TestAgentReleaseWebhook(t *testing.T) {
	w := newWorld(t)
	body := `{"action":"published","release":{"tag_name":"v9.9.9"}}`

	// No AgentReleases, or no secret: the route does not exist.
	if code := w.hook("release", signHook(body), body); code != http.StatusNotFound {
		t.Fatalf("check off: %d", code)
	}
	w.srv.AgentReleases = NewAgentReleases(nil, "")
	if code := w.hook("release", signHook(body), body); code != http.StatusNotFound {
		t.Fatalf("no secret: %d", code)
	}

	a := NewAgentReleases(nil, hookSecret)
	w.srv.AgentReleases = a
	cases := []struct {
		name, event, sig, body string
		want                   int
		ring                   bool
	}{
		{"ping", "ping", signHook(body), body, http.StatusOK, false},
		{"release", "release", signHook(body), body, http.StatusAccepted, true},
		{"other event", "push", signHook(body), body, http.StatusNoContent, false},
		{"no signature", "release", "", body, http.StatusUnauthorized, false},
		{"wrong signature", "release", signHook(body + " "), body, http.StatusUnauthorized, false},
		{"sha1 form", "release", "sha1=" + strings.Repeat("0", 40), body, http.StatusUnauthorized, false},
		{"not hex", "release", "sha256=zz", body, http.StatusUnauthorized, false},
	}
	for _, tc := range cases {
		if code := w.hook(tc.event, tc.sig, tc.body); code != tc.want {
			t.Fatalf("%s: %d want %d", tc.name, code, tc.want)
		}
		if got := rung(a); got != tc.ring {
			t.Fatalf("%s: rung %v", tc.name, got)
		}
	}
	big := strings.Repeat("x", agentReleaseWebhookMax+1)
	if code := w.hook("release", signHook(big), big); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("big: %d", code)
	}
	// The payload is never read: the version stays unknown until a check.
	if w.srv.latestAgentVersion() != "" {
		t.Fatalf("payload trusted: %q", w.srv.latestAgentVersion())
	}
}

func TestAgentReleaseRingsFold(t *testing.T) {
	a := NewAgentReleases(nil, hookSecret)
	for i := 0; i < 10; i++ {
		a.Ring()
	}
	if !rung(a) || rung(a) {
		t.Fatal("rings did not fold into one")
	}
}

func TestRunAgentReleaseCheckRingAndGap(t *testing.T) {
	f := &fakeCheck{answers: []string{"0.2.19"}}
	s := &Server{Log: slog.New(slog.DiscardHandler), AgentReleases: NewAgentReleases(f.check, hookSecret)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.RunAgentReleaseCheck(ctx); close(done) }()
	waitFor(t, func() bool { return f.calls.Load() == 1 })
	// A burst of rings right after a check: at most one more check, and
	// not before the 30 s gap.
	for i := 0; i < 10; i++ {
		s.AgentReleases.Ring()
	}
	time.Sleep(100 * time.Millisecond)
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("%d checks inside the gap", n)
	}
	cancel()
	<-done
}

func TestValidHubSignature(t *testing.T) {
	body := []byte(`{}`)
	if !validHubSignature([]byte(hookSecret), body, signHook(`{}`)) {
		t.Fatal("good")
	}
	if validHubSignature([]byte("other-secret-other-secret-other-secret"), body, signHook(`{}`)) {
		t.Fatal("other secret")
	}
	if validHubSignature([]byte(hookSecret), body, strings.ToUpper(signHook(`{}`))) {
		t.Fatal("prefix is case sensitive")
	}
}
