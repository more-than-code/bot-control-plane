package mcp

import (
	"strings"
	"testing"
)

// api-v1 v1.50 item 2: BOT_MCP_CLIENT_SECRET as an env value.
func TestClientSecretEnvValue(t *testing.T) {
	secret := strings.Repeat("s", 40)
	env := map[string]string{
		"BOT_MCP_LISTEN":        "127.0.0.1:8091",
		"BOT_MCP_API_URL":       "https://api.example",
		"BOT_MCP_PUBLIC_ORIGIN": "https://mcp.example",
		"BOT_MCP_CLIENT_SECRET": secret,
	}
	load := func() (config, error) { return configFrom(func(k string) string { return env[k] }) }
	cfg, err := load()
	if err != nil || cfg.ClientSecret != secret {
		t.Fatalf("env secret: %v", err)
	}
	env["BOT_MCP_CLIENT_SECRET_FILE"] = "/etc/x"
	if _, err := load(); err == nil || !strings.Contains(err.Error(), "BOT_MCP_CLIENT_SECRET and BOT_MCP_CLIENT_SECRET_FILE are both set") || strings.Contains(err.Error(), secret) {
		t.Fatalf("both forms: %v", err)
	}
	delete(env, "BOT_MCP_CLIENT_SECRET_FILE")
	for value, want := range map[string]string{
		"short":                         "BOT_MCP_CLIENT_SECRET must be at least 32 characters",
		strings.Repeat("a", 32) + "\nb": "BOT_MCP_CLIENT_SECRET is invalid",
		strings.Repeat("a", 4097):       "BOT_MCP_CLIENT_SECRET is invalid",
	} {
		env["BOT_MCP_CLIENT_SECRET"] = value
		if _, err := load(); err == nil || err.Error() != want {
			t.Fatalf("%.10q: %v", value, err)
		}
	}
}
