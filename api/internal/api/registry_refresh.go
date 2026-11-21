package api

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/more-than-code/bot-control-plane/api/internal/audit"
	"github.com/more-than-code/bot-control-plane/api/internal/github"
	"github.com/more-than-code/bot-control-plane/api/internal/seal"
	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// Connect GitHub logins are refreshed (api-v1 v1.43, spec
// docs/spec-github-token-refresh.md §2.3–§2.4). GitHub's expiring user
// tokens last 8 h; the stored refresh token buys a new pair. A token is
// refreshed when less than the window is left: on use, before a deploy or
// rollback task frame (the agent's pull must not outlive the token), and in
// the hourly upkeep.
const (
	refreshOnUse  = 30 * time.Minute
	refreshOnTask = 75 * time.Minute
	refreshUpkeep = 90 * time.Minute
	// refreshNever opens the stored token as it is (redaction, revokes).
	refreshNever time.Duration = 0
	// githubRefreshWait bounds the row read and the GitHub call of one
	// refresh; refreshStoreWait is the write's own budget (§10.2).
	githubRefreshWait = 15 * time.Second
	refreshStoreWait  = 20 * time.Second
	refreshStoreTries = 3
	// pendingStoreWait is the one try for a pending pair (§10.2), and the
	// budget of the row read that settles an unknown write.
	pendingStoreWait = 3 * time.Second
	// refreshUnavailableBackoff: no GitHub call for a login this long after
	// its refresh failed as unavailable (§10.3).
	refreshUnavailableBackoff = 60 * time.Second
	// githubRefreshEvery is the upkeep period; githubUpkeepDeadline bounds
	// one upkeep run.
	githubRefreshEvery   = time.Hour
	githubUpkeepDeadline = 10 * time.Minute
)

// refreshStoreBackoff is the pause before the second store try (the third
// waits twice as long). Tests shorten it.
var refreshStoreBackoff = 250 * time.Millisecond

// refreshLocks is the in-process single flight per login (project, host)
// and the login's in-memory refresh state (spec §10.2–§10.5). A second
// caller waits (or gives up when its context ends), then re-reads the row
// and finds it refreshed. The store's conditional write guards the row;
// refresh safety holds within one API process (§10.6).
type refreshLocks struct {
	mu   sync.Mutex
	keys map[string]*loginRefresh
}

// loginRefresh is one login's lock and state. users and pending are
// guarded by refreshLocks.mu (pending is also only written while holding
// sem, through setPending); retryAfter and rejectedNonce only while
// holding sem.
type loginRefresh struct {
	// sem is the lock: a one-slot channel, so a wait can select on ctx.
	sem   chan struct{}
	users int
	// pending is a rotated pair GitHub issued that could not be stored
	// (§10.2); it replaces the row whose refresh nonce is pending.oldNonce.
	pending *pendingPair
	// retryAfter: no GitHub call before it, after an unavailable refresh
	// (§10.3).
	retryAfter time.Time
	// rejectedNonce is a refresh token GitHub rejected whose clear failed
	// (§10.5): it is never sent again.
	rejectedNonce []byte
	// sealLogAfter rate-limits the "encrypt failed" line of an unsealed
	// pending pair (it lives only while the pair is pending).
	sealLogAfter time.Time
}

// pendingPair is a refreshed, sealed pair not yet in the store. access is
// the plain access token, kept only to use it and to revoke it if the row
// changed meanwhile; it is never logged.
type pendingPair struct {
	oldNonce []byte
	write    store.RegistryRefresh
	access   string
	// unsealed is set instead of write when sealing failed (the plain
	// pair, sealed on the next try).
	unsealed *github.Token
}

// idle reports whether the state can be dropped.
func (l *loginRefresh) idle(now time.Time) bool {
	return l.pending == nil && l.rejectedNonce == nil && !now.Before(l.retryAfter)
}

// setPending sets k's pending pair (nil clears it).
func (l *refreshLocks) setPending(k *loginRefresh, p *pendingPair) {
	l.mu.Lock()
	k.pending = p
	l.mu.Unlock()
}

