// Package api is the phase-1 REST surface.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/agentws"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/auth"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/github"
	"github.com/more-than-code/bot-control-plane/api/internal/registry"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

var errBadCursor = errors.New("bad cursor")

// Server serves /v1 and /health. The agent socket is mounted separately so
// this middleware never wraps a hijacked connection.
type Server struct {
	Store         store.Store
	Auth          *auth.Service
	Hub           *agentws.Hub
	Audit         audit.Writer
	AgentWSURL    string
	ConsoleOrigin string
	TrustedProxy  string
	Log           *slog.Logger
	Now           func() time.Time
	// Secrets is nil when BOT_SECRETS_KEY_FILE is unset. Env-set writes,
	// registry writes, and deploys that need a secret then return unavailable.
	Secrets *seal.Box
	// Registry serves the v1.16 tag and digest lookups. Nil makes them unavailable.
	Registry *registry.Service
	// GitHub and GitHubStates are Connect GitHub (api-v1 v1.29 items 1–4).
	// Nil makes start and callback unavailable.
	GitHub       *github.Client
	GitHubStates *github.States
	// imageBudget, when non-zero, replaces the image task timeout.
	// Production leaves it at 0. Tests set it so a timeout does not wait
	// out the real deploy budget.
	imageBudget int
}

// delegatedPolicy says what an api_delegated session may do on a route.
// The zero value, delegatedDeny, refuses a mutating call (POST, PUT, PATCH,
// DELETE) in the route wrapper, so a write that sets nothing is refused.
// delegatedDeny does not cover reads: a read is refused only by
// delegatedDenyRead, and every read route declares delegatedAllow or
// delegatedDenyRead explicitly (TestDelegatedRouteGuard fails otherwise).
type delegatedPolicy int

const (
	delegatedDeny delegatedPolicy = iota
	delegatedAllow
	delegatedHandler
	// delegatedDenyRead also refuses a read: the org and access reads that
	// api-v1 v1.24 item 10 keeps console-only.
	delegatedDenyRead
)

type route struct {
	pattern   string
	public    bool
	fn        func(*Server, http.ResponseWriter, *http.Request)
	delegated delegatedPolicy
}

