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
		orgs, found, err := projectOrgs(payload)
		if err != nil {
			return toolError("error", errStatusShape.Error())
		}
		if !found {
			return present(orgs, "0 orgs: this API predates organizations")
		}
		return present(orgs, summarizeOrgs(orgs))
	})
}

// projectOrgs keeps only the orgs list from a /v1/me body, so list_orgs does
// not repeat the profile and projects that list_projects returns.
// found is false when the body has no orgs key (an API older than v1.24);
// the list is then empty. A body that is not an object, or whose orgs is not
// an array of objects, is an error and is not shown.
func projectOrgs(payload json.RawMessage) (out json.RawMessage, found bool, err error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(payload, &body); err != nil || body == nil {
		return nil, false, errStatusShape
	}
	raw, found := body["orgs"]
	if !found {
		return json.RawMessage(`{"orgs":[]}`), false, nil
	}
	var orgs []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &orgs); err != nil || orgs == nil {
		return nil, true, errStatusShape
	}
	for _, o := range orgs {
		if o == nil {
			return nil, true, errStatusShape
		}
	}
	out, err = json.Marshal(struct {
		Orgs json.RawMessage `json:"orgs"`
	}{raw})
	if err != nil {
		return nil, true, errStatusShape
	}
	return out, true, nil
}