// pendingLogins is the logins (project, host) holding a pending pair.
func (l *refreshLocks) pendingLogins() []store.RegistryLogin {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []store.RegistryLogin
	for key, k := range l.keys {
		if k.pending == nil {
			continue
		}
		pid, host, ok := strings.Cut(key, ":")
		id, err := uuid.Parse(pid)
		if ok && err == nil {
			out = append(out, store.RegistryLogin{ProjectID: id, Host: host})
		}
	}
	return out
}

// dropIdle drops unused entries that hold nothing (a backoff that ran out).
func (l *refreshLocks) dropIdle(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, k := range l.keys {
		if k.users == 0 && k.idle(now) {
			delete(l.keys, key)
		}
	}
}

// size is the number of kept entries (tests).
func (l *refreshLocks) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.keys)
}

// hasPending reports whether key holds a pending pair.
func (l *refreshLocks) hasPending(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := l.keys[key]
	return k != nil && k.pending != nil
}

// lock takes key's lock, or returns ctx's error when ctx ends first. The
// release drops a state that holds nothing.
func (l *refreshLocks) lock(ctx context.Context, key string, now func() time.Time) (*loginRefresh, func(), error) {
	l.mu.Lock()
	if l.keys == nil {
		l.keys = map[string]*loginRefresh{}
	}
	k := l.keys[key]
	if k == nil {
		k = &loginRefresh{sem: make(chan struct{}, 1)}
		l.keys[key] = k
	}
	k.users++
	l.mu.Unlock()
	leave := func() {
		l.mu.Lock()
		k.users--
		if k.users == 0 && k.idle(now()) {
			delete(l.keys, key)
		}
		l.mu.Unlock()
	}
	select {
	case k.sem <- struct{}{}:
	case <-ctx.Done():
		leave()
		return nil, nil, ctx.Err()
	}
	return k, func() {
		<-k.sem
		leave()
	}, nil
}

// githubRefreshOn: Connect GitHub is configured, the secrets key is loaded
// and BOT_API_GITHUB_REFRESH is not off (api-v1 v1.43 item 4).
func (s *Server) githubRefreshOn() bool {
	return s.GitHub != nil && s.Secrets != nil && !s.GitHubRefreshOff
}

// refreshDue: mat is a github login with a refresh token whose access
// token has less than within left (or is past). within <= 0 never is.
func (s *Server) refreshDue(mat store.RegistryMaterial, within time.Duration) bool {
	return within > 0 && s.githubRefreshOn() && mat.Source == store.RegistrySourceGitHub &&
		mat.HasRefresh() && mat.AccessExpiresAt != nil && mat.AccessExpiresAt.Sub(s.now()) < within
}

// accessExpired reports whether mat's access token is known to be past.
func (s *Server) accessExpired(mat store.RegistryMaterial) bool {
	return mat.AccessExpiresAt != nil && !mat.AccessExpiresAt.After(s.now())
}

// registryToken is every use of a stored registry token that sends it (the
// deploy and rollback frames, tags and digest, image access): it reads the
// login, refreshes a github token with less than within left, and returns
// the material it used and the opened token. A missing login is
// store.ErrNotFound. A failed refresh still returns the current token, so
// the registry answers as before.
func (s *Server) registryToken(ctx context.Context, projectID uuid.UUID, host string, within time.Duration) (store.RegistryMaterial, string, error) {
	mat, err := s.Store.GetRegistryMaterial(ctx, projectID, host)
	if err != nil {
		return store.RegistryMaterial{}, "", err
	}
	// A pending pair (§10.2) replaces the stored token even when not due:
	// GitHub's rotation ended the stored one.
	if s.refreshDue(mat, within) || (mat.Source == store.RegistrySourceGitHub && s.refresh.hasPending(registrySubject(projectID, host))) {
		var live string
		mat, live, err = s.refreshGitHub(ctx, projectID, host, within)
		if err != nil {
			return store.RegistryMaterial{}, "", err
		}
		if live != "" {
			// A new pair that is not sealed yet (§10.2): its token is used.
			return mat, live, nil
		}
	}
	token, err := s.openRegistry(projectID, mat)
	if err != nil {
		return store.RegistryMaterial{}, "", err
	}
	return mat, token, nil
}

