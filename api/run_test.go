package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/config"
	"github.com/more-than-code/bot-control-plane/api/internal/db"
)

func TestReadTimeoutIsSet(t *testing.T) {
	body, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "ReadTimeout:") || !strings.Contains(text, "30 * time.Second") {
		t.Fatal("REST ReadTimeout of 30s is missing")
	}
}

func TestRunMigrateIsOptIn(t *testing.T) {
	var opened, migrated, served bool
	d := deps{
		open: func(context.Context, string) (*db.DB, error) {
			opened = true
			return nil, errors.New("database connection failed")
		},
		migrate: func(context.Context, string, string) error {
			migrated = true
			return nil
		},
		serve: func(string, http.Handler, func()) error {
			served = true
			return nil
		},
	}
	getenv := func(k string) string {
		switch k {
		case "BOT_API_LISTEN":
			return "127.0.0.1:0"
		case "DATABASE_URL":
			return "postgres://example.invalid/deploybot"
		case "BOT_API_GOOGLE_CLIENT_ID":
			return "dummy-google-client-id"
		case "BOT_API_GOOGLE_CLIENT_SECRET":
			return "dummy-google-client-secret"
		case "BOT_API_DEVICE_VERIFICATION_URI":
			return "http://127.0.0.1/device"
		case "BOT_API_AGENT_WS_URL":
			return "ws://127.0.0.1/agent/v1/ws"
		case "BOT_API_OAUTH_REDIRECT_URIS":
			return "http://127.0.0.1/callback"
		default:
			return ""
		}
	}
	if err := run(context.Background(), nil, getenv, d); err == nil {
		t.Fatal("expected open to fail")
	}
	if !opened || migrated || served {
		t.Fatalf("opened=%v migrated=%v served=%v", opened, migrated, served)
	}

	opened, migrated, served = false, false, false
	err := run(context.Background(), []string{"--migrate"}, func(k string) string {
		if k == "BOT_API_MIGRATE_DATABASE_URL" {
			return "postgres://example.invalid/deploybot"
		}
		return ""
	}, d)
	if err != nil {
		t.Fatal(err)
	}
	if opened || !migrated || served {
		t.Fatalf("opened=%v migrated=%v served=%v", opened, migrated, served)
	}
}

func TestRunRejectsMCPSecretMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp-secret")
	if err := os.WriteFile(path, []byte("test-mcp-client-secret-0123456789ab\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	var opened bool
	d := deps{
		open: func(context.Context, string) (*db.DB, error) {
			opened = true
			return nil, errors.New("database connection failed")
		},
		migrate: func(context.Context, string, string) error { return nil },
		serve:   func(string, http.Handler, func()) error { return nil },
	}
	getenv := func(k string) string {
		switch k {
		case "BOT_API_LISTEN":
			return "127.0.0.1:0"
		case "DATABASE_URL":
			return "postgres://example.invalid/deploybot"
		case "BOT_API_GOOGLE_CLIENT_ID":
			return "dummy-google-client-id"
		case "BOT_API_GOOGLE_CLIENT_SECRET":
			return "dummy-google-client-secret"
		case "BOT_API_DEVICE_VERIFICATION_URI":
			return "http://127.0.0.1/device"
		case "BOT_API_AGENT_WS_URL":
			return "ws://127.0.0.1/agent/v1/ws"
		case "BOT_API_OAUTH_REDIRECT_URIS":
			return "http://127.0.0.1/callback"
		case "BOT_API_MCP_RESOURCE":
			return "http://mcp.example"
		case "BOT_API_MCP_CLIENT_SECRET_FILE":
			return path
		default:
			return ""
		}
	}
	err := run(context.Background(), nil, getenv, d)
	if err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("mode error: %v", err)
	}
	if opened {
		t.Fatal("bad secret file opened the database")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	err = run(context.Background(), nil, getenv, d)
	if err == nil || !opened {
		t.Fatalf("0600 file err=%v opened=%v", err, opened)
	}
}

func walkGo(root string) ([]string, error) {
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && (info.Name() == ".git" || info.Name() == "vendor") {
			return filepath.SkipDir
		}
		// Test files quote SQL phrases when they assert on migration text.
		// They are not a server path that runs DDL.
		if !info.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func containsAny(s, sub string) bool {
	return strings.Contains(s, sub)
}

func TestNoDDLInGo(t *testing.T) {
	// The phrases are split so this file does not contain them.
	needles := []string{"CREATE " + "TABLE", "create_" + "hypertable"}
	root := "."
	entries, err := walkGo(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range entries {
		if containsAny(path, "migrations/") {
			continue
		}
		body := readFile(t, path)
		for _, n := range needles {
			if containsAny(body, n) {
				t.Errorf("%s contains %s", path, n)
			}
		}
	}
}

func TestRunUnknownArgumentExitsOne(t *testing.T) {
	if code := Run(context.Background(), []string{"--bogus"}); code != 1 {
		t.Fatalf("Run(--bogus) = %d, want 1", code)
	}
}

func TestOrgSource(t *testing.T) {
	if got, err := orgSource(""); err != nil || got.Chosen {
		t.Fatalf("unset %+v %v", got, err)
	}
	if got, err := orgSource("none"); err != nil || !got.Chosen || got.Mapping != nil {
		t.Fatalf("none %+v %v", got, err)
	}
	if _, err := orgSource(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing file accepted")
	}
	path := filepath.Join(t.TempDir(), "orgs.json")
	if err := os.WriteFile(path, []byte(`{"orgs":[{"slug":"mtc","name":"MTC","owner_email":"o@example.com","projects":["p"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := orgSource(path); err != nil || !got.Chosen || got.Mapping == nil {
		t.Fatalf("file %+v %v", got, err)
	}
}

// TestGitHubClientConfig is api-v1 v1.29 item 1: both settings and a console
// origin turn Connect GitHub on; a set file that fails the checks stops startup.
func TestGitHubClientConfig(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "gh")
	if err := os.WriteFile(good, []byte("gh-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loose := filepath.Join(dir, "loose")
	if err := os.WriteFile(loose, []byte("gh-secret-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{GitHubClientID: "cid", GitHubClientSecretFile: good, ConsoleOrigin: "http://console.test"}
	if c, err := githubClient(cfg); err != nil || c == nil {
		t.Fatalf("configured %v %v", c, err)
	}
	for name, c := range map[string]config.Config{
		"no id":     {GitHubClientSecretFile: good, ConsoleOrigin: "http://console.test"},
		"no file":   {GitHubClientID: "cid", ConsoleOrigin: "http://console.test"},
		"no origin": {GitHubClientID: "cid", GitHubClientSecretFile: good},
	} {
		if got, err := githubClient(c); err != nil || got != nil {
			t.Fatalf("%s: %v %v", name, got, err)
		}
	}
	cfg.GitHubClientSecretFile = loose
	if _, err := githubClient(cfg); err == nil || strings.Contains(err.Error(), "gh-secret-value") {
		t.Fatalf("loose file %v", err)
	}
}

// TestStartProblemsOff is spec-bell-problems L8: with BOT_API_PROBLEMS=off
// the API sets no deploy hook on the hub and starts no sweep; otherwise it
// does both.
func TestStartProblemsOff(t *testing.T) {
	for _, value := range []string{"off", "on", ""} {
		getenv := func(k string) string {
			switch k {
			case "BOT_API_LISTEN":
				return "127.0.0.1:0"
			case "DATABASE_URL":
				return "postgres://example.invalid/deploybot"
			case "BOT_API_GOOGLE_CLIENT_ID":
				return "dummy-google-client-id"
			case "BOT_API_GOOGLE_CLIENT_SECRET":
				return "dummy-google-client-secret"
			case "BOT_API_DEVICE_VERIFICATION_URI":
				return "http://127.0.0.1/device"
			case "BOT_API_AGENT_WS_URL":
				return "ws://127.0.0.1/agent/v1/ws"
			case "BOT_API_OAUTH_REDIRECT_URIS":
				return "http://127.0.0.1/callback"
			case "BOT_API_PROBLEMS":
				return value
			}
			return ""
		}
		cfg, err := config.FromEnv(getenv, false)
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		hooked := false
		swept := make(chan struct{}, 1)
		startProblems(context.Background(), cfg.ProblemsOff, slog.New(slog.NewTextHandler(io.Discard, nil)),
			func() { hooked = true }, func(context.Context) { swept <- struct{}{} })
		if value == "off" {
			select {
			case <-swept:
				t.Fatal("off: the sweep started")
			case <-time.After(50 * time.Millisecond):
			}
			if hooked {
				t.Fatal("off: the deploy hook was set")
			}
			continue
		}
		select {
		case <-swept:
		case <-time.After(5 * time.Second):
			t.Fatalf("%q: no sweep", value)
		}
		if !hooked {
			t.Fatalf("%q: no deploy hook", value)
		}
	}
}

// TestStartBackgroundUpkeep is spec-github-token-refresh §10.1 (T4): the
// GitHub login upkeep starts with BOT_API_PROBLEMS=off, when the problem
// sweep does not, and also with the problems on.
func TestStartBackgroundUpkeep(t *testing.T) {
	for _, problemsOff := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		swept := make(chan struct{}, 1)
		upkept := make(chan struct{}, 1)
		startBackground(ctx, config.Config{ProblemsOff: problemsOff}, slog.New(slog.NewTextHandler(io.Discard, nil)),
			func() {}, func(context.Context) { swept <- struct{}{} }, func(context.Context) { upkept <- struct{}{} })
		select {
		case <-upkept:
		case <-time.After(5 * time.Second):
			t.Fatalf("problems off %v: upkeep not started", problemsOff)
		}
		if problemsOff {
			select {
			case <-swept:
				t.Fatal("problems off: the sweep started")
			case <-time.After(50 * time.Millisecond):
			}
		} else {
			select {
			case <-swept:
			case <-time.After(5 * time.Second):
				t.Fatal("problems on: no sweep")
			}
		}
		cancel()
	}
}

type fakeReleaseStore struct {
	calls []string
	err   error
}

func (f *fakeReleaseStore) RecordReleaseServed(_ context.Context, v string, _ time.Time) error {
	f.calls = append(f.calls, v)
	return f.err
}

// api-v1 v1.57: the API records its own version at startup; "unknown" (an
// unparsable CHANGELOG) is not recorded, and a failure only logs one line.
func TestRecordReleaseServed(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	f := &fakeReleaseStore{}
	recordReleaseServed(context.Background(), f, "0.2.48", time.Now(), log)
	recordReleaseServed(context.Background(), f, "unknown", time.Now(), log)
	if len(f.calls) != 1 || f.calls[0] != "0.2.48" || buf.Len() != 0 {
		t.Fatalf("calls %v log %q", f.calls, buf.String())
	}
	// The cause is logged so an operator can tell a missing grant from a
	// missing table, but never a connection URL.
	f.err = errors.New("permission denied for table release_first_served")
	recordReleaseServed(context.Background(), f, "0.2.48", time.Now(), log)
	f.err = errors.New("dial postgres://deploybot_api:secret@db/deploybot")
	recordReleaseServed(context.Background(), f, "0.2.48", time.Now(), log)
	out := buf.String()
	if strings.Count(out, "\n") != 2 || strings.Count(out, "level=WARN") != 2 || !strings.Contains(out, "permission denied") ||
		strings.Contains(out, "secret") || !strings.Contains(out, "err=redacted") {
		t.Fatalf("log %q", out)
	}
}
