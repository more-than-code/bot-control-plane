package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envBase() map[string]string {
	return map[string]string{
		"BOT_API_LISTEN":                  "127.0.0.1:8090",
		"DATABASE_URL":                    "postgres://x",
		"BOT_API_GOOGLE_CLIENT_ID":        "id",
		"BOT_API_GOOGLE_CLIENT_SECRET":    "dummy",
		"BOT_API_DEVICE_VERIFICATION_URI": "http://127.0.0.1/device",
		"BOT_API_AGENT_WS_URL":            "ws://127.0.0.1/agent/v1/ws",
		"BOT_API_OAUTH_REDIRECT_URIS":     "http://127.0.0.1/callback",
	}
}

func fromEnv(t *testing.T, extra map[string]string, migrate bool) (Config, error) {
	t.Helper()
	env := envBase()
	if migrate {
		env = map[string]string{"BOT_API_MIGRATE_DATABASE_URL": "postgres://x"}
	}
	for k, v := range extra {
		env[k] = v
	}
	return FromEnv(func(k string) string { return env[k] }, migrate)
}

// api-v1 v1.50 item 2: setting both forms is a start-up error that names
// the variable and never the value.
func TestEnvSecretBothFormsRefused(t *testing.T) {
	for _, name := range []string{"BOT_SECRETS_KEY", "BOT_API_MCP_CLIENT_SECRET", "BOT_API_GITHUB_CLIENT_SECRET"} {
		for _, migrate := range []bool{false, true} {
			_, err := fromEnv(t, map[string]string{name: "s3cret-value-never-printed", name + "_FILE": "/etc/x"}, migrate)
			if err == nil || !strings.Contains(err.Error(), name+" and "+name+"_FILE are both set") || strings.Contains(err.Error(), "s3cret") {
				t.Fatalf("%s (migrate %v): %v", name, migrate, err)
			}
		}
	}
}

// Env values alone start the server; the readers apply the file rules.
func TestEnvSecretValues(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	mcp := strings.Repeat("m", 40)
	cfg, err := fromEnv(t, map[string]string{
		"BOT_SECRETS_KEY": key, "BOT_API_MCP_CLIENT_SECRET": mcp,
		"BOT_API_GITHUB_CLIENT_SECRET": "gh-secret", "BOT_API_GITHUB_CLIENT_ID": "gh-id",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.GitHubConfigured() {
		t.Fatal("github with an env secret is not configured")
	}
	if got, err := LoadSecretsKey(cfg.SecretsKey, cfg.SecretsKeyFile); err != nil || len(got) != 32 {
		t.Fatalf("key %d %v", len(got), err)
	}
	if got, err := LoadMCPClientSecret(cfg.MCPClientSecret, cfg.MCPClientSecretFile); err != nil || got != mcp {
		t.Fatalf("mcp %v", err)
	}
	if got, err := LoadGitHubClientSecret(cfg.GitHubClientSecret, cfg.GitHubClientSecretFile); err != nil || got != "gh-secret" {
		t.Fatalf("github %v", err)
	}
	// Neither form: the key stays optional, the MCP secret is required.
	if got, err := LoadSecretsKey("", ""); err != nil || got != nil {
		t.Fatalf("no key %v %v", got, err)
	}
	if _, err := LoadMCPClientSecret("", ""); err == nil || !strings.Contains(err.Error(), "BOT_API_MCP_CLIENT_SECRET_FILE is required") {
		t.Fatalf("no mcp secret %v", err)
	}
}

func TestEnvSecretValueChecks(t *testing.T) {
	short := base64.StdEncoding.EncodeToString([]byte("short"))
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"key not base64", second(LoadSecretsKey("not base64!!", "")), "BOT_SECRETS_KEY is invalid"},
		{"key wrong length", second(LoadSecretsKey(short, "")), "BOT_SECRETS_KEY is invalid"},
		{"mcp short", second(LoadMCPClientSecret("short-secret", "")), "BOT_API_MCP_CLIENT_SECRET is too short"},
		{"mcp two lines", second(LoadMCPClientSecret(strings.Repeat("a", 32)+"\nb", "")), "BOT_API_MCP_CLIENT_SECRET is invalid"},
		{"github space", second(LoadGitHubClientSecret("a b", "")), "BOT_API_GITHUB_CLIENT_SECRET is invalid"},
		{"github two lines", second(LoadGitHubClientSecret("a\rb", "")), "BOT_API_GITHUB_CLIENT_SECRET is invalid"},
	} {
		if c.err == nil || c.err.Error() != c.want {
			t.Fatalf("%s: %v", c.name, c.err)
		}
	}
}

// The file form keeps its 0600 rule.
func TestEnvSecretFilesKeepModeRule(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecretsKey("", path); err == nil || err.Error() != "BOT_SECRETS_KEY_FILE must be mode 0600" {
		t.Fatalf("0644 key file %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadSecretsKey("", path); err != nil || len(got) != 32 {
		t.Fatalf("0600 key file %v", err)
	}
}

func second[T any](_ T, err error) error { return err }
