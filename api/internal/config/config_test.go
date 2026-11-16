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
