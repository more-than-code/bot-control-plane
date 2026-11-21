package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/authz"
	"github.com/more-than-code/bot-control-plane/api/internal/github"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Connect GitHub error texts (api-v1 v1.29 items 1–3).
const (
	msgGitHubOff    = "GitHub is not configured"
	msgGitHubState  = "this GitHub sign-in expired or was already used; start again"
	msgGitHubScope  = "GitHub did not grant read:packages"
	msgGitHubFailed = "GitHub sign-in failed; try again"
	// msgRegistryChanged is a disconnect whose login changed meanwhile.
	msgRegistryChanged = "The registry login changed; reload and try again"
	githubCallPath     = "/auth/github/callback"
	githubRevokeWait   = 10 * time.Second
	maxGitHubCode      = 512
)

// githubConnectLockWait bounds the callback's wait for a running refresh of
// the same login (a variable so tests can shorten it).
var githubConnectLockWait = 30 * time.Second

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
// Every failure logs one line with its step and is recorded on the connect
// request it belongs to (api-v1 v1.39 items 1 and 2).
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
		// ConnectRequestID is the console's connect cookie (api-v1 v1.39
		// item 1): the request a failed state belonged to.
		ConnectRequestID string `json:"connect_request_id"`
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
		s.githubFailed(r.Context(), p, body.ConnectRequestID, "state", github.ReasonExpiredSignIn)
		writeCode(w, "bad_request", msgGitHubState)
		return
	}
	// From here the state names the request (or none, for a start from Settings).
	failed := func(step, reason string) {
		s.githubFailed(r.Context(), p, taken.Request, step, reason)
	}
	projectID, verifier := taken.Project, taken.Verifier
	if err := authz.Authorize(r.Context(), p.User.ID, projectID, authz.CapMembers); err != nil {
		var denied *authz.Denied
		if errors.As(err, &denied) {
			failed("forbidden", github.ReasonForbidden)
			writeCode(w, "forbidden", "forbidden")
			return
		}
		// Not a callback failure: authz could not be checked (a store error),
		// which s.fail logs and answers as any internal error.
		s.fail(w, err)
		return
	}
	tok, err := s.GitHub.Exchange(r.Context(), body.Code, verifier, s.githubRedirect())
	token := tok.Access
	if err != nil {
		failed("exchange", github.ReasonGitHubError)
		writeCode(w, "bad_gateway", msgGitHubFailed)
		return
	}
	login, scopes, err := s.GitHub.User(r.Context(), token)
	if err != nil {
		failed("user", github.ReasonGitHubError)
		s.revokeGitHub(r.Context(), token, false)
		writeCode(w, "bad_gateway", msgGitHubFailed)
		return
	}
	if !github.HasScope(scopes) {
		failed("scope", github.ReasonScopeMissing)
		s.revokeGitHub(r.Context(), token, false)
		writeCode(w, "bad_request", msgGitHubScope)
		return
	}
	host := store.GitHubRegistry
	nonce, ciphertext, err := s.Secrets.Seal(seal.RegistryAAD(projectID.String(), host), []byte(token))
	if err != nil {
		failed("store", github.ReasonSaveFailed)
		writeCode(w, "error", "internal error")
		return
	}
	// api-v1 v1.43 item 1: the refresh token and both expiry times go in
	// the same row write; a long-lived token stores none.
	refresh, err := s.sealRefreshMaterial(projectID, host, tok)
	if err != nil {
		failed("store", github.ReasonSaveFailed)
		s.revokeGitHub(r.Context(), token, false)
		writeCode(w, "error", "internal error")
		return
	}
	// The replace runs under the login's refresh lock (spec §10.4): a
	// refresh rotating the old pair meanwhile cannot slip between the read
	// of the old token and the write, so the token revoked is the live one.
	// The wait has its own bound; the OAuth state is already spent, so a
	// timeout asks the person to start again.
	lctx, lcancel := context.WithTimeout(r.Context(), githubConnectLockWait)
	st, unlock, err := s.refresh.lock(lctx, registrySubject(projectID, host), s.now)
	lcancel()
	if err != nil {
		failed("store", github.ReasonSaveFailed)
		s.revokeGitHub(r.Context(), token, false)
		writeCode(w, "bad_gateway", msgGitHubFailed)
		return
	}
	// A replaced GitHub token is revoked after the write and the unlock
	// (item 3); so is a pending pair for a row that changed.
	oldToken, oldExpired, stale := s.githubTokenOf(r.Context(), st, projectID, host)
	actor := p.User.ID
	write := store.RegistryWrite{
		Username:                login,
		Nonce:                   nonce,
		Ciphertext:              ciphertext,
		KeyVersion:              seal.KeyVersion,
		Source:                  store.RegistrySourceGitHub,
		ConnectedBy:             &actor,
		RegistryRefreshMaterial: refresh,
	}
	var reg store.Registry
	if taken.Request == "" {
		reg, err = s.Store.PutRegistry(r.Context(), projectID, host, write, p.User.ID, s.now())
	} else {
		// api-v1 v1.31 item 6: the login and the request's move to
		// connected in one transaction. A request that is no longer
		// pending is left as it is; the connect still applies.
		reg, err = s.Store.ConnectRegistry(r.Context(), projectID, host, write, p.User.ID, s.now(), takenRequestHash(taken.Request))
	}
	if err != nil {
		unlock()
		failed("store", github.ReasonSaveFailed)
		// Nothing stores the new token: revoke it so it does not linger at GitHub.
		s.revokeGitHub(r.Context(), token, false)
		s.writeErr(w, err, false)
		return
	}
	// The old row's pending pair (if any) went with it.
	s.refresh.setPending(st, nil)
	unlock()
	if oldToken != "" && oldToken != token {
		s.revokeGitHub(r.Context(), oldToken, oldExpired)
	}
	if stale != "" && stale != oldToken && stale != token {
		s.revokeGitHub(r.Context(), stale, false)
	}
	s.registryLoginChanged(r.Context(), projectID, host, false)
	detail := map[string]any{"host": host, "login": login, "via": viaKind(p.Session.Kind)}
	out := map[string]any{
		"project_id": projectID,
		"host":       reg.Host,
		"username":   reg.Username,
		"source":     reg.Source,
	}
	if taken.Request != "" {
		// v1.39 item 2: a success clears the last failure.
		s.GitHubFailures.Clear(takenRequestHash(taken.Request))
		detail["request"] = auditRequestRef(takenRequestHash(taken.Request))
		out["connect_request_id"] = taken.Request
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(projectID),
		Action:    audit.ActionRegistryGitHubConnected,
		Detail:    detail,
	})
	writeOK(w, http.StatusOK, out)
}