func contractRoutes() []route {
	return []route{
		{"GET /health", true, (*Server).handleHealth, delegatedAllow},
		{"GET /.well-known/oauth-authorization-server", true, (*Server).handleOAuthMetadata, delegatedAllow},
		{"POST /oauth/register", true, (*Server).handleOAuthRegister, delegatedAllow},
		{"GET /oauth/authorize", true, (*Server).handleOAuthAuthorize, delegatedAllow},
		{"POST /oauth/token", true, (*Server).handleOAuthToken, delegatedAllow},
		{"POST /oauth/revoke", true, (*Server).handleOAuthRevoke, delegatedAllow},
		{"GET /v1/auth/google/start", true, (*Server).handleGoogleStart, delegatedAllow},
		{"POST /v1/auth/google/callback", true, (*Server).handleGoogleCallback, delegatedAllow},
		{"POST /v1/auth/signout", false, (*Server).handleSignOut, delegatedAllow},
		{"GET /v1/me", false, (*Server).handleMe, delegatedAllow},
		{"GET /v1/oauth/requests/{id}", false, (*Server).handleOAuthRequestGet, delegatedAllow},
		{"POST /v1/oauth/requests/{id}/approve", false, (*Server).handleOAuthRequestApprove, delegatedHandler},
		{"POST /v1/oauth/requests/{id}/deny", false, (*Server).handleOAuthRequestDeny, delegatedHandler},
		{"GET /v1/me/grants", false, (*Server).handleGrantList, delegatedAllow},
		{"DELETE /v1/me/grants/{id}", false, (*Server).handleGrantDelete, delegatedHandler},
		{"POST /v1/auth/device/code", true, (*Server).handleDeviceCode, delegatedAllow},
		{"POST /v1/auth/device/token", true, (*Server).handleDeviceToken, delegatedAllow},
		{"GET /v1/auth/device/{user_code}", false, (*Server).handleDeviceGet, delegatedAllow},
		{"POST /v1/auth/device/{user_code}/approve", false, (*Server).handleDeviceApprove, delegatedHandler},
		{"POST /v1/auth/device/{user_code}/deny", false, (*Server).handleDeviceDeny, delegatedHandler},
		{"POST /v1/orgs", false, (*Server).handleOrgCreate, delegatedDeny},
		{"GET /v1/orgs/{id}", false, (*Server).handleOrgGet, delegatedAllow},
		{"PATCH /v1/orgs/{id}", false, (*Server).handleOrgPatch, delegatedDeny},
		{"DELETE /v1/orgs/{id}", false, (*Server).handleOrgDelete, delegatedDeny},
		{"GET /v1/orgs/{id}/members", false, (*Server).handleOrgMemberList, delegatedDenyRead},
		{"POST /v1/orgs/{id}/members", false, (*Server).handleOrgMemberAdd, delegatedDeny},
		{"PATCH /v1/orgs/{id}/members/{user_id}", false, (*Server).handleOrgMemberPatch, delegatedDeny},
		{"DELETE /v1/orgs/{id}/members/{user_id}", false, (*Server).handleOrgMemberDelete, delegatedDeny},
		{"POST /v1/orgs/{id}/transfer-ownership", false, (*Server).handleOrgTransfer, delegatedDeny},
		{"GET /v1/orgs/{id}/projects", false, (*Server).handleOrgProjects, delegatedAllow},
		{"POST /v1/orgs/{id}/projects", false, (*Server).handleOrgProjectCreate, delegatedDeny},
		{"GET /v1/orgs/{id}/hosts", false, (*Server).handleOrgHosts, delegatedAllow},
		{"POST /v1/orgs/{id}/join-tokens", false, (*Server).handleOrgJoinCreate, delegatedDeny},
		{"GET /v1/orgs/{id}/join-tokens", false, (*Server).handleOrgJoinList, delegatedDenyRead},
		{"DELETE /v1/orgs/{id}/join-tokens/{token_id}", false, (*Server).handleOrgJoinRevoke, delegatedDeny},
		{"GET /v1/orgs/{id}/audit", false, (*Server).handleOrgAudit, delegatedDenyRead},
		{"POST /v1/projects", false, (*Server).handleProjectCreate, delegatedDeny},
		{"GET /v1/projects", false, (*Server).handleProjectList, delegatedAllow},
		{"GET /v1/projects/{id}", false, (*Server).handleProjectGet, delegatedAllow},
		{"PATCH /v1/projects/{id}", false, (*Server).handleProjectPatch, delegatedDeny},
		{"DELETE /v1/projects/{id}", false, (*Server).handleProjectDelete, delegatedDeny},
		{"GET /v1/projects/{id}/members", false, (*Server).handleMemberList, delegatedAllow},
		{"POST /v1/projects/{id}/members", false, (*Server).handleMemberAdd, delegatedDeny},
		{"PATCH /v1/projects/{id}/members/{user_id}", false, (*Server).handleMemberPatch, delegatedDeny},
		{"DELETE /v1/projects/{id}/members/{user_id}", false, (*Server).handleMemberDelete, delegatedDeny},
		{"POST /v1/projects/{id}/transfer-ownership", false, (*Server).handleTransfer, delegatedDeny},
		{"POST /v1/projects/{id}/join-tokens", false, (*Server).handleJoinCreate, delegatedDeny},
		{"GET /v1/projects/{id}/join-tokens", false, (*Server).handleJoinList, delegatedDenyRead},
		{"DELETE /v1/projects/{id}/join-tokens/{token_id}", false, (*Server).handleJoinRevoke, delegatedDeny},
		{"POST /v1/projects/{id}/invites", false, (*Server).handleInviteCreate, delegatedDeny},
		{"GET /v1/projects/{id}/invites", false, (*Server).handleInviteList, delegatedAllow},
		{"DELETE /v1/projects/{id}/invites/{invite_id}", false, (*Server).handleInviteRevoke, delegatedDeny},
		{"POST /v1/invites/lookup", false, (*Server).handleInviteLookup, delegatedHandler},
		{"POST /v1/invites/accept", false, (*Server).handleInviteAccept, delegatedHandler},
		{"POST /v1/projects/{id}/catalog/parse", false, (*Server).handleCatalogParse, delegatedAllow},
		{"GET /v1/projects/{id}/catalog", false, (*Server).handleCatalogList, delegatedAllow},
		{"POST /v1/projects/{id}/catalog", false, (*Server).handleCatalogCreate, delegatedHandler},
		{"GET /v1/projects/{id}/catalog/{name}", false, (*Server).handleCatalogGet, delegatedAllow},
		{"PUT /v1/projects/{id}/catalog/{name}", false, (*Server).handleCatalogPut, delegatedHandler},
		{"DELETE /v1/projects/{id}/catalog/{name}", false, (*Server).handleCatalogDelete, delegatedHandler},
		{"GET /v1/projects/{id}/catalog/{name}/permissions", false, (*Server).handleCatalogPermGet, delegatedAllow},
		{"PUT /v1/projects/{id}/catalog/{name}/permissions", false, (*Server).handleCatalogPermPut, delegatedHandler},
		{"POST /v1/projects/{id}/catalog/{name}/run", false, (*Server).handleCatalogRun, delegatedHandler},
		{"POST /v1/projects/{id}/catalog/{name}/stop", false, (*Server).handleCatalogStop, delegatedHandler},
		{"POST /v1/projects/{id}/catalog/{name}/restart", false, (*Server).handleCatalogRestart, delegatedHandler},
		{"POST /v1/projects/{id}/catalog/{name}/logs", false, (*Server).handleCatalogLogs, delegatedAllow},
		{"POST /v1/projects/{id}/catalog/{name}/status", false, (*Server).handleCatalogStatus, delegatedAllow},
		{"POST /v1/projects/{id}/catalog/{name}/deploy", false, (*Server).handleCatalogDeploy, delegatedHandler},
		{"POST /v1/projects/{id}/catalog/{name}/rollback", false, (*Server).handleCatalogRollback, delegatedHandler},
		{"POST /v1/projects/{id}/catalog/{name}/remove", false, (*Server).handleCatalogRemove, delegatedHandler},
		{"GET /v1/projects/{id}/catalog/{name}/deploys", false, (*Server).handleCatalogDeploys, delegatedAllow},
		{"GET /v1/projects/{id}/env-sets", false, (*Server).handleEnvSetList, delegatedAllow},
		{"POST /v1/projects/{id}/env-sets", false, (*Server).handleEnvSetCreate, delegatedHandler},
		{"GET /v1/projects/{id}/env-sets/{set}", false, (*Server).handleEnvSetGet, delegatedAllow},
		{"PUT /v1/projects/{id}/env-sets/{set}", false, (*Server).handleEnvSetPut, delegatedHandler},
		{"DELETE /v1/projects/{id}/env-sets/{set}", false, (*Server).handleEnvSetDelete, delegatedHandler},
		{"PUT /v1/projects/{id}/env-sets/{set}/keys/{key}", false, (*Server).handleEnvKeyPut, delegatedDeny},
		{"DELETE /v1/projects/{id}/env-sets/{set}/keys/{key}", false, (*Server).handleEnvKeyDelete, delegatedDeny},
		{"POST /v1/projects/{id}/env-sets/{set}/secret", false, (*Server).handleEnvKeysSecret, delegatedDeny},
		{"POST /v1/projects/{id}/env-sets/{set}/keys/{key}/reveal", false, (*Server).handleEnvKeyReveal, delegatedDeny},
		// api-v1 v1.28 value requests: create decides in its handler; the fill page, fill and decline are console only.
		{"POST /v1/projects/{id}/env-sets/{set}/value-requests", false, (*Server).handleValueRequestCreate, delegatedHandler},
		{"GET /v1/value-requests/{request_id}", false, (*Server).handleValueRequestGet, delegatedAllow},
		{"GET /v1/value-requests/{request_id}/fill", false, (*Server).handleValueRequestFillPage, delegatedDenyRead},
		{"POST /v1/value-requests/{request_id}/fill", false, (*Server).handleValueRequestFill, delegatedDeny},
		{"POST /v1/value-requests/{request_id}/decline", false, (*Server).handleValueRequestDecline, delegatedDeny},
		{"GET /v1/projects/{id}/registries", false, (*Server).handleRegistryList, delegatedAllow},
		{"PUT /v1/projects/{id}/registries/{host}", false, (*Server).handleRegistryPut, delegatedHandler},
		{"DELETE /v1/projects/{id}/registries/{host}", false, (*Server).handleRegistryDelete, delegatedHandler},
		// api-v1 v1.29 Connect GitHub: console only.
		{"POST /v1/projects/{id}/registries/github/start", false, (*Server).handleGitHubStart, delegatedDeny},
		{"POST /v1/auth/github/callback", false, (*Server).handleGitHubCallback, delegatedDeny},
		// api-v1 v1.31 connect requests: create decides in its handler; the page and decline are console only.
		{"POST /v1/projects/{id}/registries/github/requests", false, (*Server).handleConnectRequestCreate, delegatedHandler},
		{"GET /v1/registry-connect-requests/{request_id}", false, (*Server).handleConnectRequestGet, delegatedAllow},
		{"GET /v1/registry-connect-requests/{request_id}/page", false, (*Server).handleConnectRequestPage, delegatedDenyRead},
		{"POST /v1/registry-connect-requests/{request_id}/decline", false, (*Server).handleConnectRequestDecline, delegatedDeny},
		// api-v1 v1.29 image access, readiness and the host-policy snippet: console or delegated.
		{"GET /v1/projects/{id}/image-access", false, (*Server).handleImageAccess, delegatedAllow},
		{"GET /v1/projects/{id}/catalog/{name}/readiness", false, (*Server).handleReadiness, delegatedAllow},
		{"GET /v1/projects/{id}/catalog/{name}/host-policy-snippet", false, (*Server).handleHostPolicySnippet, delegatedAllow},
		{"GET /v1/projects/{id}/image-tags", false, (*Server).handleImageTags, delegatedAllow},
		{"GET /v1/projects/{id}/image-digest", false, (*Server).handleImageDigest, delegatedAllow},
		{"GET /v1/projects/{id}/files", false, (*Server).handleFileList, delegatedAllow},
		{"GET /v1/projects/{id}/files/{name}", false, (*Server).handleFileGet, delegatedAllow},
		{"PUT /v1/projects/{id}/files/{name}", false, (*Server).handleFilePut, delegatedHandler},
		{"DELETE /v1/projects/{id}/files/{name}", false, (*Server).handleFileDelete, delegatedHandler},
		{"POST /v1/projects/{id}/file-requests", false, (*Server).handleFileRequestCreate, delegatedHandler},
		{"GET /v1/file-requests/{request_id}", false, (*Server).handleFileRequestGet, delegatedAllow},
		{"GET /v1/file-requests/{request_id}/fill", false, (*Server).handleFileRequestFillPage, delegatedDenyRead},
		{"POST /v1/file-requests/{request_id}/fill", false, (*Server).handleFileRequestFill, delegatedDeny},
		{"POST /v1/file-requests/{request_id}/decline", false, (*Server).handleFileRequestDecline, delegatedDeny},
		{"GET /v1/projects/{id}/hosts", false, (*Server).handleHostList, delegatedAllow},
		{"GET /v1/projects/{id}/services", false, (*Server).handleProjectServices, delegatedAllow},
		{"GET /v1/hosts/{id}", false, (*Server).handleHostGet, delegatedAllow},
		{"DELETE /v1/hosts/{id}/credential", false, (*Server).handleHostRevoke, delegatedDeny},
		{"GET /v1/hosts/{id}/access", false, (*Server).handleAccessList, delegatedDenyRead},
		{"PUT /v1/hosts/{id}/access/{project_id}", false, (*Server).handleAccessPut, delegatedDeny},
		{"DELETE /v1/hosts/{id}/access/{project_id}", false, (*Server).handleAccessDelete, delegatedDeny},
		// api-v1 v1.24 items 3 and 4: removed routes answer 410 for one release.
		{"GET /v1/hosts/{id}/shares", false, (*Server).handleSharesGone, delegatedAllow},
		{"GET /v1/hosts/{id}/shares/{project_id}", false, (*Server).handleSharesGone, delegatedAllow},
		{"PUT /v1/hosts/{id}/shares/{project_id}", false, (*Server).handleSharesGone, delegatedDeny},
		{"DELETE /v1/hosts/{id}/shares/{project_id}", false, (*Server).handleSharesGone, delegatedDeny},
		{"GET /v1/hosts/{id}/catalog-approvals", false, (*Server).handleApprovalsGone, delegatedAllow},
		{"POST /v1/hosts/{id}/catalog-approvals", false, (*Server).handleApprovalsGone, delegatedDeny},
		{"DELETE /v1/hosts/{id}/catalog-approvals/{entry_id}", false, (*Server).handleApprovalsGone, delegatedDeny},
		{"GET /v1/hosts/{id}/services", false, (*Server).handleServiceList, delegatedAllow},
		{"GET /v1/services/{id}", false, (*Server).handleServiceGet, delegatedAllow},
		{"PATCH /v1/services/{id}", false, (*Server).handleServicePatch, delegatedDeny},
		{"PUT /v1/services/{id}/health-checks", false, (*Server).handleHealthPut, delegatedHandler},
		{"GET /v1/hosts/{id}/metrics", false, (*Server).handleMetrics, delegatedAllow},
		{"GET /v1/hosts/{id}/processes", false, (*Server).handleProcesses, delegatedAllow},
		{"GET /v1/services/{id}/health", false, (*Server).handleHealthHistory, delegatedAllow},
		{"POST /v1/hosts/{id}/snapshot", false, (*Server).handleSnapshot, delegatedAllow},
		{"POST /v1/services/{id}/probe", false, (*Server).handleProbe, delegatedAllow},
		{"POST /v1/confirmations/{confirmation_id}", false, (*Server).handleConfirmation, delegatedAllow},
		{"GET /v1/projects/{id}/audit", false, (*Server).handleAudit, delegatedAllow},
	}
}

