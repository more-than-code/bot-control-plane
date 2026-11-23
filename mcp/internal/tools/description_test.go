package tools

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// listToolDescriptions registers every tool and reads tools/list over an in-memory session.
func listToolDescriptions(t *testing.T) map[string]string {
	t.Helper()
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	Register(server, nil)
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, tool := range res.Tools {
		out[tool.Name] = tool.Description
	}
	return out
}

// Brief program-entries-over-mcp item A: agents read the first words first.
func TestCatalogWriteToolsSayImageEntriesOnly(t *testing.T) {
	desc := listToolDescriptions(t)
	for _, name := range []string{"create_catalog_entry", "update_catalog_entry", "delete_catalog_entry"} {
		d, ok := desc[name]
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if !strings.HasPrefix(d, "Image entries only. ") {
			t.Errorf("%s description starts %q", name, d[:min(len(d), 40)])
		}
		if !strings.Contains(d, "in the console") {
			t.Errorf("%s description does not send program entries to the console", name)
		}
	}
}

// Brief item E: a program confirm can answer before the host reports ports.
func TestConfirmDescriptionSaysProgramPortsComeLater(t *testing.T) {
	d := listToolDescriptions(t)["confirm"]
	if !strings.Contains(d, "ports and started_at can still be empty") || !strings.Contains(d, "entry_status") {
		t.Errorf("confirm description lacks the ports note: %q", d)
	}
}

// Organizations spec §4.4: the read tools say how orgs fit, and there is no
// write tool for orgs, members, join tokens or host access.
func TestOrgToolDescriptions(t *testing.T) {
	desc := listToolDescriptions(t)
	for name, phrases := range map[string][]string{
		"list_orgs":     {"organizations", "Read-only", "in the console"},
		"list_projects": {"organization", "org_id", "via"},
		"list_hosts":    {"exactly one of project_id or org_id", "access_level", "owners and admins only", "removed (archived) hosts are not listed"},
	} {
		d, ok := desc[name]
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		for _, phrase := range phrases {
			if !strings.Contains(d, phrase) {
				t.Errorf("%s description lacks %q: %q", name, phrase, d)
			}
		}
	}
	for name := range desc {
		for _, word := range []string{"org", "member", "token", "access", "grant", "invite"} {
			// image_access reads registry access for an image, not org or host access.
			if strings.Contains(name, word) && name != "list_orgs" && name != "image_access" {
				t.Errorf("tool %s looks like an org, membership, token or access tool", name)
			}
		}
	}
}

// Spec agent-usability-1 F1: each request tool's result has the link alone
// on its first line, and each description names the several-links habit.
func TestRequestToolsLinkFirst(t *testing.T) {
	desc := listToolDescriptions(t)
	for _, name := range []string{"request_env_value", "request_file", "connect_registry"} {
		if !strings.Contains(desc[name], severalLinks) || !strings.Contains(desc[name], "alone on its first line") {
			t.Errorf("%s description lacks the several-links habit: %q", name, desc[name])
		}
	}
	for _, c := range []struct {
		tool  string
		args  map[string]any
		reply func(*http.Request) (int, string)
		url   string
	}{
		{"request_env_value", vrArgs, createdReply, vrURL},
		{"request_file", frArgs, fileCreatedReply, frURL},
		{"connect_registry", crArgs, crReply("pending"), crURL},
	} {
		cs, _ := vrSession(t, nil, c.reply)
		res, text, _ := callVR(t, cs, c.tool, c.args)
		if res.IsError {
			t.Fatalf("%s error %s", c.tool, text)
		}
		first, rest, _ := strings.Cut(text, "\n")
		if first != c.url || strings.Contains(rest, c.url) {
			t.Errorf("%s text does not lead with the link alone: %q", c.tool, text)
		}
	}
}

// api-v1 v1.41 item 5: list_problems names its scope, kinds and fixes (deploy_history since v1.47).
func TestListProblemsDescription(t *testing.T) {
	d := listToolDescriptions(t)["list_problems"]
	for _, phrase := range []string{"exactly one of project_id or org_id", "host_offline", "registry_login", "deploy", "needs_action", "connect_registry", "deploy_history", "Does not contact"} {
		if !strings.Contains(d, phrase) {
			t.Errorf("list_problems description lacks %q: %q", phrase, d)
		}
	}
}

// spec-deploy-stuck §2.4: a confirm past its wait names the end time and
// points at the deploy history, not entry_status.
func TestConfirmDescriptionNamesDeployHistory(t *testing.T) {
	d := listToolDescriptions(t)["confirm"]
	if !strings.Contains(d, "ends by at the latest") || !strings.Contains(d, "deploy history (deploy_entry for that host)") || strings.Contains(d, "check entry_status for the outcome") {
		t.Errorf("confirm description: %q", d)
	}
}
