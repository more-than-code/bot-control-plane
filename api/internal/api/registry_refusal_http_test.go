package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// spec-registry-refusal-detail §2 item 2: a refused stored login logs the
// status, the stage and the allowlisted OCI code, never the token or the
// registry's message; other outcomes keep today's fields.
func TestRegistryLookupLogsRefusalDetail(t *testing.T) {
	f := newRegistryFixture(t)
	f.w.srv.Secrets = testBox(t)
	buf := captureLog(f.w)
	const msg = "token is not valid for org secret-org"
	f.fake.Handle(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if u, p, ok := r.BasicAuth(); ok {
				if u == "robot" && p == "right-token-123" {
					fmt.Fprint(w, `{"token":"good"}`)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"errors":[{"code":"DENIED","message":"`+msg+`"}]}`)
				return
			}
			fmt.Fprint(w, `{"token":"anon"}`)
			return
		}
		switch r.Header.Get("Authorization") {
		case "":
			w.Header().Set("WWW-Authenticate", `Bearer realm="https://`+r.Host+`/token",service="`+r.Host+`"`)
			w.WriteHeader(http.StatusUnauthorized)
		case "Bearer good":
			w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("e", 64))
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	})

	putRegistry(t, f.w, f.owner, f.project, "ghcr.io", "robot", "wrong-token-456")
	if p := accessOf(t, f, f.owner, "ghcr.io/joe/private:v1"); p["access"] != "login_failed" {
		t.Fatalf("access %v", p)
	}
	log := buf.String()
	if !strings.Contains(log, "outcome=login_failed") || !strings.Contains(log, "auth=stored status=403 stage=token registry_code=DENIED") {
		t.Fatalf("log line: %s", log)
	}
	if strings.Contains(log, "wrong-token-456") || strings.Contains(log, "secret-org") {
		t.Fatalf("log carries the token or the registry message: %s", log)
	}

	buf.Reset()
	putRegistry(t, f.w, f.owner, f.project, "ghcr.io", "robot", "right-token-123")
	if p := accessOf(t, f, f.owner, "ghcr.io/joe/private:v2"); p["access"] != "private_ok" {
		t.Fatalf("access %v", p)
	}
	line := lookupLine(buf.String())
	if !strings.Contains(line, "outcome=private_ok") || strings.Contains(line, "status=") || strings.Contains(line, "stage=") || strings.Contains(line, "registry_code=") {
		t.Fatalf("private_ok log line: %s", line)
	}
}

// lookupLine is the registry lookup line of a log, without the request lines.
func lookupLine(log string) string {
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, `msg="registry lookup"`) {
			return l
		}
	}
	return ""
}

// The tag lookup (cachedLookup) logs the same fields for auth_failed, and an
// anonymous not_found logs none of them.
func TestRegistryTagLookupLogsRefusalDetail(t *testing.T) {
	f := newRegistryFixture(t)
	f.w.srv.Secrets = testBox(t)
	buf := captureLog(f.w)
	f.fake.Handle(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if _, _, ok := r.BasicAuth(); ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"errors":[{"code":"DENIED","message":"no access for secret-org"}]}`)
				return
			}
			fmt.Fprint(w, `{"token":"anon"}`)
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="https://`+r.Host+`/token",service="`+r.Host+`"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	})

	status, env, _ := mustCall(t, f.w, f.owner, http.MethodGet, tagsPath(f.project, "ghcr.io/joe/open"), "")
	mustHTTP(t, status, env, http.StatusBadGateway, "registry_unavailable", "registry unavailable")
	if line := lookupLine(buf.String()); !strings.Contains(line, "outcome=not_found") ||
		strings.Contains(line, "status=") || strings.Contains(line, "stage=") || strings.Contains(line, "registry_code=") {
		t.Fatalf("anonymous not_found line: %s", line)
	}

	buf.Reset()
	putRegistry(t, f.w, f.owner, f.project, "ghcr.io", "robot", "wrong-token-456")
	status, env, _ = mustCall(t, f.w, f.owner, http.MethodGet, tagsPath(f.project, "ghcr.io/joe/private"), "")
	mustHTTP(t, status, env, http.StatusBadGateway, "registry_unavailable", "registry unavailable")
	line := lookupLine(buf.String())
	if !strings.Contains(line, "outcome=auth_failed") || !strings.Contains(line, "auth=stored status=403 stage=token registry_code=DENIED") {
		t.Fatalf("auth_failed line: %s", line)
	}
	if strings.Contains(buf.String(), "wrong-token-456") || strings.Contains(buf.String(), "secret-org") {
		t.Fatalf("log carries the token or the registry message: %s", buf.String())
	}
}