// githubFailed is a callback failure (api-v1 v1.39 items 1 and 2): one Warn
// line with step, never a token, code, state or GitHub body, and reason
// recorded on the connect request requestID names when that request is the
// caller's and still pending. Any other id, malformed or not, is ignored.
func (s *Server) githubFailed(ctx context.Context, p principal, requestID, step, reason string) {
	s.logger().Warn("github sign-in failed", "step", step)
	if requestID == "" {
		return
	}
	hash, ok := requestHash(requestID, true)
	if !ok {
		return
	}
	// WithoutCancel: the failure is still recorded when the browser has gone
	// away, so the connect page shows it on the next load.
	req, err := s.Store.GetConnectRequest(context.WithoutCancel(ctx), hash)
	now := s.now()
	if err != nil || req.UserID != p.User.ID || req.StateAt(now) != store.RequestPending {
		return
	}
	s.GitHubFailures.Record(hash, reason, now, req.ExpiresAt)
}

// githubTokenOf is the access token GitHub honours for the github-source
// row of host, or "" (no row, a manual row, or a token that does not open),
// and whether that token is known to have expired. The caller holds the
// login's refresh lock st: a pending pair for this row (§10.2) replaced the
// stored token, so its access token is the live one. It never refreshes:
// the replaced token is only revoked.
//
// stale is the access token of a pending pair that no longer matches the
// row (it replaced a row that has changed since): nobody holds it, so the
// caller revokes it too.
func (s *Server) githubTokenOf(ctx context.Context, st *loginRefresh, projectID uuid.UUID, host string) (token string, expired bool, stale string) {
	mat, err := s.Store.GetRegistryMaterial(ctx, projectID, host)
	if err != nil {
		return "", false, staleOf(st, store.RegistryMaterial{})
	}
	return s.liveGitHubToken(st, projectID, mat)
}

