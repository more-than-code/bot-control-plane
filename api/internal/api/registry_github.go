package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/github"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/secret"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Connect GitHub error texts (api-v1 v1.29 items 1–3).
const (
	msgGitHubOff     = "GitHub is not configured"
	msgGitHubState   = "this GitHub sign-in expired or was already used; start again"
	msgGitHubScope   = "GitHub did not grant read:packages"
	msgGitHubFailed  = "GitHub sign-in failed; try again"
	githubCallPath   = "/auth/github/callback"
	githubRevokeWait = 10 * time.Second
	maxGitHubCode    = 512
)

// githubReady is item 1 configured: the OAuth App and the console origin.
func (s *Server) githubReady() bool {
	return s.GitHub != nil && s.GitHubStates != nil && s.ConsoleOrigin != ""
}

// githubConnect is the registries list's github_connect (item 5): configured
// and a secrets key to seal the token with.
func (s *Server) githubConnect() bool {
	return s.githubReady() && s.Secrets != nil
}

func (s *Server) githubRedirect() string {
	return s.ConsoleOrigin + githubCallPath
}

// handleGitHubStart is api-v1 v1.29 item 2.
func (s *Server) handleGitHubStart(w http.ResponseWriter, r *http.Request) {
	p, id, ok := s.catalogProject(w, r)
	if !ok {
		return
	}
	if err := authz.Authorize(r.Context(), p.User.ID, id, authz.CapMembers); err != nil {
		s.writeAuth(w, err)
		return
	}
	if !requireConsole(w, p) {
		return
	}
	if !s.githubReady() {
		writeCode(w, "unavailable", msgGitHubOff)
		return
	}
	if s.Secrets == nil {
		s.logger().Error("secrets key not configured")
		writeCode(w, "unavailable", errSecretsKey.Error())
		return
	}
	// api-v1 v1.31 item 5: an optional connect request rides in the state.
	requestID, ok := s.startConnectRequest(w, r, p, id)
	if !ok {
		return
	}
	state, verifier, err := s.GitHubStates.PutRequest(p.User.ID, id, requestID, s.now())
	if errors.Is(err, github.ErrFull) {
		writeCode(w, "unavailable", "unavailable")
		return
	}
	if err != nil {
		s.logger().Error("github state failed")
		writeCode(w, "error", "internal error")
		return
	}
	writeOK(w, http.StatusOK, map[string]any{"authorize_url": s.GitHub.AuthorizeURL(state, verifier, s.githubRedirect())})
}