// refreshGitHub refreshes one github login, one at a time per login, and
// returns the material to use and, when the token to use is a new pair not
// sealed yet, that plain access token (else ""). The lock wait respects
// ctx; after that the work runs on context.WithoutCancel: once GitHub has
// answered it has rotated, so the new pair must be kept even when the
// caller has gone. Under the lock it reads the row again, so a caller that
// waited finds the winner's token and does not refresh again. Revokes of
// tokens nobody holds run after the lock is released. It never logs a
// token.
func (s *Server) refreshGitHub(ctx context.Context, projectID uuid.UUID, host string, within time.Duration) (store.RegistryMaterial, string, error) {
	st, unlock, err := s.refresh.lock(ctx, registrySubject(projectID, host), s.now)
	if err != nil {
		return store.RegistryMaterial{}, "", err
	}
	base := context.WithoutCancel(ctx)
	var revokes []string
	revoke := func(token string) { revokes = append(revokes, token) }
	defer func() {
		unlock()
		for _, token := range revokes {
			s.revokeGitHub(base, token, false)
		}
	}()
	rctx, cancel := context.WithTimeout(base, githubRefreshWait)
	defer cancel()
	mat, err := s.Store.GetRegistryMaterial(rctx, projectID, host)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// The login is gone: so is everything kept for it; the entry is
			// dropped when the lock is released.
			if st.pending != nil {
				revoke(st.pending.access)
			}
			s.forgetLogin(st)
		}
		return mat, "", err
	}
	if p := st.pending; p != nil {
		switch {
		case p.unsealed == nil && holdsWrite(mat, p.write):
			// An earlier write committed though it reported an error.
			s.refresh.setPending(st, nil)
			s.logger().Info("github token refreshed", "project_id", projectID, "host", host)
			return mat, "", nil
		case bytes.Equal(p.oldNonce, mat.RefreshNonce) && mat.Source == store.RegistrySourceGitHub:
			// §10.2: a pair GitHub issued earlier is stored before any
			// GitHub call: one short try; else the next use or sweep.
			if p.unsealed != nil {
				return s.refreshUnsealed(rctx, base, st, projectID, host, mat, p, within, revoke)
			}
			out, err := s.storeRefreshed(base, st, projectID, host, mat, p.oldNonce, p.write, p.access, 1, pendingStoreWait, revoke)
			return out, "", err
		default:
			// The row changed (reconnect, manual write): the kept pair is stale.
			revoke(p.access)
			s.refresh.setPending(st, nil)
		}
	}
	if !s.refreshDue(mat, within) {
		return mat, "", nil
	}
	if st.rejectedNonce != nil {
		if bytes.Equal(st.rejectedNonce, mat.RefreshNonce) {
			// §10.5: never resend it; try the clear again.
			s.clearRejected(rctx, st, projectID, host, mat.RefreshNonce)
			return mat, "", nil
		}
		st.rejectedNonce = nil
	}
	if s.now().Before(st.retryAfter) {
		// §10.3: GitHub was unavailable moments ago; use the current token.
		return mat, "", nil
	}
	refresh, err := s.Secrets.Open(seal.RegistryRefreshAAD(projectID.String(), host), mat.RefreshNonce, mat.RefreshCiphertext)
	if err != nil {
		// Before any GitHub call: nothing was spent, so back off and retry.
		s.logger().Error("decrypt failed", "project_id", projectID, "host", host)
		st.retryAfter = s.now().Add(refreshUnavailableBackoff)
		return mat, "", nil
	}
	tok, err := s.GitHub.Refresh(rctx, string(refresh))
	switch {
	case errors.Is(err, github.ErrRefreshRejected):
		s.logger().Warn("github refresh failed", "reason", "rejected", "project_id", projectID, "host", host)
		s.clearRejected(rctx, st, projectID, host, mat.RefreshNonce)
		// Another process may have rotated first; use what is stored now.
		out, err := s.Store.GetRegistryMaterial(rctx, projectID, host)
		return out, "", err
	case err != nil:
		s.logger().Warn("github refresh failed", "reason", "unavailable", "project_id", projectID, "host", host)
		st.retryAfter = s.now().Add(refreshUnavailableBackoff)
		return mat, "", nil
	}
	return s.sealAndStore(base, st, projectID, host, mat, mat.RefreshNonce, tok, refreshStoreTries, refreshStoreWait, revoke)
}

