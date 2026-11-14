package tools

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

const listOrgsSchema = `{"type":"object","additionalProperties":false}`

func registerListOrgs(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name:        "list_orgs",
		Description: "List the organizations the signed-in user belongs to (id, name, slug, role). An organization owns hosts and contains projects; its owners and admins act as admin on every project in it and can list all of its hosts with list_hosts org_id. Reads the caller's profile from stored API data. Read-only: organizations, members, join tokens and host access are managed in the console. Does not contact any host.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(listOrgsSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct{}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		payload, fail := get(ctx, api, "/v1/me", nil)
		if fail != nil {
			return fail
		}
		orgs := projectOrgs(payload)
		return present(orgs, summarizeOrgs(orgs))
	})
}

// projectOrgs keeps only the orgs list from a /v1/me body, so list_orgs does
// not repeat the profile and projects that list_projects returns. A body
// without an orgs array (an API older than v1.24) becomes an empty list.
func projectOrgs(payload json.RawMessage) json.RawMessage {
	var body struct {
		Orgs []json.RawMessage `json:"orgs"`
	}
	_ = json.Unmarshal(payload, &body)
	if body.Orgs == nil {
		body.Orgs = []json.RawMessage{}
	}
	out, err := json.Marshal(body)
	if err != nil {
		return json.RawMessage(`{"orgs":[]}`)
	}
	return out
}
