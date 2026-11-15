package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadGitHubClientSecret(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "gh")
	if err := os.WriteFile(good, []byte("abc123def456\r\nignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadGitHubClientSecret(good)
	if err != nil || got != "abc123def456" {
		t.Fatalf("read %q %v", got, err)
	}
	loose := filepath.Join(dir, "loose")
	if err := os.WriteFile(loose, []byte("abc123def456\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		loose:                        "must be mode 0600",
		link:                         "must not be a symlink",
		empty:                        "is empty",
		filepath.Join(dir, "absent"): "is missing",
		"":                           "is required",
	} {
		_, err := ReadGitHubClientSecret(path)
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "abc123") || (path != "" && strings.Contains(err.Error(), dir)) {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func TestGitHubConfigured(t *testing.T) {
	env := map[string]string{
		"BOT_API_LISTEN": ":1", "DATABASE_URL": "x", "BOT_API_GOOGLE_CLIENT_ID": "g", "BOT_API_GOOGLE_CLIENT_SECRET": "s",
		"BOT_API_DEVICE_VERIFICATION_URI": "http://d", "BOT_API_AGENT_WS_URL": "ws://a", "BOT_API_OAUTH_REDIRECT_URIS": "http://r",
		"BOT_API_GITHUB_CLIENT_ID": "cid",
	}
	cfg, err := FromEnv(func(k string) string { return env[k] }, false)
	if err != nil || cfg.GitHubConfigured() {
		t.Fatalf("id only %v %v", cfg.GitHubConfigured(), err)
	}
	env["BOT_API_GITHUB_CLIENT_SECRET_FILE"] = "/run/gh"
	cfg, err = FromEnv(func(k string) string { return env[k] }, false)
	if err != nil || !cfg.GitHubConfigured() || cfg.GitHubClientSecretFile != "/run/gh" {
		t.Fatalf("both %+v %v", cfg.GitHubConfigured(), err)
	}
}

// TestReadGitHubClientSecretSwap: the shared reader refuses a file swapped
// for a symlink between the check and the open, as for the secrets key.
func TestReadGitHubClientSecretSwap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	other := filepath.Join(dir, "other")
	for _, p := range []string{path, other} {
		if err := os.WriteFile(p, []byte("secret-"+filepath.Base(p)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	beforeSecretsOpen = func() {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(other, path); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { beforeSecretsOpen = nil })
	got, err := ReadGitHubClientSecret(path)
	if err == nil || !strings.Contains(err.Error(), "symlink") || got != "" {
		t.Fatalf("swapped %q %v", got, err)
	}
}