// refreshUnsealed handles a pending pair that could not be sealed (§10.2).
// While its own access token is not due it is sealed and stored. When it is
// due, its refresh token (the one GitHub honours now; the row's was spent)
// buys the next pair, which replaces it. A rejected unsealed refresh token
// ends the login as a rejected refresh does: the pair is dropped and the
// row's spent refresh token is cleared, never sent.
func (s *Server) refreshUnsealed(rctx, base context.Context, st *loginRefresh, projectID uuid.UUID, host string, mat store.RegistryMaterial, p *pendingPair, within time.Duration, revoke func(string)) (store.RegistryMaterial, string, error) {
	tok := *p.unsealed
	due := within > 0 && s.githubRefreshOn() && tok.Refresh != "" && !tok.ExpiresAt.IsZero() && tok.ExpiresAt.Sub(s.now()) < within
	if due && !s.now().Before(st.retryAfter) {
		next, err := s.GitHub.Refresh(rctx, tok.Refresh)
		switch {
		case errors.Is(err, github.ErrRefreshRejected):
			s.logger().Warn("github refresh failed", "reason", "rejected", "project_id", projectID, "host", host)
			s.refresh.setPending(st, nil)
			s.clearRejected(rctx, st, projectID, host, p.oldNonce)
			out, err := s.Store.GetRegistryMaterial(rctx, projectID, host)
			return out, "", err
		case err != nil:
			s.logger().Warn("github refresh failed", "reason", "unavailable", "project_id", projectID, "host", host)
			st.retryAfter = s.now().Add(refreshUnavailableBackoff)
			return mat, tok.Access, nil
		}
		tok = next
	}
	return s.sealAndStore(base, st, projectID, host, mat, p.oldNonce, tok, 1, pendingStoreWait, revoke)
}

// sealAndStore seals a pair GitHub issued and stores it. GitHub has spent
// the old refresh token, so a seal failure must not lose the pair: it is
// kept unsealed in st.pending (sealed and stored by the next use or sweep)
// and its access token is used now; the spent token is never sent again.
func (s *Server) sealAndStore(base context.Context, st *loginRefresh, projectID uuid.UUID, host string, mat store.RegistryMaterial, oldNonce []byte, tok github.Token, tries int, budget time.Duration, revoke func(string)) (store.RegistryMaterial, string, error) {
	write, err := s.sealRefreshed(projectID, host, tok)
	if err != nil {
		// One line per minute while sealing keeps failing.
		if now := s.now(); !now.Before(st.sealLogAfter) {
			s.logger().Error("encrypt failed", "project_id", projectID, "host", host)
			st.sealLogAfter = now.Add(refreshUnavailableBackoff)
		}
		s.refresh.setPending(st, &pendingPair{oldNonce: oldNonce, access: tok.Access, unsealed: &tok})
		return mat, tok.Access, nil
	}
	out, err := s.storeRefreshed(base, st, projectID, host, mat, oldNonce, write, tok.Access, tries, budget, revoke)
	return out, "", err
}

// forgetLogin clears everything kept in memory for a login that is gone.
func (s *Server) forgetLogin(st *loginRefresh) {
	s.refresh.setPending(st, nil)
	st.rejectedNonce = nil
	st.retryAfter = time.Time{}
}

// holdsWrite reports whether the row mat holds the access token of w (its
// nonce is fresh per seal, so equal nonce and ciphertext mean our write).
func holdsWrite(mat store.RegistryMaterial, w store.RegistryRefresh) bool {
	return len(w.Nonce) > 0 && bytes.Equal(mat.Nonce, w.Nonce) && bytes.Equal(mat.Ciphertext, w.Ciphertext)
}

