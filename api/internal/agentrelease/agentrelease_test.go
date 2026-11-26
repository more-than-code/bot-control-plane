package agentrelease

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fake is a GitHub with one latest release.
type fake struct {
	tag      string
	sums     string
	sig      string
	status   int    // for the latest call; 0 = 200
	bigBody  bool   // latest body over the limit
	redirect string // the latest call redirects here
	slow     time.Duration
}

func (f *fake) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.slow > 0 {
			time.Sleep(f.slow)
		}
		switch {
		case r.URL.Path == "/latest":
			if r.Header.Get("User-Agent") != "deploybot" {
				t.Errorf("User-Agent %q", r.Header.Get("User-Agent"))
			}
			if f.redirect != "" {
				http.Redirect(w, r, f.redirect, http.StatusFound)
				return
			}
			if f.status != 0 {
				w.WriteHeader(f.status)
				return
			}
			if f.bigBody {
				_, _ = w.Write([]byte(`{"tag_name":"` + f.tag + `","body":"` + strings.Repeat("x", maxRelease) + `"}`))
				return
			}
			_, _ = w.Write([]byte(`{"tag_name":"` + f.tag + `","draft":false}`))
		case r.URL.Path == "/download/"+f.tag+"/SHA256SUMS":
			_, _ = w.Write([]byte(f.sums))
		case r.URL.Path == "/download/"+f.tag+"/SHA256SUMS.sig":
			_, _ = w.Write([]byte(f.sig))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sumsFor(version string) string {
	h := strings.Repeat("ab", 32)
	return h + "  bot-agent-runner-v" + version + "-linux-amd64\n" +
		h + "  bot-agent-v" + version + "-linux-amd64\n" +
		h + "  bot-agent-v" + version + "-linux-arm64\n" +
		h + "  install.sh\n"
}

func sign(priv ed25519.PrivateKey, sums string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(sums))) + "\n"
}

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func checker(srv *httptest.Server, keys ...ed25519.PublicKey) *Checker {
	c := New()
	c.LatestURL = srv.URL + "/latest"
	c.DownloadBase = srv.URL + "/download/"
	c.Keys = keys
	return c
}

