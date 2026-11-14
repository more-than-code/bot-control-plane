package tools

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

const listProjectsSchema = `{"type":"object","additionalProperties":false}`

func registerListProjects(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name:        "list_projects",
		Description: "List the projects the signed-in user can reach, with the organization each belongs to. Reads the caller's profile, organizations (id, name, slug, role) and projects (id, name, slug, role, org_id, via) from stored API data. via is member for a project membership, or org when the caller reaches the project only as an owner or admin of its organization (role is then admin). Does not contact any host.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(listProjectsSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct{}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		payload, fail := get(ctx, api, "/v1/me", nil)
		if fail != nil {
			return fail
		}
		return present(payload, summarizeProjects(payload))
	})
}