// liveGitHubToken is githubTokenOf for a row already read. A manual row
// gives no token: a personal access token is never sent to GitHub.
func (s *Server) liveGitHubToken(st *loginRefresh, projectID uuid.UUID, mat store.RegistryMaterial) (token string, expired bool, stale string) {
	stale = staleOf(st, mat)
	if mat.Source != store.RegistrySourceGitHub {
		return "", false, stale
	}
	if p := st.pending; p != nil && bytes.Equal(p.oldNonce, mat.RefreshNonce) {
		return p.access, false, ""
	}
	token, err := s.openRegistry(projectID, mat)
	if err != nil {
		return "", false, stale
	}
	return token, s.accessExpired(mat), stale
}

// staleOf is the access token of st's pending pair when it does not belong
// to the github row mat, else "".
func staleOf(st *loginRefresh, mat store.RegistryMaterial) string {
	p := st.pending
	if p == nil || (mat.Source == store.RegistrySourceGitHub && bytes.Equal(p.oldNonce, mat.RefreshNonce)) {
		return ""
	}
	return p.access
}

// revokeGitHub revokes token at GitHub, best effort, and reports whether it
// worked. It never logs the token or a GitHub body. A token already past
// its expiry cannot be revoked; that failure is an Info line with
// reason=expired (api-v1 v1.43), any other a Warn.
func (s *Server) revokeGitHub(ctx context.Context, token string, expired bool) bool {
	if s.GitHub == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), githubRevokeWait)
	defer cancel()
	if err := s.GitHub.Revoke(ctx, token); err != nil {
		if expired {
			s.logger().Info("github revoke failed", "reason", "expired")
		} else {
			s.logger().Warn("github revoke failed")
		}
		return false
	}
	return true
}

// deleteGitHubRegistry is api-v1 v1.29 item 4: delete the github row (its
// refresh material goes with it), then revoke its access token at GitHub,
// best effort. It runs under the login's refresh lock (spec §10.4), reading
// the row again there, so a pair rotated meanwhile is the one revoked; a
// pending pair (§10.2) is the live token and is dropped. The token is
// opened before the delete so a missing key still deletes the row and
// reports revoked false.
func (s *Server) deleteGitHubRegistry(w http.ResponseWriter, r *http.Request, p principal, projectID uuid.UUID, mat store.RegistryMaterial) {
	st, unlock, err := s.refresh.lock(r.Context(), registrySubject(projectID, mat.Host), s.now)
	if err != nil {
		s.writeErr(w, err, false)
		return
	}
	seen := mat
	mat, err = s.Store.GetRegistryMaterial(r.Context(), projectID, mat.Host)
	if err != nil {
		unlock()
		s.writeErr(w, err, false)
		return
	}
	// The login changed while this request waited (a manual login, or
	// another GitHub account): a disconnect never removes a login it did
	// not mean to. A refresh of the same login (same account) is fine.
	if mat.Source != store.RegistrySourceGitHub || mat.Username != seen.Username {
		unlock()
		writeCode(w, "conflict", msgRegistryChanged)
		return
	}
	token, expired, stale := s.liveGitHubToken(st, projectID, mat)
	if err := s.Store.DeleteRegistry(r.Context(), projectID, mat.Host); err != nil {
		unlock()
		s.writeErr(w, err, false)
		return
	}
	s.forgetLogin(st)
	unlock()
	s.registryLoginChanged(r.Context(), projectID, mat.Host, true)
	revoked := false
	if token != "" {
		revoked = s.revokeGitHub(r.Context(), token, expired)
	}
	if stale != "" && stale != token {
		s.revokeGitHub(r.Context(), stale, false)
	}
	s.auditOK(r.Context(), p, store.AuditRow{
		ProjectID: idPtr(projectID),
		Action:    audit.ActionRegistryGitHubDisconnected,
		Detail:    map[string]any{"host": mat.Host, "revoked": revoked},
	})
	writeOK(w, http.StatusOK, map[string]any{"revoked": revoked})
}

// takenRequestHash is the stored hash of the connect request the OAuth state
// carries: an id, or a handle a console session gave (api-v1 v1.33 item 8).
func takenRequestHash(id string) []byte {
	hash, _ := requestHash(id, true)
	return hash
}
