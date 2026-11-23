package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

const testGrantURL = "https://github.com/settings/connections/applications/Iv1.abc123"

// api-v1 v1.53: the org_restricted fix passes the GitHub link, never a
// console link, and Next tells the agent not to ask for a reconnect.
func TestReadinessGitHubOrgAccess(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"ready": false,
		"checks": []any{
			map[string]any{"check": "registry", "status": "missing",
				"message": "ghcr.io refused the GitHub connection: the kea-studio organization has not granted the Deploybot GitHub app access; an owner of kea-studio grants it on GitHub (reconnecting will not help)",
				"fix":     map[string]any{"action": "github_org_access", "registry": "ghcr.io", "owner": "kea-studio", "url": testGrantURL}},
		},
	})
	out, text, err := projectReadinessLinks(linkClient(t, "https://console.example"), payload, lnProject, "web")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Checks []readyRow `json:"checks"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	row := body.Checks[0]
	if row.Fix["action"] != "github_org_access" || row.Fix["owner"] != "kea-studio" || row.Fix["url"] != testGrantURL || row.Fix["console_url"] != nil {
		t.Fatalf("fix %+v", row.Fix)
	}
	if !strings.Contains(row.Next, "an owner of kea-studio clicks Grant") || !strings.HasSuffix(row.Next, "Open: "+testGrantURL) ||
		!strings.Contains(row.Message, "kea-studio organization has not granted") || !strings.Contains(text, testGrantURL) {
		t.Fatalf("row %+v text %s", row, text)
	}
	// A link that is not GitHub's application page is dropped.
	bad := projectFix(map[string]any{"action": "github_org_access", "owner": "kea-studio", "url": "https://evil.example/x"})
	if bad["url"] != nil || !strings.Contains(nextStep(bad, lnProject, "web"), "Authorized OAuth Apps") {
		t.Fatalf("bad %+v", bad)
	}
	if got := projectFix(map[string]any{"action": "github_org_access", "owner": "-bad"}); got["action"] != "none" {
		t.Fatalf("no owner %+v", got)
	}
}

func TestGitHubRefusal(t *testing.T) {
	for _, tc := range []struct {
		in   map[string]any
		want string
	}{
		{map[string]any{"reason": "org_restricted", "owner": "kea-studio", "account": "octo-cat", "url": testGrantURL},
			`{"account":"octo-cat","owner":"kea-studio","reason":"org_restricted","url":"` + testGrantURL + `"}`},
		{map[string]any{"reason": "org_restricted", "owner": "kea studio"}, `null`},
		{map[string]any{"reason": "no_package_access", "owner": "kea-studio", "url": "javascript:x"}, `{"owner":"kea-studio","reason":"no_package_access"}`},
		{map[string]any{"reason": "github_sign_in"}, `{"reason":"github_sign_in"}`},
		{map[string]any{"reason": "other"}, `null`},
		{nil, `null`},
	} {
		got := githubRefusal(tc.in)
		var raw []byte
		if got == nil {
			raw = []byte("null")
		} else {
			raw, _ = json.Marshal(got)
		}
		if string(raw) != tc.want {
			t.Errorf("%v: %s, want %s", tc.in, raw, tc.want)
		}
	}
	org := refusalSummary(map[string]any{"reason": "org_restricted", "owner": "kea-studio", "url": testGrantURL})
	if !strings.Contains(org, "kea-studio organization has not granted") || !strings.HasSuffix(org, "Open: "+testGrantURL) || strings.Contains(org, "connect_registry") {
		t.Fatalf("org %q", org)
	}
	if s := refusalSummary(map[string]any{"reason": "no_package_access", "owner": "kea-studio", "account": "octo-cat"}); !strings.Contains(s, "The GitHub account octo-cat cannot see the kea-studio package") {
		t.Fatalf("no access %q", s)
	}
	if s := refusalSummary(map[string]any{"reason": "github_sign_in"}); !strings.Contains(s, "reconnect GitHub") {
		t.Fatalf("sign in %q", s)
	}
}

// list_problems passes a registry_login detail on, and for an org that has
// not granted the app tells the agent to send the GitHub link.
func TestSummarizeRegistryRefusalProblem(t *testing.T) {
	detail := "The kea-studio organization has not granted the Deploybot GitHub app access, so reconnecting will not help. An org owner clicks Grant (a member clicks Request) next to kea-studio at " + testGrantURL
	payload, _ := json.Marshal(map[string]any{"items": []any{
		map[string]any{"kind": "registry_login", "level": "needs_action", "title": "ghcr.io login was refused", "detail": detail, "opened_at": "2026-11-23T08:00:00Z", "resolved_at": nil},
	}})
	text := summarizeProblems(payload)
	if !strings.Contains(text, "(needs_action): "+detail+" — send the person that GitHub link; reconnecting will not help.") || strings.Contains(text, "connect_registry") {
		t.Fatalf("summary %q", text)
	}
}
