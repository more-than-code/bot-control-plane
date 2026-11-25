// Package config loads the API settings from the environment.
package config

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/more-than-code/bot-control-plane/api/internal/agentversion"
)

// Config is the process configuration. Secrets come from the environment
// and are never logged.
type Config struct {
	Listen             string
	DatabaseURL        string
	MigrateDatabaseURL string
	// OrgMappingFile is BOT_API_ORG_MAPPING_FILE, read only with --migrate:
	// the org mapping for migration 0015 (spec-organizations §4.1).
	OrgMappingFile        string
	GoogleClientID        string
	GoogleClientSecret    string
	GoogleIssuer          string
	OAuthRedirectURIs     []string
	DeviceVerificationURI string
	AgentWSURL            string
	PublicOrigin          string
	TrustedProxy          string
	MCPResource           string
	ConsoleOrigin         string
	MCPClientSecretFile   string
	// SecretsKeyFile is optional. Empty means env-set and registry writes
	// that need the key are unavailable. The file contents are not stored here.
	SecretsKeyFile string
	// RegistryAllowPrivate is BOT_API_REGISTRY_ALLOW_PRIVATE=1 (dev only). It
	// lifts only the address and port checks on registry tag and digest lookups.
	RegistryAllowPrivate bool
	// GitHubClientID and GitHubClientSecretFile are the Connect GitHub OAuth
	// App (api-v1 v1.29 item 1). Without both, Connect GitHub is unavailable.
	GitHubClientID         string
	GitHubClientSecretFile string
	// LatestAgentVersion is BOT_API_LATEST_AGENT_VERSION: the newest
	// bot-agent release (X.Y.Z), or empty for no update hint (api-v1 v1.34).
	LatestAgentVersion string
	// EdgeTLSIssuer is BOT_API_EDGE_TLS_ISSUER: "" (ACME) or "internal"
	// (Caddy's internal CA, for dev and test hosts that the internet cannot
	// reach on 80 and 443; api-v1 v1.35 acceptance 3).
	EdgeTLSIssuer string
	// ProblemsOff is BOT_API_PROBLEMS=off: the problem detectors (sweep and
	// hooks) do not run and the problem routes answer empty lists (api-v1
	// v1.41 item 6). Empty or on runs them.
	ProblemsOff bool
	// ServerHost is BOT_API_SERVER_HOST: the id of the host that serves
	// this Deploybot; the owners and admins of its org get the
	// metrics_retention problem (api-v1 v1.58). Nil leaves that check off.
	ServerHost uuid.UUID
	// GitHubRefreshOff is BOT_API_GITHUB_REFRESH=off: Connect GitHub tokens
	// are not refreshed, on use or hourly; stored material is kept (api-v1
	// v1.43 item 4). Empty or on refreshes.
	GitHubRefreshOff bool
	// SecretsKey, MCPClientSecret and GitHubClientSecret are the env-value
	// forms BOT_SECRETS_KEY, BOT_API_MCP_CLIENT_SECRET and
	// BOT_API_GITHUB_CLIENT_SECRET of the three *_FILE settings (api-v1
	// v1.50 item 2, for containers). At most one form of each is set. They
	// are checked like the files by the Load* readers and never logged.
	SecretsKey         string
	MCPClientSecret    string
	GitHubClientSecret string
}

const defaultIssuer = "https://accounts.google.com"

// EdgeIssuerInternal is the one non-empty BOT_API_EDGE_TLS_ISSUER value.
const EdgeIssuerInternal = "internal"

