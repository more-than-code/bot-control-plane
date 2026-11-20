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
		Description: "List open problems for a project or an organization (api-v1 v1.41); pass exactly one of project_id or org_id. These are the problems the user sees in the console bell: host_offline (an always-on host has not reported for more than 10 minutes; organization owners and admins only, so org_id only), registry_login (a stored registry login was refused), and deploy (the latest deploy of an entry on a host failed or was rolled back). Each has level needs_action or heads_up, title, detail, opened_at and a console href. With project_id, the problems of that project the user may see; with org_id (organization owners and admins only), every problem in the organization. The summary names the usual fix: for a refused registry login, ask the person to reconnect it (connect_registry sends them a connect request); for an offline host, check the machine and its bot-agent service; for a deploy, look at entry_logs and entry_status. Reads stored problems. Does not contact the hosts or the registries.",
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
		return present(payload, summarizeProblems(payload))
	})
}

// problemFix is the next step the summary names per kind.
var problemFix = map[string]string{
	"host_offline":   "check the machine and its bot-agent service",
	"registry_login": "ask the person to reconnect it (connect_registry sends a connect request)",
	"deploy":         "look at entry_logs and entry_status, then deploy again or roll back",
}

// summarizeProblems is "N open problems: <title> since <time> (<level>) — <fix>; …".
func summarizeProblems(payload json.RawMessage) string {
	var body struct {
		Items []struct {
			Kind       string     `json:"kind"`
			Level      string     `json:"level"`
			Title      string     `json:"title"`
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
		if fix, ok := problemFix[p.Kind]; ok {
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
