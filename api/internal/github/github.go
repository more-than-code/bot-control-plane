// Package github is the outbound GitHub OAuth App client behind Connect
// GitHub (api-v1 v1.29 items 1–4): the authorize URL with PKCE S256, the
// code exchange, the token refresh (api-v1 v1.43), GET /user with its
// X-OAuth-Scopes header, and the token revoke. It also holds the
// single-use sign-in states in memory.
//
// No error, log line or return value carries a GitHub response body. The
// tokens are returned only to the caller, which seals them.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// Production endpoints. Tests point Config at a fake.
const (
	DefaultAuthorizeURL = "https://github.com/login/oauth/authorize"
	DefaultTokenURL     = "https://github.com/login/oauth/access_token"
	DefaultAPIURL       = "https://api.github.com"

	// Scope is the only scope Connect GitHub asks for.
	Scope = "read:packages"

	requestTimeout = 10 * time.Second
	bodyMax        = 64 << 10
	userAgent      = "deploybot"
)

// ErrFailed is any failed GitHub call. It never names the cause.
var ErrFailed = errors.New("github request failed")

// ErrRefreshRejected is GitHub's bad_refresh_token: the refresh token is
// expired, revoked or already used, so it must never be sent again.
var ErrRefreshRejected = errors.New("github refresh token rejected")

// refreshPrefix starts every GitHub refresh token.
const refreshPrefix = "ghr_"

var loginRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

// Config is one OAuth App. The secret is never logged.
type Config struct {
	ClientID     string
	ClientSecret string
	// Optional overrides for tests; empty means the production endpoint.
	AuthorizeURL string
	TokenURL     string
	APIURL       string
	HTTP         *http.Client
	// Now is the clock for token expiry times; nil is time.Now.
	Now func() time.Time
}

// Token is one GitHub user token answer (api-v1 v1.43 item 1). Refresh is
// empty and the times are zero when GitHub issued a long-lived token.
type Token struct {
	Access           string
	Refresh          string
	ExpiresAt        time.Time
	RefreshExpiresAt time.Time
}

// Client calls GitHub for one OAuth App.
type Client struct {
	cfg  Config
	http *http.Client
}

// New returns a client. Missing URLs take the production values.
func New(cfg Config) *Client {
	if cfg.AuthorizeURL == "" {
		cfg.AuthorizeURL = DefaultAuthorizeURL
	}
	if cfg.TokenURL == "" {
		cfg.TokenURL = DefaultTokenURL
	}
	if cfg.APIURL == "" {
		cfg.APIURL = DefaultAPIURL
	}
	cfg.APIURL = strings.TrimSuffix(cfg.APIURL, "/")
	hc := cfg.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: requestTimeout}
	}
	// A redirect could carry the token elsewhere; GitHub does not redirect these calls.
	noRedirect := *hc
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{cfg: cfg, http: &noRedirect}
}