// Patterns is the frozen route table, including the method.
func Patterns() []string {
	routes := contractRoutes()
	out := make([]string, len(routes))
	for i, rt := range routes {
		out[i] = rt.pattern
	}
	return out
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Handler is the REST mux. It does not serve the agent WebSocket.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, rt := range contractRoutes() {
		s.route(mux, rt)
	}
	s.route(mux, route{pattern: "/", public: true, fn: (*Server).handleNotFound})
	return mux
}

func (s *Server) route(mux *http.ServeMux, rt route) {
	mux.HandleFunc(rt.pattern, func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		start := time.Now()
		defer func() {
			if rec := recover(); rec != nil {
				s.logger().Error("panic", "pattern", r.Pattern)
				if sw.code == 0 {
					writeCode(sw, "error", "internal error")
				}
			}
			code := sw.code
			if code == 0 {
				code = http.StatusOK
			}
			s.logger().Info("request", "method", r.Method, "pattern", r.Pattern, "status", code, "ms", time.Since(start).Milliseconds())
		}()
		if strings.Contains(rt.pattern, " /v1/") && s.rejectMCPAudience(sw, r) {
			return
		}
		// Public routes have no delegated principal. A mutating deny route
		// refuses api_delegated before the handler, so the answer does not
		// depend on whether the path id exists.
		refuse := (mutating(r.Method) && rt.delegated == delegatedDeny) || rt.delegated == delegatedDenyRead
		if !rt.public && refuse && s.rejectDelegated(sw, r, rt.pattern) {
			return
		}
		if !rt.public {
			sess, user, err := s.Auth.Authenticate(r.Context(), r.Header.Get("Authorization"))
			if err != nil {
				writeCode(sw, "unauthorized", "unauthorized")
				return
			}
			if s.now().Sub(sess.LastSeenAt) > time.Minute {
				_ = s.Store.TouchSession(r.Context(), sess.ID, s.now())
			}
			ctx := authz.WithLookup(r.Context(), s.lookup)
			ctx = authz.WithHostLookup(ctx, s.hostLookup)
			ctx = withPrincipal(ctx, principal{User: user, Session: sess})
			r = r.WithContext(ctx)
		}
		rt.fn(s, sw, r)
	})
}

