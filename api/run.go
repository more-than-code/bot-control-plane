package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/agentws"
	"github.com/more-than-code/bot-control-plane/api/internal/api"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/auth"
	"github.com/more-than-code/bot-control-plane/api/internal/config"
	"github.com/more-than-code/bot-control-plane/api/internal/db"
	"github.com/more-than-code/bot-control-plane/api/internal/github"
	"github.com/more-than-code/bot-control-plane/api/internal/registry"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
)

type deps struct {
	open    func(context.Context, string) (*db.DB, error)
	migrate func(ctx context.Context, url, orgMappingFile string) error
	serve   func(addr string, h http.Handler, stop func()) error
}

// Run is the API server's former main. args are the arguments after the
// program name or subcommand. It returns the process exit code.
func Run(ctx context.Context, args []string) int {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(log)
	err := run(ctx, args, os.Getenv, deps{
		open:    db.Open,
		migrate: migrate,
		serve:   serve,
	})
	if err != nil {
		log.Error("startup failed", "err", safeErr(err))
		return 1
	}
	return 0
}

// migrate reads BOT_API_ORG_MAPPING_FILE (unset, "none" for the default
// rule, or a mapping file), then applies the embedded migrations.
func migrate(ctx context.Context, url, orgMappingFile string) error {
	orgs, err := orgSource(orgMappingFile)
	if err != nil {
		return err
	}
	return db.MigrateWith(ctx, url, orgs)
}

func orgSource(orgMappingFile string) (db.OrgSource, error) {
	switch orgMappingFile {
	case "":
		return db.OrgSource{}, nil
	case "none":
		return db.OrgSource{Chosen: true}, nil
	}
	m, err := db.LoadOrgMapping(orgMappingFile)
	if err != nil {
		return db.OrgSource{}, err
	}
	return db.OrgSource{Chosen: true, Mapping: m}, nil
}

func run(ctx context.Context, args []string, getenv func(string) string, d deps) error {
	migrateOnly := false
	for _, arg := range args {
		if arg != "--migrate" {
			return errors.New("unknown argument")
		}
		migrateOnly = true
	}
	cfg, err := config.FromEnv(getenv, migrateOnly)
	if err != nil {
		return err
	}
	if migrateOnly {
		return d.migrate(ctx, cfg.MigrateDatabaseURL, cfg.OrgMappingFile)
	}
	var mcpSecret string
	if cfg.MCPResource != "" {
		mcpSecret, err = config.ReadMCPClientSecret(cfg.MCPClientSecretFile)
		if err != nil {
			return err
		}
	}
	key, err := config.ReadSecretsKey(cfg.SecretsKeyFile)
	if err != nil {
		return err
	}
	var secrets *seal.Box
	if key != nil {
		secrets, err = seal.New(key)
		if err != nil {
			return err
		}
	}
	gh, err := githubClient(cfg)
	if err != nil {
		return err
	}
	pool, err := d.open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	now := func() time.Time { return time.Now().UTC() }
	log := slog.Default()
	aud := audit.Writer{Store: pool, Log: log, Now: now}
	hub := agentws.New(pool, aud, log, now)
	authn := auth.New(pool, cfg, now)
	if mcpSecret != "" {
		if err := authn.SetMCPClientSecret(ctx, mcpSecret); err != nil {
			return err
		}
		log.Info("mcp oauth client ready")
	}
	srv := &api.Server{
		Store:         pool,
		Auth:          authn,
		Hub:           hub,
		Audit:         aud,
		AgentWSURL:    cfg.AgentWSURL,
		ConsoleOrigin: cfg.ConsoleOrigin,
		TrustedProxy:  cfg.TrustedProxy,
		Log:           log,
		Now:           now,
		Secrets:       secrets,
		Registry:      registry.NewService(registry.Options{AllowPrivate: cfg.RegistryAllowPrivate}),
		// api-v1 v1.34: the agent update hint on host reads.
		LatestAgentVersion: cfg.LatestAgentVersion,
	}
	if gh != nil {
		srv.GitHub = gh
		srv.GitHubStates = github.NewStates()
		log.Info("connect github ready")
	} else if cfg.GitHubClientID != "" || cfg.GitHubClientSecretFile != "" {
		log.Warn("connect github is off: it needs BOT_API_GITHUB_CLIENT_ID, BOT_API_GITHUB_CLIENT_SECRET_FILE and BOT_API_CONSOLE_ORIGIN")
	}
	if cfg.RegistryAllowPrivate {
		log.Warn("registry lookups may reach private addresses and other ports (BOT_API_REGISTRY_ALLOW_PRIVATE=1, dev only)")
	}
	root := http.NewServeMux()
	root.Handle("GET /agent/v1/ws", hub)
	root.Handle("/", srv.Handler())
	log.Info("listening", "addr", cfg.Listen, "public_origin", cfg.PublicOrigin)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go housekeep(ctx, pool, srv.Auth, now)
	return d.serve(cfg.Listen, root, hub.CloseAll)
}

// githubClient is the Connect GitHub client (api-v1 v1.29 item 1), or nil
// when the client id, the secret file or the console origin is not set. A
// set secret file that fails the file checks stops the server, as for the
// secrets key.
func githubClient(cfg config.Config) (*github.Client, error) {
	if !cfg.GitHubConfigured() {
		return nil, nil
	}
	secret, err := config.ReadGitHubClientSecret(cfg.GitHubClientSecretFile)
	if err != nil {
		return nil, err
	}
	if cfg.ConsoleOrigin == "" {
		return nil, nil
	}
	return github.New(github.Config{ClientID: cfg.GitHubClientID, ClientSecret: secret}), nil
}

func housekeep(ctx context.Context, pool *db.DB, authn *auth.Service, now func() time.Time) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	var minutes int
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			authn.Sweep(now())
			minutes++
			if minutes%60 != 0 {
				continue
			}
			if err := authn.PurgeDevices(ctx); err != nil {
				slog.Error("device purge failed")
			}
			if err := authn.PurgeOAuthClients(ctx); err != nil {
				slog.Error("oauth client purge failed")
			}
			if err := pool.DeleteOldBatches(ctx, now().Add(-48*time.Hour)); err != nil {
				slog.Error("batch purge failed")
			}
		}
	}
}

func serve(addr string, h http.Handler, stop func()) error {
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-sig:
		if stop != nil {
			stop()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(ctx)
	}
}

func safeErr(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "://") {
		return "redacted"
	}
	return msg
}
