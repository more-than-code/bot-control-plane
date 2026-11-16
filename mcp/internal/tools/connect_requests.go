package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

// Connect requests (api-v1 v1.31, spec-settings-automatic §3.6). The agent
// asks; the person who asked approves Connect GitHub in the console and on
// github.com. No token ever passes through these tools.

// connectRule starts both tool descriptions.
const connectRule = "No registry token is ever sent through this tool or shown to you."

// connectRegistryHost is the only registry with a connect flow.
const connectRegistryHost = "ghcr.io"

const connectRegistrySchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id", "registry"],
  "properties": {
    "project_id": {"type": "string", "description": "Project UUID."},
    "registry": {"type": "string", "enum": ["ghcr.io"], "description": "The registry to connect. Only ghcr.io (GitHub) for now."},
    "reason": {"type": "string", "maxLength": 200, "description": "Optional short note shown to the person on the connect page, as plain text. At most 200 characters, no line breaks."}
  }
}`

const getRegistryConnectRequestSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["request_id"],
  "properties": {
    "request_id": {"type": "string", "description": "The request_id that connect_registry returned."}
  }
}`

// connectRequestKind is what the shared request helpers need of connect
// requests (requests.go). The summary text is connectSummary: a connect is
// approved, not filled.
var connectRequestKind = requestKind{
	rule:      connectRule,
	title:     "Connect request",
	page:      "/connect/",
	path:      "/v1/registry-connect-requests/",
	done:      "connected",
	checkTool: "get_registry_connect_request",
}

func registerConnectRequests(server *mcp.Server, api *apiclient.Client) {
	closedWorld := false
	addElicitingTool(server, &mcp.Tool{
		Name: "connect_registry",
		Description: connectRule + " Ask the signed-in person to connect GitHub for a project, so Deploybot can pull private ghcr.io images. " +
			"Deploybot makes a one-time link, valid for 15 minutes, that only that person can use; they open it in their own browser, " +
			"continue to github.com and approve read:packages there. The login is stored encrypted for the project and replaces any ghcr.io login it has. " +
			"The result always has the link as text; show it to the person. A client on MCP 2026-07-28 that supports URL elicitation also offers the link to the person directly before the result. " +
			"Needs owner or admin on the project, and Connect GitHub configured on the server. Asking again for the same project replaces your earlier link, which then stops working. " +
			"Check the outcome with get_registry_connect_request. Does not contact any host.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closedWorld},
		InputSchema: json.RawMessage(connectRegistrySchema),
	}, func(ctx context.Context, call requestCall) *mcp.CallToolResult {
		return connectRegistry(ctx, api, call)
	})

	addTool(server, &mcp.Tool{
		Name: "get_registry_connect_request",
		Description: connectRule + " Read the state of a connect request you made with connect_registry: pending, connected, declined or expired. " +
			"Only the person who asked can read it. Reads stored API data. Does not contact any host.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(getRegistryConnectRequestSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			RequestID string `json:"request_id"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if !confirmationIDPattern.MatchString(in.RequestID) {
			return badRequest(errors.New("request_id must be the 43-character id from connect_registry"))
		}
		payload, fail := get(ctx, api, connectRequestKind.readPath(in.RequestID), nil)
		if fail != nil {
			return fail
		}
		view, err := projectConnectRequest(payload)
		if err != nil {
			return toolError("error", errStatusShape.Error())
		}
		return present(view.raw, view.summary())
	})
}

type connectRegistryArgs struct {
	ProjectID string  `json:"project_id"`
	Registry  string  `json:"registry"`
	Reason    *string `json:"reason"`
}

func (a connectRegistryArgs) validate() error {
	if err := requireUUID("project_id", a.ProjectID); err != nil {
		return err
	}
	if a.Registry != connectRegistryHost {
		return errors.New("registry must be ghcr.io; other registries need a login added in the console")
	}
	if a.Reason != nil && !reasonOK(*a.Reason) {
		return errors.New("reason must be at most 200 characters of plain text")
	}
	return nil
}

func connectRegistry(ctx context.Context, api *apiclient.Client, call requestCall) *mcp.CallToolResult {
	var in connectRegistryArgs
	if err := decodeArgs(call.args, &in); err != nil {
		return badRequest(err)
	}
	if err := in.validate(); err != nil {
		return badRequest(err)
	}
	if call.state != "" {
		return connectRegistryRetry(ctx, api, in, call)
	}
	body := map[string]any{}
	if in.Reason != nil {
		body["reason"] = *in.Reason
	}
	payload, fail := post(ctx, api, idPath("v1", "projects", in.ProjectID, "registries", "github", "requests"), body)
	if fail != nil {
		return fail
	}
	created, ok := connectRequestKind.parseCreated(payload)
	if !ok {
		return toolError("error", errStatusShape.Error())
	}
	if call.wantsURLInput() {
		return connectRequestKind.inputRequired(created, "Deploybot asks you to connect GitHub for this project so it can pull private ghcr.io images. Open this link in your browser; the agent never sees the token.")
	}
	return connectResult(created, "", elicitNotSupported)
}

