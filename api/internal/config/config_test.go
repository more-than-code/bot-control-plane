package config

import (
	"testing"

	"github.com/google/uuid"
)

func TestRegistryAllowPrivate(t *testing.T) {
	for val, want := range map[string]bool{"": false, "0": false, "true": false, "1": true, " 1 ": true} {
		env := map[string]string{"BOT_API_MIGRATE_DATABASE_URL": "postgres://x", "BOT_API_REGISTRY_ALLOW_PRIVATE": val}
		cfg, err := FromEnv(func(k string) string { return env[k] }, true)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.RegistryAllowPrivate != want {
			t.Fatalf("%q: %v", val, cfg.RegistryAllowPrivate)
		}
	}
}

// api-v1 v1.34 item 1: X.Y.Z or empty; anything else stops the API at start.
func TestLatestAgentVersion(t *testing.T) {
	base := map[string]string{
		"BOT_API_LISTEN":                  "127.0.0.1:8090",
		"DATABASE_URL":                    "postgres://x",
		"BOT_API_GOOGLE_CLIENT_ID":        "id",
		"BOT_API_GOOGLE_CLIENT_SECRET":    "dummy",
		"BOT_API_DEVICE_VERIFICATION_URI": "http://127.0.0.1/device",
		"BOT_API_AGENT_WS_URL":            "ws://127.0.0.1/agent/v1/ws",
		"BOT_API_OAUTH_REDIRECT_URIS":     "http://127.0.0.1/callback",
	}
	for val, want := range map[string]string{"": "", "0.2.10": "0.2.10", " 0.2.10 ": "0.2.10"} {
		env := map[string]string{"BOT_API_LATEST_AGENT_VERSION": val}
		for k, v := range base {
			env[k] = v
		}
		cfg, err := FromEnv(func(k string) string { return env[k] }, false)
		if err != nil {
			t.Fatalf("%q: %v", val, err)
		}
		if cfg.LatestAgentVersion != want {
			t.Fatalf("%q: got %q", val, cfg.LatestAgentVersion)
		}
	}
	for _, val := range []string{"v0.2.10", "0.2", "latest", "0.2.10-rc1", "0.2.10.1", "12345.0.0", "0.2.00010"} {
		env := map[string]string{"BOT_API_LATEST_AGENT_VERSION": val}
		for k, v := range base {
			env[k] = v
		}
		_, err := FromEnv(func(k string) string { return env[k] }, false)
		if err == nil || err.Error() != "BOT_API_LATEST_AGENT_VERSION must be X.Y.Z or empty" {
			t.Fatalf("%q: err %v", val, err)
		}
	}
}

func TestEdgeTLSIssuer(t *testing.T) {
	base := map[string]string{
		"BOT_API_LISTEN": ":8090", "DATABASE_URL": "postgres://x", "BOT_API_GOOGLE_CLIENT_ID": "id",
		"BOT_API_GOOGLE_CLIENT_SECRET": "s", "BOT_API_DEVICE_VERIFICATION_URI": "http://127.0.0.1/device",
		"BOT_API_AGENT_WS_URL": "ws://127.0.0.1/agent/v1/ws", "BOT_API_OAUTH_REDIRECT_URIS": "http://127.0.0.1/cb",
	}
	for val, want := range map[string]string{"": "", "internal": "internal", " internal ": "internal", "acme": "error", "Internal": "error", "letsencrypt": "error"} {
		env := map[string]string{"BOT_API_EDGE_TLS_ISSUER": val}
		for k, v := range base {
			env[k] = v
		}
		cfg, err := FromEnv(func(k string) string { return env[k] }, false)
		if want == "error" {
			if err == nil || err.Error() != "BOT_API_EDGE_TLS_ISSUER must be empty or internal" {
				t.Fatalf("%q: %v", val, err)
			}
			continue
		}
		if err != nil || cfg.EdgeTLSIssuer != want {
			t.Fatalf("%q: %q %v", val, cfg.EdgeTLSIssuer, err)
		}
	}
}