// handleGitHubCallback is api-v1 v1.29 item 3. The token is sealed and
// stored; it is never written to a response, a log line or an audit row.
func (s *Server) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r)
	if !ok {
		return
	}
	if !requireConsole(w, p) {
		return
	}
	if !s.githubReady() {
		writeCode(w, "unavailable", msgGitHubOff)
		return
	}
	var body struct {
		Code  string `json:"code"`
		State string `json:"state"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	// The cheap checks run before the state is spent, so a retry can use it.
	if body.Code == "" || len(body.Code) > maxGitHubCode {
		writeCode(w, "bad_request", "invalid code")
		return
	}
	if s.Secrets == nil {
		s.logger().Error("secrets key not configured")
		writeCode(w, "unavailable", errSecretsKey.Error())
		return
	}
	taken, err := s.GitHubStates.TakeState(body.State, p.User.ID, s.now())
	if err != nil {
		writeCode(w, "bad_request", msgGitHubState)
		return
	}
	projectID, verifier := taken.Project, taken.Verifier
	if err := authz.Authorize(r.Context(), p.User.ID, projectID, authz.CapMembers); err != nil {
		var denied *authz.Denied
		if errors.As(err, &denied) {
			writeCode(w, "forbidden", "forbidden")
			return
		}
		s.fail(w, err)
		return
	}
	token, err := s.GitHub.Exchange(r.Context(), body.Code, verifier, s.githubRedirect())
	if err != nil {
		s.logger().Warn("github sign-in failed", "step", "exchange")
		writeCode(w, "bad_gateway", msgGitHubFailed)
		return
	}
	login, scopes, err := s.GitHub.User(r.Context(), token)
	if err != nil {
		s.logger().Warn("github sign-in failed", "step", "user")
		s.revokeGitHub(r.Context(), token)
		writeCode(w, "bad_gateway", msgGitHubFailed)
		return
	}
	if !github.HasScope(scopes) {
		s.revokeGitHub(r.Context(), token)
		writeCode(w, "bad_request", msgGitHubScope)
		return
	}
	host := store.GitHubRegistry
	nonce, ciphertext, err := s.Secrets.Seal(seal.RegistryAAD(projectID.String(), host), []byte(token))
	if err != nil {
		s.logger().Error("encrypt failed")
		writeCode(w, "error", "internal error")
		return
	}
	// A replaced GitHub token is revoked after the write (item 3).
	oldToken := s.githubTokenOf(r.Context(), projectID, host)
	actor := p.User.ID
	write := store.RegistryWrite{
		Username:    login,
		Nonce:       nonce,
		Ciphertext:  ciphertext,
		KeyVersion:  seal.KeyVersion,
		Source:      store.RegistrySourceGitHub,
		ConnectedBy: &actor,
	}
	var reg store.Registry
	if taken.Request == "" {
		reg, err = s.Store.PutRegistry(r.Context(), projectID, host, write, p.User.ID, s.now())
	} else {
		// api-v1 v1.31 item 6: the login and the request's move to
		// connected in one transaction. A request that is no longer
		// pending is left as it is; the connect still applies.
		reg, err = s.Store.ConnectRegistry(r.Context(), projectID, host, write, p.User.ID, s.now(), secret.Hash(taken.Request))
	}
	if err != nil {
		// Nothing stores the new token: revoke it so it does not linger at GitHub.
		s.revokeGitHub(r.Context(), token)
		s.writeErr(w, err, false)
		return
	}
	if oldToken != "" && oldToken != token {
		s.revokeGitHub(r.Context(), oldToken)
	}
	detail := map[string]any{"host": host, "login": login, "via": viaKind(p.Session.Kind)}
	out := map[string]any{
		"project_id": projectID,
		"host":       reg.Host,
		"username":   reg.Username,
		"source":     reg.Source,
	}
	if taken.Request != "" {
		detail["request"] = auditRequestRef(secret.Hash(taken.Request))
		out["connect_request_id"] = taken.Request
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(projectID),
		Action:    audit.ActionRegistryGitHubConnected,
		Detail:    detail,
	})
	writeOK(w, http.StatusOK, out)
}

// githubTokenOf is the stored token of a github-source row for host, or ""
// (no row, a manual row, or a token that does not open).
func (s *Server) githubTokenOf(ctx context.Context, projectID uuid.UUID, host string) string {
	mat, err := s.Store.GetRegistryMaterial(ctx, projectID, host)
	if err != nil || mat.Source != store.RegistrySourceGitHub {
		return ""
	}
	token, err := s.openRegistry(projectID, mat)
	if err != nil {
		return ""
	}
	return token
}

// revokeGitHub revokes token at GitHub, best effort, and reports whether it
// worked. It never logs the token or a GitHub body.
func (s *Server) revokeGitHub(ctx context.Context, token string) bool {
	if s.GitHub == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), githubRevokeWait)
	defer cancel()
	if err := s.GitHub.Revoke(ctx, token); err != nil {
		s.logger().Warn("github revoke failed")
		return false
	}
	return true
}

// deleteGitHubRegistry is api-v1 v1.29 item 4: delete the github row, then
// revoke its token at GitHub, best effort. mat is the row read before the
// delete. The token is opened before the delete so a missing key still
// deletes the row and reports revoked false.
func (s *Server) deleteGitHubRegistry(w http.ResponseWriter, r *http.Request, p principal, projectID uuid.UUID, mat store.RegistryMaterial) {
	token, openErr := s.openRegistry(projectID, mat)
	if err := s.Store.DeleteRegistry(r.Context(), projectID, mat.Host); err != nil {
		s.writeErr(w, err, false)
		return
	}
	revoked := false
	if openErr == nil {
		revoked = s.revokeGitHub(r.Context(), token)
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(projectID),
		Action:    audit.ActionRegistryGitHubDisconnected,
		Detail:    map[string]any{"host": mat.Host, "revoked": revoked},
	})
	writeOK(w, http.StatusOK, map[string]any{"revoked": revoked})
}
