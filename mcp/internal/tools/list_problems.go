package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

// listProblemsSchema states exactly one of project_id or org_id, as
// list_hosts. It is also checked in code.
const listProblemsSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "project_id": {"type": "string", "format": "uuid"},
    "org_id": {"type": "string", "format": "uuid"}
  },
  "oneOf": [
    {"required": ["project_id"]},
    {"required": ["org_id"]}
  ]
}`

func registerListProblems(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name:        "list_problems",
		Description: "List open problems for a project or an organization (api-v1 v1.41); pass exactly one of project_id or org_id. These are the problems the user sees in the console bell: host_offline (an always-on host has not reported for more than 10 minutes; organization owners and admins only, so org_id only), registry_login (a stored registry login was refused), deploy (the latest deploy of an entry on a host failed or was rolled back), host_file (a host config file could not be restored after a failed apply, or was changed on the host; organization owners and admins only, fixed in the console's Config files tab, since config files are not available over MCP), and host_resource (an always-on host is low on memory, a disk mount is nearly full (needs_action), or the CPU is busy or the load is high for 15 minutes (heads_up); organization owners and admins only; per-host thresholds in get_alert_settings and set_alert_settings; query_metrics shows the numbers). Each has level needs_action or heads_up, title, detail, opened_at and a console href. With project_id, the problems of that project the user may see; with org_id (organization owners and admins only), every problem in the organization. The summary names the usual fix: for a refused registry login, the detail says why: when it names a GitHub organization that has not granted the Deploybot GitHub app, send the person the GitHub link it gives (reconnecting will not help); otherwise ask the person to reconnect it (connect_registry sends them a connect request); for an offline host, check the machine and its bot-agent service; for a deploy, read deploy_history for that entry and host (the reason and the stored log tail), then fix the cause; for a host resource, free memory or disk on the host, or raise its threshold. Reads stored problems. Does not contact the hosts or the registries.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(listProblemsSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			ProjectID *string `json:"project_id"`
			OrgID     *string `json:"org_id"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if (in.ProjectID == nil) == (in.OrgID == nil) {
			return badRequest(errors.New("exactly one of project_id or org_id is required"))
		}
		var path string
		if in.OrgID != nil {
			if err := requireUUID("org_id", *in.OrgID); err != nil {
				return badRequest(err)
			}
			path = idPath("v1", "orgs", *in.OrgID, "problems")
		} else {
			if err := requireUUID("project_id", *in.ProjectID); err != nil {
				return badRequest(err)
			}
			path = idPath("v1", "projects", *in.ProjectID, "problems")
		}
		payload, fail := get(ctx, api, path, nil)
		if fail != nil {
			return fail
		}
		return present(payload, summarizeProblems(payload)+problemLinks(api, payload))
	})
}

// problemFix is the next step the summary names per kind.
var problemFix = map[string]string{
	"host_offline":   "check the machine and its bot-agent service",
	"registry_login": "ask the person to reconnect it (connect_registry sends a connect request)",
	"deploy":         "read deploy_history for the reason and the stored log tail, fix the cause, then deploy again or roll back",
	"host_file":      "an organization owner or admin opens the host's Config files tab in the console (config files are not available over MCP)",
	"host_resource":  "free memory or disk on the host, or raise its threshold (set_alert_settings)",
}

// summarizeProblems is "N open problems: <title> since <time> (<level>) — <fix>; …".
func summarizeProblems(payload json.RawMessage) string {
	var body struct {
		Items []struct {
			Kind       string     `json:"kind"`
			Level      string     `json:"level"`
			Title      string     `json:"title"`
			Detail     string     `json:"detail"`
			OpenedAt   time.Time  `json:"opened_at"`
			ResolvedAt *time.Time `json:"resolved_at"`
		} `json:"items"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return "problems"
	}
	open, resolved := []string{}, 0
	for _, p := range body.Items {
		if p.ResolvedAt != nil {
			resolved++
			continue
		}
		line := summaryField(p.Title) + " since " + p.OpenedAt.UTC().Format("2006-01-02 15:04") + " UTC (" + summaryField(p.Level) + ")"
		fix, ok := problemFix[p.Kind]
		if p.Kind == "registry_login" && p.Detail != "" {
			// api-v1 v1.52: the detail says why; for an organization that has
			// not granted the app it carries the GitHub link that fixes it.
			line += ": " + strings.TrimSuffix(clampField(p.Detail, 320), ".")
			if strings.Contains(p.Detail, "https://github.com/settings/connections/applications/") {
				fix = "send the person that GitHub link; reconnecting will not help"
			}
		}
		if ok {
			line += " — " + fix
		}
		open = append(open, line)
	}
	out := "No open problems."
	switch len(open) {
	case 0:
	case 1:
		out = "1 open problem: " + open[0] + "."
	default:
		out = fmt.Sprintf("%d open problems: %s.", len(open), strings.Join(open, "; "))
	}
	if resolved > 0 {
		out += fmt.Sprintf(" %d resolved.", resolved)
	}
	return out
}

// problemLinks is a summary line with each open problem's console page
// (its href on the console origin), or "".
func problemLinks(api *apiclient.Client, payload json.RawMessage) string {
	var body struct {
		Items []struct {
			Title      string     `json:"title"`
			Href       string     `json:"href"`
			ResolvedAt *time.Time `json:"resolved_at"`
		} `json:"items"`
	}
	if json.Unmarshal(payload, &body) != nil {
		return ""
	}
	parts := []string{}
	for _, p := range body.Items {
		if p.ResolvedAt != nil || len(parts) == maxListItems {
			continue
		}
		if link := api.ConsoleURL(p.Href); link != "" && !strings.ContainsAny(p.Href, " \t\r\n") {
			parts = append(parts, summaryField(p.Title)+": "+link)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "\nOpen in the console: " + strings.Join(parts, "; ")
}