// FromEnv reads configuration. migrateOnly skips the fields a normal
// server needs and requires only BOT_API_MIGRATE_DATABASE_URL.
func FromEnv(getenv func(string) string, migrateOnly bool) (Config, error) {
	cfg := Config{
		Listen:                 strings.TrimSpace(getenv("BOT_API_LISTEN")),
		DatabaseURL:            strings.TrimSpace(getenv("DATABASE_URL")),
		MigrateDatabaseURL:     strings.TrimSpace(getenv("BOT_API_MIGRATE_DATABASE_URL")),
		OrgMappingFile:         strings.TrimSpace(getenv("BOT_API_ORG_MAPPING_FILE")),
		GoogleClientID:         strings.TrimSpace(getenv("BOT_API_GOOGLE_CLIENT_ID")),
		GoogleClientSecret:     strings.TrimSpace(getenv("BOT_API_GOOGLE_CLIENT_SECRET")),
		GoogleIssuer:           strings.TrimSpace(getenv("BOT_API_GOOGLE_ISSUER")),
		DeviceVerificationURI:  strings.TrimSpace(getenv("BOT_API_DEVICE_VERIFICATION_URI")),
		AgentWSURL:             strings.TrimSpace(getenv("BOT_API_AGENT_WS_URL")),
		PublicOrigin:           strings.TrimSpace(getenv("BOT_API_PUBLIC_ORIGIN")),
		TrustedProxy:           strings.TrimSpace(getenv("BOT_API_TRUSTED_PROXY")),
		MCPResource:            strings.TrimSpace(getenv("BOT_API_MCP_RESOURCE")),
		ConsoleOrigin:          strings.TrimSpace(getenv("BOT_API_CONSOLE_ORIGIN")),
		MCPClientSecretFile:    strings.TrimSpace(getenv("BOT_API_MCP_CLIENT_SECRET_FILE")),
		SecretsKeyFile:         strings.TrimSpace(getenv("BOT_SECRETS_KEY_FILE")),
		RegistryAllowPrivate:   strings.TrimSpace(getenv("BOT_API_REGISTRY_ALLOW_PRIVATE")) == "1",
		GitHubClientID:         strings.TrimSpace(getenv("BOT_API_GITHUB_CLIENT_ID")),
		GitHubClientSecretFile: strings.TrimSpace(getenv("BOT_API_GITHUB_CLIENT_SECRET_FILE")),
		LatestAgentVersion:     strings.TrimSpace(getenv("BOT_API_LATEST_AGENT_VERSION")),
		EdgeTLSIssuer:          strings.TrimSpace(getenv("BOT_API_EDGE_TLS_ISSUER")),
		SecretsKey:             strings.TrimSpace(getenv("BOT_SECRETS_KEY")),
		MCPClientSecret:        strings.TrimSpace(getenv("BOT_API_MCP_CLIENT_SECRET")),
		GitHubClientSecret:     strings.TrimSpace(getenv("BOT_API_GITHUB_CLIENT_SECRET")),
	}
	// api-v1 v1.50 item 2: a secret is an env value or a file, never both.
	for _, pair := range []struct{ name, value, file string }{
		{"BOT_SECRETS_KEY", cfg.SecretsKey, cfg.SecretsKeyFile},
		{"BOT_API_MCP_CLIENT_SECRET", cfg.MCPClientSecret, cfg.MCPClientSecretFile},
		{"BOT_API_GITHUB_CLIENT_SECRET", cfg.GitHubClientSecret, cfg.GitHubClientSecretFile},
	} {
		if pair.value != "" && pair.file != "" {
			return Config{}, fmt.Errorf("%s and %s_FILE are both set; set only one", pair.name, pair.name)
		}
	}
	problems := strings.TrimSpace(getenv("BOT_API_PROBLEMS"))
	cfg.ProblemsOff = problems == "off"
	githubRefresh := strings.TrimSpace(getenv("BOT_API_GITHUB_REFRESH"))
	cfg.GitHubRefreshOff = githubRefresh == "off"
	if cfg.GoogleIssuer == "" {
		cfg.GoogleIssuer = defaultIssuer
	}
	for _, part := range strings.Split(getenv("BOT_API_OAUTH_REDIRECT_URIS"), ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			cfg.OAuthRedirectURIs = append(cfg.OAuthRedirectURIs, part)
		}
	}
	if migrateOnly {
		if cfg.MigrateDatabaseURL == "" {
			return Config{}, fmt.Errorf("BOT_API_MIGRATE_DATABASE_URL is required for --migrate")
		}
		return cfg, nil
	}
	var missing []string
	require := func(name, val string) {
		if val == "" {
			missing = append(missing, name)
		}
	}
	require("BOT_API_LISTEN", cfg.Listen)
	require("DATABASE_URL", cfg.DatabaseURL)
	require("BOT_API_GOOGLE_CLIENT_ID", cfg.GoogleClientID)
	require("BOT_API_GOOGLE_CLIENT_SECRET", cfg.GoogleClientSecret)
	require("BOT_API_DEVICE_VERIFICATION_URI", cfg.DeviceVerificationURI)
	require("BOT_API_AGENT_WS_URL", cfg.AgentWSURL)
	if len(cfg.OAuthRedirectURIs) == 0 {
		missing = append(missing, "BOT_API_OAUTH_REDIRECT_URIS")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing config: %s", strings.Join(missing, ", "))
	}
	if strings.ContainsAny(cfg.AgentWSURL, "' \t\r\n") {
		return Config{}, fmt.Errorf("BOT_API_AGENT_WS_URL must not contain quotes or whitespace")
	}
	// api-v1 v1.48: the release form the host's helper accepts (1 to 4 digits each).
	if cfg.LatestAgentVersion != "" && !agentversion.ValidRelease(cfg.LatestAgentVersion) {
		return Config{}, fmt.Errorf("BOT_API_LATEST_AGENT_VERSION must be X.Y.Z or empty")
	}
	if v := strings.TrimSpace(getenv("BOT_API_SERVER_HOST")); v != "" {
		id, err := uuid.Parse(v)
		if err != nil || id == uuid.Nil {
			return Config{}, fmt.Errorf("BOT_API_SERVER_HOST must be a host id (UUID) or empty")
		}
		cfg.ServerHost = id
	}
	if cfg.EdgeTLSIssuer != "" && cfg.EdgeTLSIssuer != EdgeIssuerInternal {
		return Config{}, fmt.Errorf("BOT_API_EDGE_TLS_ISSUER must be empty or internal")
	}
	if problems != "" && problems != "on" && problems != "off" {
		return Config{}, fmt.Errorf("BOT_API_PROBLEMS must be empty, on or off")
	}
	if githubRefresh != "" && githubRefresh != "on" && githubRefresh != "off" {
		return Config{}, fmt.Errorf("BOT_API_GITHUB_REFRESH must be empty, on or off")
	}
	return cfg, nil
}

// GitHubConfigured reports whether both Connect GitHub settings are set.
func (c Config) GitHubConfigured() bool {
	return c.GitHubClientID != "" && (c.GitHubClientSecretFile != "" || c.GitHubClientSecret != "")
}

// RedirectAllowed reports whether uri is on the OAuth allowlist.
func (c Config) RedirectAllowed(uri string) bool {
	for _, allowed := range c.OAuthRedirectURIs {
		if allowed == uri {
			return true
		}
	}
	return false
}