func (s *Server) hostLookup(ctx context.Context, userID, hostID uuid.UUID) (authz.Relation, error) {
	facts, err := s.Store.HostFacts(ctx, userID, hostID)
	if err != nil {
		return authz.Relation{}, err
	}
	access := make([]authz.Access, len(facts.Access))
	for i, a := range facts.Access {
		access[i] = authz.Access{ProjectID: a.ProjectID, Level: a.Level, Role: a.Role}
	}
	return authz.Relate(facts.OrgID, facts.OrgRole, access), nil
}

func (s *Server) lookup(ctx context.Context, userID, projectID uuid.UUID) (authz.ProjectFacts, error) {
	pa, err := s.Store.ProjectAccess(ctx, projectID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return authz.ProjectFacts{}, nil
	}
	if err != nil {
		return authz.ProjectFacts{}, err
	}
	return authz.ProjectFacts{Found: true, MemberRole: pa.MemberRole, OrgRole: pa.OrgRole}, nil
}

type principal struct {
	User    store.User
	Session store.Session
}

type principalKey struct{}

func withPrincipal(ctx context.Context, p principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func principalFrom(ctx context.Context) (principal, bool) {
	p, ok := ctx.Value(principalKey{}).(principal)
	return p, ok
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Server) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Server) logErr(err error) {
	msg := err.Error()
	if strings.Contains(msg, "://") {
		msg = "redacted"
	}
	s.logger().Error("request failed", "err", msg)
}