// AuthorizeURL is GitHub's authorize URL for state, the S256 challenge of
// verifier, and redirectURI (api-v1 v1.29 item 2).
func (c *Client) AuthorizeURL(state, verifier, redirectURI string) string {
	q := url.Values{}
	q.Set("client_id", c.cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", Scope)
	q.Set("state", state)
	q.Set("code_challenge", oauth2.S256ChallengeFromVerifier(verifier))
	q.Set("code_challenge_method", "S256")
	q.Set("allow_signup", "false")
	return c.cfg.AuthorizeURL + "?" + q.Encode()
}

// Exchange trades code (with the PKCE verifier) for a user token.
func (c *Client) Exchange(ctx context.Context, code, verifier, redirectURI string) (Token, error) {
	form := url.Values{}
	form.Set("client_id", c.cfg.ClientID)
	form.Set("client_secret", c.cfg.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)
	tok, err := c.tokenCall(ctx, form)
	if err != nil {
		// bad_refresh_token has no meaning for a code exchange.
		return Token{}, ErrFailed
	}
	return tok, nil
}

// Refresh trades a refresh token for a new token (api-v1 v1.43 item 1).
// GitHub rotates: the old refresh and access tokens stop working. A
// bad_refresh_token answer is ErrRefreshRejected; anything else that fails
// is ErrFailed.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	form := url.Values{}
	form.Set("client_id", c.cfg.ClientID)
	form.Set("client_secret", c.cfg.ClientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	return c.tokenCall(ctx, form)
}

// tokenCall posts form to the token endpoint and reads the answer:
// ErrRefreshRejected for GitHub's bad_refresh_token, ErrFailed for any
// other failure.
func (c *Client) tokenCall(ctx context.Context, form url.Values) (Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, ErrFailed
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	body, _, status, err := c.do(req)
	if err != nil {
		return Token{}, ErrFailed
	}
	var raw struct {
		AccessToken           string `json:"access_token"`
		RefreshToken          string `json:"refresh_token"`
		ExpiresIn             int64  `json:"expires_in"`
		RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"`
		Error                 string `json:"error"`
	}
	if json.Unmarshal(body, &raw) != nil {
		return Token{}, ErrFailed
	}
	if raw.Error == "bad_refresh_token" {
		return Token{}, ErrRefreshRejected
	}
	if status != http.StatusOK || raw.Error != "" || !tokenOK(raw.AccessToken) {
		return Token{}, ErrFailed
	}
	if raw.RefreshToken != "" && (!strings.HasPrefix(raw.RefreshToken, refreshPrefix) || !tokenOK(raw.RefreshToken)) {
		return Token{}, ErrFailed
	}
	// A year in seconds is far beyond GitHub's 8 h and 6 months; past it
	// the answer is not trusted (and could overflow a Duration).
	const maxLife = 366 * 24 * 3600
	if raw.ExpiresIn < 0 || raw.RefreshTokenExpiresIn < 0 || raw.ExpiresIn > maxLife || raw.RefreshTokenExpiresIn > maxLife {
		return Token{}, ErrFailed
	}
	now := c.now()
	tok := Token{Access: raw.AccessToken, Refresh: raw.RefreshToken}
	if raw.ExpiresIn > 0 {
		tok.ExpiresAt = now.Add(time.Duration(raw.ExpiresIn) * time.Second)
	}
	if raw.RefreshTokenExpiresIn > 0 {
		tok.RefreshExpiresAt = now.Add(time.Duration(raw.RefreshTokenExpiresIn) * time.Second)
	}
	return tok, nil
}

func (c *Client) now() time.Time {
	if c.cfg.Now != nil {
		return c.cfg.Now().UTC()
	}
	return time.Now().UTC()
}

// User reads GET /user: the login and the granted scopes from X-OAuth-Scopes.
func (c *Client) User(ctx context.Context, token string) (string, []string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIURL+"/user", nil)
	if err != nil {
		return "", nil, ErrFailed
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
	body, header, status, err := c.do(req)
	if err != nil || status != http.StatusOK {
		return "", nil, ErrFailed
	}
	var u struct {
		Login string `json:"login"`
	}
	if json.Unmarshal(body, &u) != nil || !loginRe.MatchString(u.Login) {
		return "", nil, ErrFailed
	}
	var scopes []string
	for _, part := range strings.Split(header.Get("X-OAuth-Scopes"), ",") {
		if part = strings.TrimSpace(part); part != "" {
			scopes = append(scopes, part)
		}
	}
	return u.Login, scopes, nil
}

// HasScope reports whether scopes holds Scope.
func HasScope(scopes []string) bool {
	for _, s := range scopes {
		if s == Scope {
			return true
		}
	}
	return false
}

// Revoke deletes token at GitHub (DELETE /applications/{client_id}/token,
// basic auth with the client id and secret). Only 204 counts as revoked
// (api-v1 v1.29 item 4).
func (c *Client) Revoke(ctx context.Context, token string) error {
	raw, err := json.Marshal(map[string]string{"access_token": token})
	if err != nil {
		return ErrFailed
	}
	u := c.cfg.APIURL + "/applications/" + url.PathEscape(c.cfg.ClientID) + "/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, bytes.NewReader(raw))
	if err != nil {
		return ErrFailed
	}
	req.SetBasicAuth(c.cfg.ClientID, c.cfg.ClientSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
	_, _, status, err := c.do(req)
	if err != nil || status != http.StatusNoContent {
		return ErrFailed
	}
	return nil
}

// do sends req within the request budget and reads at most bodyMax bytes.
func (c *Client) do(req *http.Request) ([]byte, http.Header, int, error) {
	ctx, cancel := context.WithTimeout(req.Context(), requestTimeout)
	defer cancel()
	res, err := c.http.Do(req.WithContext(ctx))
	if err != nil {
		return nil, nil, 0, ErrFailed
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, bodyMax+1))
	if err != nil || len(body) > bodyMax {
		return nil, nil, 0, ErrFailed
	}
	return body, res.Header, res.StatusCode, nil
}

// tokenOK is a sanity check on a returned token: printable, no spaces,
// and short enough for the registry token column.
func tokenOK(t string) bool {
	if t == "" || len(t) > 1024 {
		return false
	}
	for _, r := range t {
		if r <= ' ' || r > '~' {
			return false
		}
	}
	return true
}