// storeRefreshed writes a refreshed pair over the row holding oldNonce,
// with its own budget and up to tries tries (§10.2: 3 tries over 20 s right
// after GitHub answers, one short try for a pending pair). Stored: the
// written material is returned. A write can commit and still report an
// error (a reset after commit), so before treating the pair as unstored or
// lost the row is read again: when it holds our write, it is stored. Not
// stored because the row holds something else (§10.4): the new access
// token is revoked, best effort, and that row is used. The store failing
// every try: the pair is kept in st.pending and returned, so the caller
// uses the live token, and the next use or sweep stores it.
func (s *Server) storeRefreshed(base context.Context, st *loginRefresh, projectID uuid.UUID, host string, mat store.RegistryMaterial, oldNonce []byte, write store.RegistryRefresh, access string, tries int, budget time.Duration, revoke func(string)) (store.RegistryMaterial, error) {
	ctx, cancel := context.WithTimeout(base, budget)
	defer cancel()
	var stored bool
	var err error
	for try := 0; try < tries; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(refreshStoreBackoff * time.Duration(try)):
			}
		}
		stored, err = s.Store.RefreshRegistry(ctx, projectID, host, oldNonce, write)
		if err == nil || ctx.Err() != nil {
			break
		}
	}
	if stored && err == nil {
		s.refresh.setPending(st, nil)
		s.logger().Info("github token refreshed", "project_id", projectID, "host", host)
		return withRefreshed(mat, write), nil
	}
	// Not stored, or unknown: look at the row.
	rctx, rcancel := context.WithTimeout(base, pendingStoreWait)
	defer rcancel()
	now, rerr := s.Store.GetRegistryMaterial(rctx, projectID, host)
	switch {
	case rerr == nil && holdsWrite(now, write):
		s.refresh.setPending(st, nil)
		s.logger().Info("github token refreshed", "project_id", projectID, "host", host)
		return now, nil
	case rerr != nil && !errors.Is(rerr, store.ErrNotFound),
		rerr == nil && err != nil && bytes.Equal(now.RefreshNonce, oldNonce):
		// Unknown (the row cannot be read; our write may have committed),
		// or the store is failing and the row still waits for this pair:
		// keep it. The next use settles it (holdsWrite) or stores it.
		s.logger().Error("github refresh not stored", "project_id", projectID, "host", host)
		s.refresh.setPending(st, &pendingPair{oldNonce: oldNonce, write: write, access: access})
		return withRefreshed(mat, write), nil
	}
	// The row is gone or holds something else: a reconnect, a manual write
	// or a delete landed first, and the new token is not anyone's login.
	s.refresh.setPending(st, nil)
	revoke(access)
	if errors.Is(rerr, store.ErrNotFound) {
		s.forgetLogin(st)
	}
	return now, rerr
}

// withRefreshed is mat with the refreshed token and material of w.
func withRefreshed(mat store.RegistryMaterial, w store.RegistryRefresh) store.RegistryMaterial {
	mat.Nonce, mat.Ciphertext, mat.KeyVersion = w.Nonce, w.Ciphertext, w.KeyVersion
	mat.RegistryRefreshMaterial = w.RegistryRefreshMaterial
	return mat
}

// clearRejected is GitHub's bad_refresh_token (api-v1 v1.43 item 2): the
// refresh columns are cleared, so the token is never sent again, and the
// clear is audited once. The access token stays; the registry then answers
// login_failed and the registry_login problem opens. A failed clear is
// remembered in st (§10.5) and tried again on the next use.
func (s *Server) clearRejected(ctx context.Context, st *loginRefresh, projectID uuid.UUID, host string, oldNonce []byte) {
	cleared, err := s.Store.ClearRegistryRefresh(ctx, projectID, host, oldNonce)
	if err != nil {
		s.logger().Error("github refresh clear failed", "project_id", projectID, "host", host)
		st.rejectedNonce = append([]byte(nil), oldNonce...)
		return
	}
	st.rejectedNonce = nil
	if !cleared {
		return
	}
	s.Audit.Record(ctx, store.AuditRow{
		ProjectID: idPtr(projectID),
		Via:       audit.ViaSystem,
		Action:    audit.ActionRegistryGitHubRefreshRejected,
		Decision:  audit.DecisionAllowed,
		Detail:    map[string]any{"host": host},
	})
}

