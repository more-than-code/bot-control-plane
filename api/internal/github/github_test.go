package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

func TestAuthorizeURL(t *testing.T) {
	c := New(Config{ClientID: "cid", ClientSecret: "sec"})
	raw := c.AuthorizeURL("st", "verifier-verifier-verifier-verifier-verifier", "https://console.example/auth/github/callback")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme+"://"+u.Host+u.Path != DefaultAuthorizeURL {
		t.Fatalf("url %s %v", raw, err)
	}
	q := u.Query()
	want := map[string]string{
		"client_id": "cid", "scope": "read:packages", "state": "st",
		"redirect_uri":          "https://console.example/auth/github/callback",
		"code_challenge_method": "S256",
		"code_challenge":        oauth2.S256ChallengeFromVerifier("verifier-verifier-verifier-verifier-verifier"),
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Fatalf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if strings.Contains(raw, "sec") {
		t.Fatal("client secret in the authorize URL")
	}
}

type fakeGitHub struct {
	srv       *httptest.Server
	tokenCode int
	tokenBody string
	scopes    string
	login     string
	revoke    int
	gotForm   url.Values
	gotAuth   string
	revoked   []string
}

func newFake(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{tokenCode: 200, tokenBody: `{"access_token":"gho_tok","token_type":"bearer","scope":"read:packages"}`, scopes: "read:packages", login: "octo", revoke: 204}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login/oauth/access_token":
			_ = r.ParseForm()
			f.gotForm = r.PostForm
			w.WriteHeader(f.tokenCode)
			_, _ = io.WriteString(w, f.tokenBody)
		case r.URL.Path == "/user":
			f.gotAuth = r.Header.Get("Authorization")
			w.Header().Set("X-OAuth-Scopes", f.scopes)
			_ = json.NewEncoder(w).Encode(map[string]any{"login": f.login})
		case r.Method == http.MethodDelete && r.URL.Path == "/applications/cid/token":
			user, pass, _ := r.BasicAuth()
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if user == "cid" && pass == "sec" {
				f.revoked = append(f.revoked, body["access_token"])
			}
			w.WriteHeader(f.revoke)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) client() *Client {
	return New(Config{ClientID: "cid", ClientSecret: "sec", TokenURL: f.srv.URL + "/login/oauth/access_token", APIURL: f.srv.URL, HTTP: f.srv.Client()})
}

func TestExchangeUserRevoke(t *testing.T) {
	f := newFake(t)
	c := f.client()
	ctx := context.Background()
	full, err := c.Exchange(ctx, "code1", "ver1", "https://c/auth/github/callback")
	if err != nil || full.Access != "gho_tok" || full.Refresh != "" || !full.ExpiresAt.IsZero() || !full.RefreshExpiresAt.IsZero() {
		t.Fatalf("exchange %+v %v", full != (Token{}), err)
	}
	tok := full.Access
	if f.gotForm.Get("code_verifier") != "ver1" || f.gotForm.Get("code") != "code1" || f.gotForm.Get("client_secret") != "sec" {
		t.Fatalf("form %v", f.gotForm)
	}
	login, scopes, err := c.User(ctx, tok)
	if err != nil || login != "octo" || !HasScope(scopes) || f.gotAuth != "Bearer gho_tok" {
		t.Fatalf("user %q %v %v", login, scopes, err)
	}
	f.scopes = "repo, write:packages"
	if _, scopes, _ := c.User(ctx, tok); HasScope(scopes) {
		t.Fatal("scope without read:packages accepted")
	}
	if err := c.Revoke(ctx, tok); err != nil || len(f.revoked) != 1 || f.revoked[0] != "gho_tok" {
		t.Fatalf("revoke %v %v", f.revoked, err)
	}
	// Only 204 is revoked (api-v1 v1.29 item 4).
	for _, code := range []int{404, 401, 500} {
		f.revoke = code
		if err := c.Revoke(ctx, tok); err != ErrFailed {
			t.Fatalf("revoke %d: %v", code, err)
		}
	}
}

// TestExchangeExpiring is acceptance 1 at the client: an expiring answer
// keeps the refresh token and both expiry times from the injected clock.
func TestExchangeExpiring(t *testing.T) {
	f := newFake(t)
	now := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
	c := New(Config{ClientID: "cid", ClientSecret: "sec", TokenURL: f.srv.URL + "/login/oauth/access_token", APIURL: f.srv.URL, HTTP: f.srv.Client(),
		Now: func() time.Time { return now }})
	f.tokenBody = `{"access_token":"ghu_acc","expires_in":28800,"refresh_token":"ghr_ref","refresh_token_expires_in":15897600,"token_type":"bearer","scope":""}`
	tok, err := c.Exchange(context.Background(), "c", "v", "r")
	if err != nil || tok.Access != "ghu_acc" || tok.Refresh != "ghr_ref" ||
		!tok.ExpiresAt.Equal(now.Add(8*time.Hour)) || !tok.RefreshExpiresAt.Equal(now.Add(15897600*time.Second)) {
		t.Fatalf("exchange ok=%v %v", tok.Access == "ghu_acc", err)
	}
	// A refresh token that does not look like one is refused.
	for _, body := range []string{
		`{"access_token":"ghu_acc","expires_in":28800,"refresh_token":"gho_notrefresh"}`,
		`{"access_token":"ghu_acc","expires_in":28800,"refresh_token":"ghr_has space"}`,
		`{"access_token":"ghu_acc","expires_in":-1}`,
		`{"access_token":"ghu_acc","expires_in":99999999999}`,
	} {
		f.tokenBody = body
		if _, err := c.Exchange(context.Background(), "c", "v", "r"); err != ErrFailed {
			t.Fatalf("%s: %v", body, err)
		}
	}
}

// TestRefresh: grant_type refresh_token with the client credentials; a
// bad_refresh_token answer is ErrRefreshRejected; other failures ErrFailed,
// never with a body.
func TestRefresh(t *testing.T) {
	f := newFake(t)
	now := time.Date(2026, 11, 21, 8, 0, 0, 0, time.UTC)
	c := New(Config{ClientID: "cid", ClientSecret: "sec", TokenURL: f.srv.URL + "/login/oauth/access_token", APIURL: f.srv.URL, HTTP: f.srv.Client(),
		Now: func() time.Time { return now }})
	f.tokenBody = `{"access_token":"ghu_new","expires_in":28800,"refresh_token":"ghr_new","refresh_token_expires_in":15897600}`
	tok, err := c.Refresh(context.Background(), "ghr_old")
	if err != nil || tok.Access != "ghu_new" || tok.Refresh != "ghr_new" || !tok.ExpiresAt.Equal(now.Add(8*time.Hour)) {
		t.Fatalf("refresh ok=%v %v", tok.Access == "ghu_new", err)
	}
	if f.gotForm.Get("grant_type") != "refresh_token" || f.gotForm.Get("refresh_token") != "ghr_old" ||
		f.gotForm.Get("client_id") != "cid" || f.gotForm.Get("client_secret") != "sec" || f.gotForm.Get("code") != "" {
		t.Fatalf("form keys %d", len(f.gotForm))
	}
	f.tokenBody = `{"error":"bad_refresh_token","error_description":"SECRET-DETAIL"}`
	if _, err := c.Refresh(context.Background(), "ghr_old"); err != ErrRefreshRejected || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("rejected %v", err)
	}
	for _, tc := range []struct {
		code int
		body string
	}{
		{500, `SECRET-DETAIL`},
		{502, `{"error":"server_error"}`},
		{200, `{"error":"incorrect_client_credentials","error_description":"SECRET-DETAIL"}`},
		{200, `{"access_token":"ghu_new","refresh_token":"nope"}`},
	} {
		f.tokenCode, f.tokenBody = tc.code, tc.body
		if _, err := c.Refresh(context.Background(), "ghr_old"); err != ErrFailed || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("%d %s: %v", tc.code, tc.body, err)
		}
	}
	// A bad_refresh_token code on the Exchange path is a plain failure.
	f.tokenCode, f.tokenBody = 200, `{"error":"bad_refresh_token"}`
	if _, err := c.Exchange(context.Background(), "c", "v", "r"); err != ErrFailed {
		t.Fatalf("exchange %v", err)
	}
}