// api-v1 v1.41 item 6: BOT_API_PROBLEMS=off turns the detectors off.
func TestProblemsSwitch(t *testing.T) {
	base := map[string]string{
		"BOT_API_LISTEN": ":8090", "DATABASE_URL": "postgres://x", "BOT_API_GOOGLE_CLIENT_ID": "id",
		"BOT_API_GOOGLE_CLIENT_SECRET": "s", "BOT_API_DEVICE_VERIFICATION_URI": "http://127.0.0.1/device",
		"BOT_API_AGENT_WS_URL": "ws://127.0.0.1/agent/v1/ws", "BOT_API_OAUTH_REDIRECT_URIS": "http://127.0.0.1/cb",
	}
	for val, want := range map[string]string{"": "on", "on": "on", " off ": "off", "off": "off", "Off": "error", "0": "error"} {
		env := map[string]string{"BOT_API_PROBLEMS": val}
		for k, v := range base {
			env[k] = v
		}
		cfg, err := FromEnv(func(k string) string { return env[k] }, false)
		if want == "error" {
			if err == nil || err.Error() != "BOT_API_PROBLEMS must be empty, on or off" {
				t.Fatalf("%q: %v", val, err)
			}
			continue
		}
		if err != nil || cfg.ProblemsOff != (want == "off") {
			t.Fatalf("%q: %v %v", val, cfg.ProblemsOff, err)
		}
	}
}

// api-v1 v1.43 item 4: BOT_API_GITHUB_REFRESH=off stops GitHub token refresh.
func TestGitHubRefreshSwitch(t *testing.T) {
	base := map[string]string{
		"BOT_API_LISTEN": ":8090", "DATABASE_URL": "postgres://x", "BOT_API_GOOGLE_CLIENT_ID": "id",
		"BOT_API_GOOGLE_CLIENT_SECRET": "s", "BOT_API_DEVICE_VERIFICATION_URI": "http://127.0.0.1/device",
		"BOT_API_AGENT_WS_URL": "ws://127.0.0.1/agent/v1/ws", "BOT_API_OAUTH_REDIRECT_URIS": "http://127.0.0.1/cb",
	}
	for val, want := range map[string]string{"": "on", "on": "on", " off ": "off", "off": "off", "OFF": "error", "no": "error"} {
		env := map[string]string{"BOT_API_GITHUB_REFRESH": val}
		for k, v := range base {
			env[k] = v
		}
		cfg, err := FromEnv(func(k string) string { return env[k] }, false)
		if want == "error" {
			if err == nil || err.Error() != "BOT_API_GITHUB_REFRESH must be empty, on or off" {
				t.Fatalf("%q: %v", val, err)
			}
			continue
		}
		if err != nil || cfg.GitHubRefreshOff != (want == "off") {
			t.Fatalf("%q: %v %v", val, cfg.GitHubRefreshOff, err)
		}
	}
}

// api-v1 v1.58: BOT_API_SERVER_HOST names the host serving Deploybot (a
// UUID); empty leaves the retention check off; anything else is an error.
func TestServerHost(t *testing.T) {
	base := map[string]string{
		"BOT_API_LISTEN": ":8090", "DATABASE_URL": "postgres://x", "BOT_API_GOOGLE_CLIENT_ID": "id",
		"BOT_API_GOOGLE_CLIENT_SECRET": "s", "BOT_API_DEVICE_VERIFICATION_URI": "http://127.0.0.1/device",
		"BOT_API_AGENT_WS_URL": "ws://127.0.0.1/agent/v1/ws", "BOT_API_OAUTH_REDIRECT_URIS": "http://127.0.0.1/cb",
	}
	id := "4d0f7877-b2b7-4886-b372-80ee085ead9c"
	for val, want := range map[string]string{"": "", " " + id + " ": id, "us1-portkey-io": "error", "00000000-0000-0000-0000-000000000000": "error"} {
		env := map[string]string{"BOT_API_SERVER_HOST": val}
		for k, v := range base {
			env[k] = v
		}
		cfg, err := FromEnv(func(k string) string { return env[k] }, false)
		if want == "error" {
			if err == nil || err.Error() != "BOT_API_SERVER_HOST must be a host id (UUID) or empty" {
				t.Fatalf("%q: %v", val, err)
			}
			continue
		}
		got := ""
		if cfg.ServerHost != uuid.Nil {
			got = cfg.ServerHost.String()
		}
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", val, got, err)
		}
	}
}
