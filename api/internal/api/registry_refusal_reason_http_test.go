package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/more-than-code/bot-control-plane/api/internal/store"
)

// restrictedBody is GitHub's 403 for an org with OAuth App access
// restrictions (trimmed).
const restrictedBody = `{"message":"Although you appear to have the correct authorization credentials, the ` +
	"`kea-studio`" + ` organization has enabled OAuth App access restrictions, meaning that data access to third-parties is limited. GITHUB-BODY"}`

const grantURL = "https://github.com/settings/connections/applications/test-client"

// refusedFixture is Connect GitHub done, then ghcr.io refusing the stored
// login for every image, as it does for an org that has not granted the
// app. The web entry uses ghcr.io/kea-studio/app.
func refusedFixture(t *testing.T) *refreshFixture {
	t.Helper()
	rf := newRefreshFixture(t)
	createImage(t, rf.w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/kea-studio/app:v1"}`)
	rf.connect(t)
	rf.private("nobody", "never", true)
	return rf
}

// refusalOf asks image-access for the kea-studio image past the cache and
// returns the access and the refusal object (nil when absent).
func refusalOf(t *testing.T, rf *refreshFixture) (string, map[string]any) {
	t.Helper()
	rf.at(rf.cur.Sub(rf.w.now) + 2*time.Minute)
	status, env, raw := mustCall(t, rf.w, rf.owner, http.MethodGet, accessPath(rf.project, "ghcr.io/kea-studio/app:v1"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	rf.seen = append(rf.seen, raw)
	p := objectPayload(t, env)
	access, _ := p["access"].(string)
	refusal, _ := p["refusal"].(map[string]any)
	return access, refusal
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// api-v1 v1.52: an org that has not granted the app is named, with the
// GitHub page where it is granted, in image-access, readiness and the
// registry_login problem; reconnecting is not suggested.
func TestRegistryRefusalOrgRestricted(t *testing.T) {
	rf := refusedFixture(t)
	rf.gh.set(func(g *refreshGitHub) { g.packageStatus, g.packageBody = http.StatusForbidden, restrictedBody })
	access, refusal := refusalOf(t, rf)
	want := `{"account":"octo-cat","owner":"kea-studio","reason":"org_restricted","url":"` + grantURL + `"}`
	if access != accessLoginFailed || jsonOf(refusal) != want {
		t.Fatalf("access %s refusal %s", access, jsonOf(refusal))
	}
	// Cached with the answer: a second read within 60 s does not ask GitHub.
	rf.gh.set(func(g *refreshGitHub) { g.packageReads = 0 })
	status, env, _ := mustCall(t, rf.w, rf.owner, http.MethodGet, accessPath(rf.project, "ghcr.io/kea-studio/app:v1"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if jsonOf(objectPayload(t, env)["refusal"]) != want {
		t.Fatal("cached answer lost the refusal")
	}
	rf.gh.set(func(g *refreshGitHub) {
		if g.packageReads != 0 {
			t.Fatalf("cached answer asked GitHub %d times", g.packageReads)
		}
	})

	_, checks, raw := readinessOf(t, rf.w, rf.owner, rf.project, "web", "")
	rf.seen = append(rf.seen, raw)
	reg := findCheck(t, checks, "registry")
	if reg.Status != readyMissing || !strings.Contains(reg.Message, "the kea-studio organization has not granted the Deploybot GitHub app access") ||
		strings.Contains(reg.Message, "; reconnect GitHub") {
		t.Fatalf("readiness %+v", reg)
	}
	if jsonOf(reg.Fix) != `{"action":"github_org_access","owner":"kea-studio","registry":"ghcr.io","url":"`+grantURL+`"}` {
		t.Fatalf("fix %s", jsonOf(reg.Fix))
	}

	got, ok := problemOf(t, rf.w, store.ProblemRegistryLogin, rf.project+":ghcr.io")
	if !ok || got.ResolvedAt != nil || !strings.Contains(got.Detail, "The kea-studio organization has not granted") ||
		!strings.HasSuffix(got.Detail, " at "+grantURL) || len(got.Detail) > store.MaxProblemText {
		t.Fatalf("problem %+v", got)
	}
	rf.assertNoTokens(t)
}

// A 404 on the org package: the connected account cannot see it.
func TestRegistryRefusalNoPackageAccess(t *testing.T) {
	rf := refusedFixture(t)
	access, refusal := refusalOf(t, rf)
	if access != accessLoginFailed || jsonOf(refusal) != `{"account":"octo-cat","owner":"kea-studio","reason":"no_package_access"}` {
		t.Fatalf("access %s refusal %s", access, jsonOf(refusal))
	}
	_, checks, _ := readinessOf(t, rf.w, rf.owner, rf.project, "web", "")
	reg := findCheck(t, checks, "registry")
	if !strings.Contains(reg.Message, "the GitHub account octo-cat cannot see the kea-studio package") || reg.Fix["action"] != fixConnectGitHub {
		t.Fatalf("readiness %+v", reg)
	}
	got, _ := problemOf(t, rf.w, store.ProblemRegistryLogin, rf.project+":ghcr.io")
	if !strings.HasPrefix(got.Detail, "The GitHub account octo-cat cannot see the kea-studio package.") {
		t.Fatalf("problem %q", got.Detail)
	}
	rf.assertNoTokens(t)
}

// GitHub refuses the token itself, or it lost read:packages: reconnect.
func TestRegistryRefusalSignIn(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(rf *refreshFixture)
	}{
		{"revoked", func(rf *refreshFixture) { rf.gh.expireAll(); rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 0 }) }},
		{"scope", func(rf *refreshFixture) { rf.gh.set(func(g *refreshGitHub) { g.scopes = "repo" }) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rf := newRefreshFixture(t)
			createImage(t, rf.w, rf.owner, rf.sc.project.ID, "web", `{"kind":"service","image":"ghcr.io/kea-studio/app:v1"}`)
			rf.gh.set(func(g *refreshGitHub) { g.expiresIn = 0 })
			rf.connect(t)
			rf.private("nobody", "never", true)
			tc.edit(rf)
			access, refusal := refusalOf(t, rf)
			if access != accessLoginFailed || refusal["reason"] != "github_sign_in" || refusal["owner"] != nil {
				t.Fatalf("access %s refusal %s", access, jsonOf(refusal))
			}
			_, checks, _ := readinessOf(t, rf.w, rf.owner, rf.project, "web", "")
			reg := findCheck(t, checks, "registry")
			if !strings.HasSuffix(reg.Message, "; reconnect GitHub") || reg.Fix["action"] != fixConnectGitHub {
				t.Fatalf("readiness %+v", reg)
			}
			got, _ := problemOf(t, rf.w, store.ProblemRegistryLogin, rf.project+":ghcr.io")
			if got.Detail != detailGitHubLoginRefused {
				t.Fatalf("problem %q", got.Detail)
			}
			rf.assertNoTokens(t)
		})
	}
}

// A reason GitHub does not give (a 403 without the restrictions text, or
// the account's own package) leaves refusal out and today's texts in place.
func TestRegistryRefusalUnknown(t *testing.T) {
	rf := refusedFixture(t)
	rf.gh.set(func(g *refreshGitHub) {
		g.packageStatus, g.packageBody = http.StatusForbidden, `{"message":"GITHUB-BODY"}`
	})
	access, refusal := refusalOf(t, rf)
	if access != accessLoginFailed || refusal != nil {
		t.Fatalf("access %s refusal %s", access, jsonOf(refusal))
	}
	_, checks, _ := readinessOf(t, rf.w, rf.owner, rf.project, "web", "")
	if reg := findCheck(t, checks, "registry"); reg.Message != "the stored login for ghcr.io was refused; reconnect GitHub" {
		t.Fatalf("readiness %+v", reg)
	}
	got, _ := problemOf(t, rf.w, store.ProblemRegistryLogin, rf.project+":ghcr.io")
	if got.Detail != detailGitHubLoginRefused {
		t.Fatalf("problem %q", got.Detail)
	}

	// The account's own package: no org read at all.
	own := newRefreshFixture(t)
	own.connect(t)
	own.private("nobody", "never", true)
	own.at(own.cur.Sub(own.w.now) + 2*time.Minute)
	status, env, _ := mustCall(t, own.w, own.owner, http.MethodGet, accessPath(own.project, "ghcr.io/octo-cat/app:v1"), "")
	mustHTTP(t, status, env, http.StatusOK, "ok", "ok")
	if p := objectPayload(t, env); p["access"] != accessLoginFailed || p["refusal"] != nil {
		t.Fatalf("own package %s", jsonOf(p))
	}
	own.gh.set(func(g *refreshGitHub) {
		if g.packageReads != 0 {
			t.Fatal("own package read an org package")
		}
	})
	rf.assertNoTokens(t)
}

// A manual ghcr.io login never asks GitHub and carries no refusal.
func TestRegistryRefusalManualLogin(t *testing.T) {
	rf := newRefreshFixture(t)
	putRegistry(t, rf.w, rf.owner, rf.project, "ghcr.io", "octo-cat", "wrong-token")
	rf.private("nobody", "never", true)
	access, refusal := refusalOf(t, rf)
	if access != accessLoginFailed || refusal != nil {
		t.Fatalf("access %s refusal %s", access, jsonOf(refusal))
	}
	rf.gh.set(func(g *refreshGitHub) {
		if g.packageReads != 0 {
			t.Fatal("manual login asked GitHub")
		}
	})
}
