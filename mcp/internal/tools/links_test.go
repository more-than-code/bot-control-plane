package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

const (
	lnProject = "11111111-1111-4111-8111-111111111111"
	lnHost    = "22222222-2222-4222-8222-222222222222"
	lnOrg     = "33333333-3333-4333-8333-333333333333"
)

func linkClient(t *testing.T, origin string) *apiclient.Client {
	t.Helper()
	c, err := apiclient.New("https://api.example", false)
	if err != nil {
		t.Fatal(err)
	}
	c.SetConsoleOrigin(origin)
	return c
}

func TestFixURL(t *testing.T) {
	api := linkClient(t, "https://console.example/")
	for _, tc := range []struct {
		fix  map[string]any
		want string
	}{
		{map[string]any{"action": "connect_github", "registry": "ghcr.io"}, "https://console.example/projects/" + lnProject + "?tab=settings#registries"},
		{map[string]any{"action": "add_registry_login", "registry": "quay.io"}, "https://console.example/projects/" + lnProject + "?tab=settings#registries"},
		{map[string]any{"action": "create_env_set", "set": "web"}, "https://console.example/projects/" + lnProject + "?tab=settings#env-sets"},
		{map[string]any{"action": "fill_value", "set": "@web", "key": "K"}, "https://console.example/projects/" + lnProject + "?tab=catalog&catalog.q=web"},
		{map[string]any{"action": "add_file", "name": "tls.pem"}, "https://console.example/projects/" + lnProject + "?tab=settings#files"},
		{map[string]any{"action": "grant_deploy_access", "host_id": lnHost}, "https://console.example/hosts/" + lnHost + "#access"},
		{map[string]any{"action": "host_policy_snippet", "host_id": lnHost, "image_pattern": "ghcr.io/kea-studio/marketing-os-prototype"},
			"https://console.example/hosts/" + lnHost + "?tab=policy&policy.add_image=ghcr.io%2Fkea-studio%2Fmarketing-os-prototype"},
		{map[string]any{"action": "allow_edge", "host_id": lnHost}, "https://console.example/hosts/" + lnHost + "?tab=policy&policy.edge=on"},
		{map[string]any{"action": "turn_on_edge", "host_id": lnHost}, "https://console.example/hosts/" + lnHost + "#edge"},
		{map[string]any{"action": "verify_domain", "org_id": lnOrg}, "https://console.example/orgs/" + lnOrg + "?tab=settings#domains"},
		{map[string]any{"action": "grant_deploy_access", "host_id": "not-a-uuid"}, ""},
		{map[string]any{"action": "none"}, ""},
	} {
		if got := fixURL(api, tc.fix, lnProject); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.fix, got, tc.want)
		}
	}
	// No origin, no links.
	if got := fixURL(linkClient(t, ""), map[string]any{"action": "allow_edge", "host_id": lnHost}, lnProject); got != "" {
		t.Fatalf("%q", got)
	}
	if got := fixURL(nil, map[string]any{"action": "allow_edge", "host_id": lnHost}, lnProject); got != "" {
		t.Fatalf("%q", got)
	}
}

// check_readiness puts console_url on a person's fix and names it in Next.
func TestReadinessConsoleURL(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"ready": false,
		"checks": []any{
			map[string]any{"check": "host_policy", "status": "missing", "message": "this host does not allow image ghcr.io/kea/site",
				"fix": map[string]any{"action": "host_policy_snippet", "host_id": lnHost, "image_pattern": "ghcr.io/kea/site"}},
			map[string]any{"check": "routes", "status": "warn", "message": "the edge is not allowed on vm",
				"fix": map[string]any{"action": "allow_edge", "host_id": lnHost}},
			map[string]any{"check": "env_sets", "status": "ok", "message": "fine", "fix": map[string]any{"action": "none"}},
		},
	})
	out, text, err := projectReadinessLinks(linkClient(t, "https://console.example"), payload, lnProject, "site")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Checks []struct {
			Fix  map[string]any `json:"fix"`
			Next string         `json:"next"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	want := "https://console.example/hosts/" + lnHost + "?tab=policy&policy.add_image=ghcr.io%2Fkea%2Fsite"
	if body.Checks[0].Fix["console_url"] != want || !strings.Contains(body.Checks[0].Next, "Open: "+want) {
		t.Fatalf("%+v", body.Checks[0])
	}
	if body.Checks[1].Fix["console_url"] != "https://console.example/hosts/"+lnHost+"?tab=policy&policy.edge=on" {
		t.Fatalf("%+v", body.Checks[1])
	}
	if _, ok := body.Checks[2].Fix["console_url"]; ok {
		t.Fatal("an ok check got a link")
	}
	if !strings.Contains(text, "Policy tab") {
		t.Fatalf("%s", text)
	}
}

func TestHostAndProblemLinks(t *testing.T) {
	api := linkClient(t, "https://console.example")
	hosts := json.RawMessage(`{"items":[{"id":"` + lnHost + `","name":"bot-ops-vm"},{"id":"bad","name":"x"}]}`)
	if got := hostLinks(api, hosts); got != "\nHost pages: bot-ops-vm https://console.example/hosts/"+lnHost {
		t.Fatalf("%q", got)
	}
	if hostLinks(linkClient(t, ""), hosts) != "" {
		t.Fatal("links with no origin")
	}
	probs := json.RawMessage(`{"items":[{"title":"pi4 offline","href":"/hosts/` + lnHost + `"},{"title":"x","href":"//evil.example/"},{"title":"old","href":"/a","resolved_at":"2026-11-23T00:00:00Z"}]}`)
	if got := problemLinks(api, probs); got != "\nOpen in the console: pi4 offline: https://console.example/hosts/"+lnHost {
		t.Fatalf("%q", got)
	}
}
