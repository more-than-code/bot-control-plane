package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// api-v1 v1.62: BOT_API_AGENT_RELEASE_CHECK and the release webhook secret.

func releaseBaseEnv() map[string]string {
	return map[string]string{
		"BOT_API_LISTEN": ":8090", "DATABASE_URL": "postgres://x", "BOT_API_GOOGLE_CLIENT_ID": "id",
		"BOT_API_GOOGLE_CLIENT_SECRET": "s", "BOT_API_DEVICE_VERIFICATION_URI": "http://127.0.0.1/device",
		"BOT_API_AGENT_WS_URL": "ws://127.0.0.1/agent/v1/ws", "BOT_API_OAUTH_REDIRECT_URIS": "http://127.0.0.1/cb",
	}
}

func TestAgentReleaseCheckSwitch(t *testing.T) {
	for val, want := range map[string]string{"": "on", "on": "on", " off ": "off", "OFF": "error", "maybe": "error"} {
		env := releaseBaseEnv()
		env["BOT_API_AGENT_RELEASE_CHECK"] = val
		cfg, err := FromEnv(func(k string) string { return env[k] }, false)
		if want == "error" {
			if err == nil || err.Error() != "BOT_API_AGENT_RELEASE_CHECK must be on or off" {
				t.Fatalf("%q: %v", val, err)
			}
			continue
		}
		if err != nil || cfg.AgentReleaseCheckOff != (want == "off") {
			t.Fatalf("%q: %v %v", val, cfg.AgentReleaseCheckOff, err)
		}
	}
}

func TestAgentReleaseWebhookSecretBothForms(t *testing.T) {
	env := releaseBaseEnv()
	env["BOT_API_AGENT_RELEASE_WEBHOOK_SECRET"] = strings.Repeat("a", 32)
	env["BOT_API_AGENT_RELEASE_WEBHOOK_SECRET_FILE"] = "/run/secrets/hook"
	_, err := FromEnv(func(k string) string { return env[k] }, false)
	if err == nil || !strings.Contains(err.Error(), "both set") {
		t.Fatalf("both: %v", err)
	}
}

func TestLoadAgentReleaseWebhookSecret(t *testing.T) {
	long := strings.Repeat("s", 32)
	if got, err := LoadAgentReleaseWebhookSecret("", ""); got != "" || err != nil {
		t.Fatalf("unset: %q %v", got, err)
	}
	if got, err := LoadAgentReleaseWebhookSecret(long, ""); got != long || err != nil {
		t.Fatalf("value: %q %v", got, err)
	}
	_, err := LoadAgentReleaseWebhookSecret(long[:31], "")
	if err == nil || err.Error() != "BOT_API_AGENT_RELEASE_WEBHOOK_SECRET must be at least 32 characters" || strings.Contains(err.Error(), long[:31]) {
		t.Fatalf("short: %v", err)
	}
	if _, err := LoadAgentReleaseWebhookSecret(long+"\nx", ""); err == nil {
		t.Fatal("two lines accepted")
	}
	path := filepath.Join(t.TempDir(), "hook")
	if err := os.WriteFile(path, []byte(long+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadAgentReleaseWebhookSecret("", path); got != long || err != nil {
		t.Fatalf("file: %q %v", got, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgentReleaseWebhookSecret("", path); err == nil {
		t.Fatal("mode 0644 accepted")
	}
}