func (s *Server) handleNotFound(w http.ResponseWriter, _ *http.Request) {
	writeCode(w, "not_found", "not found")
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []byte(`{"status":"ok"}`))
}

func mutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func viaKind(kind string) string {
	switch kind {
	case store.KindLegacyMCP, store.KindMCPAccess, store.KindMCPRefresh, store.KindDelegated:
		return audit.ViaMCP
	default:
		return audit.ViaConsole
	}
}

// rejectMCPAudience refuses a live MCP token on /v1/*.
// That is mcp_access, mcp_refresh, or a legacy mcp session that survived cut-over.
// A missing or unknown bearer is left for the route. Expired tokens are not live.
func (s *Server) rejectMCPAudience(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") == "" {
		return false
	}
	sess, _, err := s.Auth.Authenticate(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		return false
	}
	if sess.Kind != store.KindMCPAccess && sess.Kind != store.KindMCPRefresh && sess.Kind != store.KindLegacyMCP {
		return false
	}
	writeCode(w, "invalid_token", "invalid token")
	return true
}

// rejectDelegated refuses a live api_delegated session on a console-only write.
// A missing or invalid bearer is left for the handler, so its error code stays
// the same. Project and host stay unset: a path id may not exist, and the
// audit foreign keys would reject it.
func (s *Server) rejectDelegated(w http.ResponseWriter, r *http.Request, pattern string) bool {
	if r.Header.Get("Authorization") == "" {
		return false
	}
	sess, user, err := s.Auth.Authenticate(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		return false
	}
	if sess.Kind != store.KindDelegated {
		return false
	}
	code := "forbidden"
	s.Audit.Record(r.Context(), store.AuditRow{
		ActorUserID: idPtr(user.ID),
		Via:         audit.ViaMCP,
		Action:      audit.ActionDelegatedDenied,
		Decision:    audit.DecisionDenied,
		ErrorCode:   &code,
		Detail:      map[string]any{"route": pattern},
	})
	writeCode(w, "forbidden", "forbidden")
	return true
}