func TestExchangeErrorsHideBodies(t *testing.T) {
	f := newFake(t)
	c := f.client()
	for _, tc := range []struct {
		code int
		body string
	}{
		{200, `{"error":"bad_verification_code","error_description":"SECRET-DETAIL"}`},
		{500, `SECRET-DETAIL`},
		{200, `{"access_token":"has space"}`},
		{200, `not json SECRET-DETAIL`},
	} {
		f.tokenCode, f.tokenBody = tc.code, tc.body
		_, err := c.Exchange(context.Background(), "c", "v", "r")
		if err != ErrFailed || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("%v", err)
		}
	}
	f.login = "bad login!"
	if _, _, err := c.User(context.Background(), "t"); err != ErrFailed {
		t.Fatalf("bad login %v", err)
	}
}

func TestStates(t *testing.T) {
	s := NewStates()
	user, other, project := uuid.New(), uuid.New(), uuid.New()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	state, verifier, err := s.Put(user, project, now)
	if err != nil || len(state) != 43 || len(verifier) < 43 {
		t.Fatalf("put %q %v", state, err)
	}
	got, v, err := s.Take(state, user, now.Add(time.Minute))
	if err != nil || got != project || v != verifier {
		t.Fatalf("take %v %v", got, err)
	}
	if _, _, err := s.Take(state, user, now); err != ErrState {
		t.Fatal("state used twice")
	}
	state, _, _ = s.Put(user, project, now)
	if _, _, err := s.Take(state, other, now); err != ErrState {
		t.Fatal("other user took the state")
	}
	if _, _, err := s.Take(state, user, now); err != ErrState {
		t.Fatal("a foreign attempt must spend the state")
	}
	state, _, _ = s.Put(user, project, now)
	if _, _, err := s.Take(state, user, now.Add(StateTTL)); err != ErrState {
		t.Fatal("expired state accepted")
	}
	for i := 0; i < stateCap; i++ {
		if _, _, err := s.Put(uuid.New(), project, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.Put(uuid.New(), project, now); err != ErrFull {
		t.Fatalf("cap %v", err)
	}
	// At the cap, expired states are swept at once even inside the minute.
	if _, _, err := s.Put(user, project, now.Add(StateTTL)); err != nil || s.Len() != 1 {
		t.Fatalf("sweep %v %d", err, s.Len())
	}
}

// TestStatesPerUser is api-v1 v1.29 item 2: at most 5 per user, the oldest
// is dropped; other users keep theirs.
func TestStatesPerUser(t *testing.T) {
	s := NewStates()
	user, other, project := uuid.New(), uuid.New(), uuid.New()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	var states []string
	for i := 0; i < PerUser+2; i++ {
		st, _, err := s.Put(user, project, now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		states = append(states, st)
	}
	theirs, _, _ := s.Put(other, project, now)
	if s.Len() != PerUser+1 {
		t.Fatalf("len %d", s.Len())
	}
	for i, st := range states {
		_, _, err := s.Take(st, user, now.Add(time.Minute))
		if (i < 2) != (err == ErrState) {
			t.Fatalf("state %d: %v", i, err)
		}
	}
	if _, _, err := s.Take(theirs, other, now.Add(time.Minute)); err != nil {
		t.Fatalf("other user %v", err)
	}
}

// TestStatesSweepThrottled: expired states are swept at most once a minute.
func TestStatesSweepThrottled(t *testing.T) {
	s := NewStates()
	project := uuid.New()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	s.Put(uuid.New(), project, now)                // first sweep at now
	s.Put(uuid.New(), project, now.Add(-StateTTL)) // already expired, kept until the next sweep
	s.Put(uuid.New(), project, now.Add(30*time.Second))
	if s.Len() != 3 {
		t.Fatalf("swept inside the minute: %d", s.Len())
	}
	s.Put(uuid.New(), project, now.Add(time.Minute))
	if s.Len() != 3 {
		t.Fatalf("after a minute %d, want 3", s.Len())
	}
}