func TestCheckGood(t *testing.T) {
	pub, priv := newKey(t)
	other, _ := newKey(t)
	sums := sumsFor("0.2.19")
	f := &fake{tag: "v0.2.19", sums: sums, sig: sign(priv, sums)}
	srv := f.server(t)
	got, err := checker(srv, other, pub).Check(context.Background())
	if err != nil || got != "0.2.19" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestCheckRefuses(t *testing.T) {
	pub, priv := newKey(t)
	_, otherPriv := newKey(t)
	good := sumsFor("0.2.19")
	old := sumsFor("0.2.18")
	cases := []struct {
		name string
		f    fake
		want error
	}{
		{"bad signature", fake{tag: "v0.2.19", sums: good, sig: "AAAA\n"}, ErrBadSignature},
		{"untrusted key", fake{tag: "v0.2.19", sums: good, sig: sign(otherPriv, good)}, ErrBadSignature},
		{"sums changed after signing", fake{tag: "v0.2.19", sums: good + "x\n", sig: sign(priv, good)}, ErrBadSignature},
		{"replayed sums of an older release", fake{tag: "v0.2.19", sums: old, sig: sign(priv, old)}, ErrBadRelease},
		{"tag not vX.Y.Z", fake{tag: "v0.2.19-rc1", sums: good, sig: sign(priv, good)}, ErrBadRelease},
		{"tag without v", fake{tag: "0.2.19", sums: good, sig: sign(priv, good)}, ErrBadRelease},
		{"tag part too long", fake{tag: "v0.2.12345", sums: good, sig: sign(priv, good)}, ErrBadRelease},
		{"no release", fake{tag: "v0.2.19", status: http.StatusNotFound}, ErrUnavailable},
		{"server error", fake{tag: "v0.2.19", status: http.StatusInternalServerError}, ErrUnavailable},
		{"body over the limit", fake{tag: "v0.2.19", bigBody: true}, ErrUnavailable},
		{"redirect to another host", fake{tag: "v0.2.19", redirect: "https://example.com/latest"}, ErrUnavailable},
		{"sig missing", fake{tag: "v0.2.19", sums: good}, ErrBadSignature},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.f
			srv := f.server(t)
			got, err := checker(srv, pub).Check(context.Background())
			if !errors.Is(err, tc.want) || got != "" {
				t.Fatalf("got %q %v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestCheckRedirectToTestServerRefused(t *testing.T) {
	// A plain-http redirect, even to the same server, is not an allowed host.
	pub, priv := newKey(t)
	sums := sumsFor("0.2.19")
	f := &fake{tag: "v0.2.19", sums: sums, sig: sign(priv, sums)}
	srv := f.server(t)
	f.redirect = srv.URL + "/latest2"
	if _, err := checker(srv, pub).Check(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err %v", err)
	}
}

func TestCheckTimeout(t *testing.T) {
	pub, priv := newKey(t)
	sums := sumsFor("0.2.19")
	f := &fake{tag: "v0.2.19", sums: sums, sig: sign(priv, sums), slow: 200 * time.Millisecond}
	srv := f.server(t)
	c := checker(srv, pub)
	c.HTTP.Timeout = 50 * time.Millisecond
	if _, err := c.Check(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err %v", err)
	}
}

func TestVerifyNoKeysOrPlaceholder(t *testing.T) {
	_, priv := newKey(t)
	sums := []byte(sumsFor("0.2.19"))
	sig := []byte(sign(priv, string(sums)))
	if Verify(nil, sums, sig) == nil {
		t.Fatal("no keys verified")
	}
	if Verify([]ed25519.PublicKey{make([]byte, ed25519.PublicKeySize)}, sums, sig) == nil {
		t.Fatal("placeholder verified")
	}
	// As the helper: a placeholder or a bad key anywhere refuses the list.
	pub := priv.Public().(ed25519.PublicKey)
	if Verify([]ed25519.PublicKey{make([]byte, ed25519.PublicKeySize), pub}, sums, sig) == nil {
		t.Fatal("list with a placeholder verified")
	}
	if Verify([]ed25519.PublicKey{pub[:31], pub}, sums, sig) == nil {
		t.Fatal("list with a short key verified")
	}
	if Verify([]ed25519.PublicKey{priv.Public().(ed25519.PublicKey)}, sums, []byte(strings.TrimSuffix(string(sig), "\n")+"\r\n")) != nil {
		t.Fatal("CRLF sig refused")
	}
}

func TestNamesVersion(t *testing.T) {
	sums := []byte(sumsFor("0.2.19"))
	if !NamesVersion(sums, "0.2.19") {
		t.Fatal("own version")
	}
	for _, v := range []string{"0.2.1", "0.2.190", "0.2.18"} {
		if NamesVersion(sums, v) {
			t.Fatalf("named %s", v)
		}
	}
	noArm := strings.ReplaceAll(string(sums), "bot-agent-v0.2.19-linux-arm64", "other")
	if NamesVersion([]byte(noArm), "0.2.19") {
		t.Fatal("named without the arm64 binary")
	}
}

// TestPublicKeysMatchBotAgent pins the copy of bot-agent
// internal/release.PublicKeys (spec §9 item 7).
func TestPublicKeysMatchBotAgent(t *testing.T) {
	want := []string{
		"Ee1UIjnw5zgPNw5ZtKNBTCdLvYM3ozEcSRcfWZaCHh8=",
		"DCG1ElztSCaavg19pGaC2Hud1CoLZXvIDrlFr0RXBPA=",
	}
	if len(PublicKeys) != len(want) {
		t.Fatalf("%d keys", len(PublicKeys))
	}
	for i, k := range PublicKeys {
		if got := base64.StdEncoding.EncodeToString(k); got != want[i] {
			t.Fatalf("key %d is %s", i, got)
		}
	}
}

func TestCheckErrorsCarryNoBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("MARKER-secret-body"))
	}))
	defer srv.Close()
	_, err := checker(srv).Check(context.Background())
	if err == nil || strings.Contains(err.Error(), "MARKER") {
		t.Fatalf("err %v", err)
	}
}

func TestCheckOversizeAndMissingAssets(t *testing.T) {
	pub, priv := newKey(t)
	good := sumsFor("0.2.19")
	big := good + strings.Repeat("#", maxSums)
	cases := map[string]fake{
		"sums over 4 KiB": {tag: "v0.2.19", sums: big, sig: sign(priv, big)},
		"sig over 128 B":  {tag: "v0.2.19", sums: good, sig: strings.Repeat("A", maxSig+1)},
		"sums missing":    {tag: "v0.2.19", sums: "", sig: sign(priv, good)},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			f := f
			srv := f.server(t)
			if name == "sums missing" {
				// The fake answers 200 with an empty body; make it a 404.
				c := checker(srv, pub)
				c.DownloadBase = srv.URL + "/nowhere/"
				if _, err := c.Check(context.Background()); !errors.Is(err, ErrUnavailable) {
					t.Fatalf("err %v", err)
				}
				return
			}
			if _, err := checker(srv, pub).Check(context.Background()); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func TestAllowedRedirects(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://objects.githubusercontent.com/x":        true,
		"https://release-assets.githubusercontent.com/x": true,
		"https://github.com/x":                           true,
		"https://api.github.com/x":                       true,
		"http://github.com/x":                            false,
		"https://github.com:8443/x":                      false,
		"https://user@github.com/x":                      false,
		"https://example.com/x":                          false,
		"https://github.com.example.com/x":               false,
	} {
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		if got := checkRedirect(req, nil) == nil; got != want {
			t.Fatalf("%s: %v", raw, got)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "https://github.com/x", nil)
	if checkRedirect(req, make([]*http.Request, maxRedirects)) == nil {
		t.Fatal("redirect cap")
	}
}
