package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

// listHostsSchema states exactly one of project_id or org_id. It is also
// checked in code: clients do not all enforce oneOf.
const listHostsSchema = `{
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

func registerListHosts(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name:        "list_hosts",
		Description: "List hosts for a project or an organization; pass exactly one of project_id or org_id. Hosts belong to an organization, and projects get access to them. With project_id, the hosts that project may use, each with access_level (metrics: status, charts and processes; deploy: also its services, logs and catalog runs). With org_id (organization owners and admins only), every host of the organization; project access per host is shown in the console, not here. Revoked hosts list with status revoked until an org admin removes them in the console; removed (archived) hosts are not listed. Reads stored host records (id, name, status, last seen, agent_version, and related fields); update_available is true when a newer bot-agent (latest_agent_version) exists (false for a revoked host), and the summary then says so. The summary lists, per host, the shared networks and subnets open to the caller (the host's administrator (root) opened them in runner.toml), and whether the host's Deploybot edge is on (it serves set_routes routes; an org admin turns it on in the console), and each host's console page, where an org owner or admin manages its edge, policy and access; post that link for the person rather than describing clicks. Does not contact the hosts.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(listHostsSchema),
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
		org := in.OrgID != nil
		var path string
		if org {
			if err := requireUUID("org_id", *in.OrgID); err != nil {
				return badRequest(err)
			}
			path = idPath("v1", "orgs", *in.OrgID, "hosts")
		} else {
			if err := requireUUID("project_id", *in.ProjectID); err != nil {
				return badRequest(err)
			}
			path = idPath("v1", "projects", *in.ProjectID, "hosts")
		}
		payload, fail := get(ctx, api, path, nil)
		if fail != nil {
			return fail
		}
		return present(payload, summarizeHosts(payload, org)+hostLinks(api, payload))
	})
}

// hostLinks is a summary line with each host's console page, or "" when
// there is no console origin.
func hostLinks(api *apiclient.Client, payload json.RawMessage) string {
	var body struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if json.Unmarshal(payload, &body) != nil {
		return ""
	}
	parts := []string{}
	for i, h := range body.Items {
		if i == maxListItems {
			break
		}
		if link := hostURL(api, h.ID, ""); link != "" {
			parts = append(parts, summaryField(h.Name)+" "+link)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "\nHost pages: " + strings.Join(parts, ", ")
}