func idPtr(id uuid.UUID) *uuid.UUID {
	v := id
	return &v
}

func (s *Server) deny(w http.ResponseWriter, r *http.Request, p principal, projectID, hostID, serviceID *uuid.UUID, action string, err error) {
	s.denyDetail(w, r, p, projectID, hostID, serviceID, action, nil, err)
}

// denyHost is deny for a host event: the audit row carries the host's org
// (api-v1 v1.24 item 8) and projectID when one applies. A caller who cannot
// see the host (not_found) gets a row with no org, host, project or service
// (review A4), as orgGate does, so it stays out of that org's audit; the
// host id is kept in the detail.
func (s *Server) denyHost(w http.ResponseWriter, r *http.Request, p principal, host store.Host, projectID, serviceID *uuid.UUID, action string, err error) {
	s.denyHostDetail(w, r, p, host, projectID, serviceID, action, nil, err)
}

// denyHostDetail is denyHost with an audit detail.
func (s *Server) denyHostDetail(w http.ResponseWriter, r *http.Request, p principal, host store.Host, projectID, serviceID *uuid.UUID, action string, detail map[string]any, err error) {
	var denied *authz.Denied
	if errors.As(err, &denied) && denied.Code == "not_found" {
		hidden := map[string]any{"host_id": host.ID.String()}
		for k, v := range detail {
			hidden[k] = v
		}
		s.denyRow(w, r, p, nil, nil, nil, nil, action, hidden, err)
		return
	}
	s.denyRow(w, r, p, idPtr(host.OrgID), projectID, idPtr(host.ID), serviceID, action, detail, err)
}

