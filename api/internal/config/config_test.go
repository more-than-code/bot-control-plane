package config

import "testing"

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
	for _, val := range []string{"v0.2.10", "0.2", "latest", "0.2.10-rc1", "0.2.10.1"} {
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