// connectRegistryRetry answers the retry after the URL elicitation. It reads
// the request's state and never creates a second request. The API answers
// only the person who asked, and the project must match.
func connectRegistryRetry(ctx context.Context, api *apiclient.Client, in connectRegistryArgs, call requestCall) *mcp.CallToolResult {
	created, ok := connectRequestKind.decodeState(call.state)
	if !ok {
		return badRequest(errors.New("requestState is not one this tool returned"))
	}
	payload, fail := get(ctx, api, connectRequestKind.readPath(created.RequestID), nil)
	if fail != nil {
		return fail
	}
	view, err := projectConnectRequest(payload)
	if err != nil {
		return toolError("error", errStatusShape.Error())
	}
	if view.projectID != in.ProjectID {
		return badRequest(errors.New("requestState belongs to another project"))
	}
	created.ExpiresAt = view.expiresAt
	return connectResult(created, view.state, elicitOutcome(call.responses[fillInputID]))
}

// connectResult is the result every client ends with: the link as text.
func connectResult(created createdRequest, state, elicitation string) *mcp.CallToolResult {
	out := map[string]any{
		"request_id":  created.RequestID,
		"url":         created.URL,
		"expires_at":  publicString(created.ExpiresAt),
		"elicitation": elicitation,
	}
	if state != "" {
		out["state"] = state
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return toolError("error", "internal error")
	}
	return present(encoded, connectSummary(created, state, elicitation))
}

func connectSummary(created createdRequest, state, elicitation string) string {
	var b strings.Builder
	b.WriteString(connectRule)
	fmt.Fprintf(&b, " Connect request %s for ghcr.io", created.RequestID)
	open := state == "" || state == "pending"
	switch state {
	case "", "pending":
		fmt.Fprintf(&b, " is pending; it can be used until %s.", summaryField(created.ExpiresAt))
	case "connected":
		b.WriteString(" is connected: the project now pulls ghcr.io images with the GitHub login.")
	case "declined":
		b.WriteString(" was declined by the person.")
	default:
		b.WriteString(" has expired without a connection.")
	}
	switch elicitation {
	case elicitAccepted:
		if open {
			b.WriteString(" The person agreed to open the link; that does not mean GitHub is connected yet.")
		}
	case elicitDeclined:
		b.WriteString(" The person declined to open the link.")
		if open {
			b.WriteString(" The request stays open until it expires, so the link below still works.")
		}
	case elicitCancelled:
		b.WriteString(" The person dismissed the link.")
		if open {
			b.WriteString(" The request stays open until it expires, so the link below still works.")
		}
	default:
		b.WriteString(" Show the person this link and ask them to open it in their browser, signed in to Deploybot as themselves, and approve on github.com.")
	}
	b.WriteString("\nLink: ")
	b.WriteString(created.URL)
	b.WriteString("\nCheck the outcome with get_registry_connect_request.")
	return b.String()
}

// connectEndedHint follows an ended request: the person may have approved
// on GitHub after it ended, and that connect still applies (api-v1 v1.31 item 6).
const connectEndedHint = "If the person finished on GitHub anyway, check_readiness shows whether ghcr.io is now connected."

type connectRequestView struct {
	raw       json.RawMessage
	projectID string
	state     string
	expiresAt string
}

// projectConnectRequest allowlists the ConnectRequest object (api-v1 v1.31 item 1).
func projectConnectRequest(payload json.RawMessage) (connectRequestView, error) {
	var body struct {
		RequestID string `json:"request_id"`
		ProjectID string `json:"project_id"`
		Registry  string `json:"registry"`
		State     string `json:"state"`
		ExpiresAt string `json:"expires_at"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(payload, &body); err != nil || !connectRequestKind.stateOK(body.State) ||
		body.Registry != connectRegistryHost || !uuidPattern.MatchString(body.ProjectID) {
		return connectRequestView{}, errStatusShape
	}
	out, err := json.Marshal(map[string]any{
		"request_id": publicString(body.RequestID),
		"project_id": body.ProjectID,
		"registry":   body.Registry,
		"state":      body.State,
		"expires_at": publicString(body.ExpiresAt),
		"created_at": publicString(body.CreatedAt),
	})
	if err != nil {
		return connectRequestView{}, errStatusShape
	}
	return connectRequestView{raw: out, projectID: body.ProjectID, state: body.State, expiresAt: body.ExpiresAt}, nil
}

func (v connectRequestView) summary() string {
	switch v.state {
	case "connected":
		return "ghcr.io: connected. The project pulls ghcr.io images with the GitHub login; you will not see the token."
	case "declined":
		return "ghcr.io: declined by the person.\n" + connectEndedHint
	case "expired":
		return "ghcr.io: expired without a connection. Ask again with connect_registry if it is still needed.\n" + connectEndedHint
	default:
		return "ghcr.io: pending. The person has not connected GitHub yet."
	}
}