func (s *Server) denyDetail(w http.ResponseWriter, r *http.Request, p principal, projectID, hostID, serviceID *uuid.UUID, action string, detail map[string]any, err error) {
	s.denyRow(w, r, p, nil, projectID, hostID, serviceID, action, detail, err)
}

func (s *Server) denyRow(w http.ResponseWriter, r *http.Request, p principal, orgID, projectID, hostID, serviceID *uuid.UUID, action string, detail map[string]any, err error) {
	var denied *authz.Denied
	if errors.As(err, &denied) {
		if mutating(r.Method) && action != "" {
			code := denied.Code
			s.Audit.Record(r.Context(), store.AuditRow{
				ActorUserID: idPtr(p.User.ID),
				Via:         viaKind(p.Session.Kind),
				OrgID:       orgID,
				ProjectID:   projectID,
				HostID:      hostID,
				ServiceID:   serviceID,
				Action:      action,
				Decision:    audit.DecisionDenied,
				ErrorCode:   &code,
				Detail:      detail,
			})
		}
		writeCode(w, denied.Code, phrase(denied.Code))
		return
	}
	s.logErr(err)
	writeCode(w, "error", "internal error")
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.writeErr(w, err, true)
}

// failInvite maps store errors the same way as fail. A database error can
// echo token_hash, so the log line does not include err.Error().
func (s *Server) failInvite(w http.ResponseWriter, err error) {
	s.writeErr(w, err, false)
}

func (s *Server) writeErr(w http.ResponseWriter, err error, log bool) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeCode(w, "not_found", "not found")
	case errors.Is(err, store.ErrConflict):
		writeCode(w, "conflict", "conflict")
	case errors.Is(err, store.ErrUnauthorized):
		writeCode(w, "unauthorized", "unauthorized")
	case errors.Is(err, store.ErrOwner):
		writeCode(w, "forbidden", "forbidden")
	case errors.Is(err, store.ErrExpired):
		writeCode(w, "bad_request", "expired")
	case errors.Is(err, store.ErrState):
		writeCode(w, "conflict", "conflict")
	case errors.Is(err, store.ErrInvalid):
		writeCode(w, "bad_request", "bad request")
	default:
		if log {
			s.logErr(err)
		} else {
			s.logger().Error("request failed")
		}
		writeCode(w, "error", "internal error")
	}
}

func (s *Server) ownerErr(w http.ResponseWriter, r *http.Request, p principal, projectID *uuid.UUID, action string, err error) {
	s.ownerRefusal(w, r, p, nil, projectID, action, err)
}

// ownerRefusal maps ErrOwner (the owner's role cannot change, and the owner
// cannot be removed) to an audited forbidden for a project or an org.
// Anything else goes to fail.
func (s *Server) ownerRefusal(w http.ResponseWriter, r *http.Request, p principal, orgID, projectID *uuid.UUID, action string, err error) {
	if errors.Is(err, store.ErrOwner) {
		s.denyRow(w, r, p, orgID, projectID, nil, nil, action, nil, &authz.Denied{Code: "forbidden"})
		return
	}
	s.fail(w, err)
}

func (s *Server) require(w http.ResponseWriter, r *http.Request) (principal, bool) {
	p, ok := principalFrom(r.Context())
	if !ok {
		writeCode(w, "unauthorized", "unauthorized")
		return principal{}, false
	}
	return p, true
}

func (s *Server) pathID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		writeCode(w, "bad_request", "invalid id")
		return uuid.Nil, false
	}
	return id, true
}

func (s *Server) hostStatus(h store.Host) string {
	if s.Hub != nil {
		return s.Hub.Status(h, s.now())
	}
	if h.RevokedAt != nil {
		return "revoked"
	}
	if !h.AlwaysOn {
		return "asleep"
	}
	return "offline"
}

func (s *Server) auditOK(ctx context.Context, p principal, row store.AuditRow) {
	row.ActorUserID = idPtr(p.User.ID)
	if row.Via == "" {
		row.Via = viaKind(p.Session.Kind)
	}
	if row.Decision == "" {
		row.Decision = audit.DecisionAllowed
	}
	s.Audit.Record(ctx, row)
}