// sealRefreshed seals a refreshed token for RefreshRegistry.
func (s *Server) sealRefreshed(projectID uuid.UUID, host string, tok github.Token) (store.RegistryRefresh, error) {
	if s.refreshSealFail != nil {
		if err := s.refreshSealFail(); err != nil {
			return store.RegistryRefresh{}, err
		}
	}
	nonce, ciphertext, err := s.Secrets.Seal(seal.RegistryAAD(projectID.String(), host), []byte(tok.Access))
	if err != nil {
		return store.RegistryRefresh{}, err
	}
	refresh, err := s.sealRefreshMaterial(projectID, host, tok)
	if err != nil {
		return store.RegistryRefresh{}, err
	}
	return store.RegistryRefresh{Nonce: nonce, Ciphertext: ciphertext, KeyVersion: seal.KeyVersion, RegistryRefreshMaterial: refresh}, nil
}

// sealRefreshMaterial is the stored form of tok's refresh token and expiry
// times: empty for a long-lived token (no expiry), and no refresh token
// without one.
func (s *Server) sealRefreshMaterial(projectID uuid.UUID, host string, tok github.Token) (store.RegistryRefreshMaterial, error) {
	var out store.RegistryRefreshMaterial
	if !tok.ExpiresAt.IsZero() {
		at := tok.ExpiresAt.UTC()
		out.AccessExpiresAt = &at
	}
	if tok.Refresh == "" {
		return out, nil
	}
	nonce, ciphertext, err := s.Secrets.Seal(seal.RegistryRefreshAAD(projectID.String(), host), []byte(tok.Refresh))
	if err != nil {
		return store.RegistryRefreshMaterial{}, err
	}
	out.RefreshNonce, out.RefreshCiphertext = nonce, ciphertext
	if !tok.RefreshExpiresAt.IsZero() {
		at := tok.RefreshExpiresAt.UTC()
		out.RefreshExpiresAt = &at
	}
	return out, nil
}

// RunGitHubRefresh is the login upkeep (api-v1 v1.43 item 1): once at start
// and then every hour it refreshes every github login whose access token
// has less than 90 minutes left, so a rarely used login and its 6-month
// refresh token stay alive. It is not a problem detector: it runs with
// BOT_API_PROBLEMS=off, and does nothing when BOT_API_GITHUB_REFRESH=off or
// Connect GitHub is not configured.
func (s *Server) RunGitHubRefresh(ctx context.Context) {
	if !s.githubRefreshOn() {
		return
	}
	s.SweepGitHubRefresh(ctx)
	tick := time.NewTicker(githubRefreshEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.SweepGitHubRefresh(ctx)
		}
	}
}

// SweepGitHubRefresh is one upkeep run, within githubUpkeepDeadline.
func (s *Server) SweepGitHubRefresh(ctx context.Context) {
	if !s.githubRefreshOn() {
		return
	}
	s.refresh.dropIdle(s.now())
	ctx, cancel := context.WithTimeout(ctx, githubUpkeepDeadline)
	defer cancel()
	due, err := s.Store.ListRegistryRefreshDue(ctx, s.now().Add(refreshUpkeep))
	if err != nil {
		s.logger().Error("github refresh sweep failed")
		return
	}
	// Pairs that could not be stored are stored now (§10.2).
	due = append(due, s.refresh.pendingLogins()...)
	for _, l := range due {
		if ctx.Err() != nil {
			return
		}
		if _, _, err := s.refreshGitHub(ctx, l.ProjectID, l.Host, refreshUpkeep); err != nil && !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
			s.logger().Error("github refresh sweep failed", "project_id", l.ProjectID, "host", l.Host)
		}
	}
}
