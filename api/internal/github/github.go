// Package github is the outbound GitHub OAuth App client behind Connect
// GitHub (api-v1 v1.29 items 1–4): the authorize URL with PKCE S256, the
// code exchange, GET /user with its X-OAuth-Scopes header, and the token
// revoke. It also holds the single-use sign-in states in memory.
//
// No error, log line or return value carries a GitHub response body. The
// token is returned only to the caller, which seals it.
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
func (c *Client) Exchange(ctx context.Context, code, verifier, redirectURI string) (string, error) {
	form := url.Values{}
	form.Set("client_id", c.cfg.ClientID)
	form.Set("client_secret", c.cfg.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", ErrFailed
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	body, _, status, err := c.do(req)
	if err != nil || status != http.StatusOK {
		return "", ErrFailed
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if json.Unmarshal(body, &tok) != nil || tok.Error != "" || !tokenOK(tok.AccessToken) {
		return "", ErrFailed
	}
	return tok.AccessToken, nil
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
