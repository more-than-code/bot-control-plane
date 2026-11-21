package registry_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/more-than-code/bot-control-plane/api/internal/registry"
	"github.com/more-than-code/bot-control-plane/api/internal/registry/registrytest"
)

// refusalRegistry answers reg.test behind a Bearer challenge whose realm is
// https://reg.test/token. realm and registry answer the token and the
// manifest or tags request after the challenge.
func refusalRegistry(f *registrytest.Fake, realm, registry http.HandlerFunc) {
	f.Handle(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			realm(w, r)
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="https://reg.test/token",service="reg.test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		registry(w, r)
	})
}

func tokenOK(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"token":"tok-123"}`) }

func answer(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}
}

// padded is a valid DENIED error body of exactly n bytes.
func padded(n int) string {
	const head = `{"errors":[{"code":"DENIED"}]}`
	return head + strings.Repeat(" ", n-len(head))
}

func refusal(t *testing.T, err error) *registry.Error {
	t.Helper()
	var re *registry.Error
	if !errors.As(err, &re) {
		t.Fatalf("error %v is not a registry.Error", err)
	}
	return re
}

var stored = &registry.Credential{Username: "u", Token: "the-stored-token"}

// spec-registry-refusal-detail §4: a stored credential refused at the token
// step or the manifest step carries the status, the stage and the
// allowlisted OCI code; the message is never kept.
func TestRefusalDetailDigest(t *testing.T) {
	const secretish = "token the-stored-token is not valid for org secret-org"
	big := `{"errors":[{"code":"DENIED","message":"` + strings.Repeat("x", 5000) + `"}]}`
	cases := []struct {
		name           string
		realm, reg     http.HandlerFunc
		status         int
		stage, ociCode string
	}{
		{"token 401", answer(401, `{"errors":[{"code":"UNAUTHORIZED","message":"`+secretish+`"}]}`), tokenOK, 401, registry.StageToken, "UNAUTHORIZED"},
		{"token 403 denied", answer(403, `{"errors":[{"code":"DENIED","message":"`+secretish+`"}]}`), tokenOK, 403, registry.StageToken, "DENIED"},
		// The manifest step is a HEAD, whose answer has no body: no OCI code.
		{"manifest 403 denied", tokenOK, answer(403, `{"errors":[{"code":"DENIED","message":"`+secretish+`"}]}`), 403, registry.StageManifest, ""},
		{"unknown code", answer(401, `{"errors":[{"code":"FOO","message":"x"}]}`), tokenOK, 401, registry.StageToken, ""},
		{"not json", answer(401, `denied: `+secretish), tokenOK, 401, registry.StageToken, ""},
		{"body over 4 KiB", answer(403, big), tokenOK, 403, registry.StageToken, ""},
		{"empty body", answer(401, ``), tokenOK, 401, registry.StageToken, ""},
		// A complete, valid body: only the length check can make it empty.
		{"valid body one byte over 4 KiB", answer(403, padded(4097)), tokenOK, 403, registry.StageToken, ""},
		{"valid body of exactly 4 KiB", answer(403, padded(4096)), tokenOK, 403, registry.StageToken, "DENIED"},
		{"manifest 401 after a good token", tokenOK, answer(401, `{"errors":[{"code":"UNAUTHORIZED"}]}`), 401, registry.StageManifest, ""},
		// HEAD without Docker-Content-Digest falls back to a GET, whose body
		// carries the code.
		{"digest GET fallback 403", tokenOK, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodHead {
				return
			}
			answer(403, `{"errors":[{"code":"DENIED","message":"`+secretish+`"}]}`)(w, r)
		}, 403, registry.StageManifest, "DENIED"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := registrytest.New(t)
			refusalRegistry(f, c.realm, c.reg)
			_, sent, err := f.Client().Digest(context.Background(), registry.Repo{Host: "reg.test", Path: "o/app", Cred: stored}, "v1")
			if !sent {
				t.Fatal("stored credential not sent")
			}
			re := refusal(t, err)
			if re.Code != registry.CodeAuthFailed || re.Status != c.status || re.Stage != c.stage || re.RegistryCode != c.ociCode {
				t.Fatalf("got %+v, want auth_failed %d %s %q", *re, c.status, c.stage, c.ociCode)
			}
			if strings.Contains(fmt.Sprintf("%v %+v", err, *re), "the-stored-token") || strings.Contains(err.Error(), "secret-org") {
				t.Fatalf("error carries registry text: %v %+v", err, *re)
			}
		})
	}
}

func TestRefusalDetailTags(t *testing.T) {
	f := registrytest.New(t)
	refusalRegistry(f, tokenOK, answer(403, `{"errors":[{"code":"DENIED","message":"no"}]}`))
	_, _, err := f.Client().Tags(context.Background(), registry.Repo{Host: "reg.test", Path: "o/app", Cred: stored})
	re := refusal(t, err)
	if re.Code != registry.CodeAuthFailed || re.Status != 403 || re.Stage != registry.StageTags || re.RegistryCode != "DENIED" {
		t.Fatalf("got %+v", *re)
	}
}

// Without a stored credential a refusal at the token step stays not_found
// with no detail.
func TestRefusalDetailAnonymousTokenStep(t *testing.T) {
	for _, status := range []int{401, 403} {
		f := registrytest.New(t)
		refusalRegistry(f, answer(status, `{"errors":[{"code":"DENIED","message":"no"}]}`), tokenOK)
		_, sent, err := f.Client().Digest(context.Background(), registry.Repo{Host: "reg.test", Path: "o/app"}, "v1")
		re := refusal(t, err)
		if sent || re.Code != registry.CodeNotFound || re.Status != 0 || re.Stage != "" || re.RegistryCode != "" {
			t.Fatalf("%d: sent %v, got %+v", status, sent, *re)
		}
	}
}

// Without a stored credential a 401/403 stays not_found with no detail.
func TestRefusalDetailAnonymousUnchanged(t *testing.T) {
	f := registrytest.New(t)
	refusalRegistry(f, tokenOK, answer(403, `{"errors":[{"code":"DENIED","message":"no"}]}`))
	_, sent, err := f.Client().Digest(context.Background(), registry.Repo{Host: "reg.test", Path: "o/app"}, "v1")
	if sent {
		t.Fatal("credential sent without one stored")
	}
	re := refusal(t, err)
	if re.Code != registry.CodeNotFound || re.Status != 0 || re.Stage != "" || re.RegistryCode != "" {
		t.Fatalf("got %+v", *re)
	}
}
