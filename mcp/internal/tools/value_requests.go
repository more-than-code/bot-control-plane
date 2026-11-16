package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

// Value requests (api-v1 v1.28, spec-env-request-value §3.4). The agent
// names a key; the person who asked enters the value on the console page.
// No value ever passes through these tools.

// valueRule starts both tool descriptions (spec §3.4).
const valueRule = "Secret values are never sent through this tool or shown to you."

// envSetPattern is the env set name rule; envKeyPattern (env_sets.go) is the key rule.
var envSetPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

// valueRequestKind is what the agent sees of value requests (requests.go).
var valueRequestKind = requestKind{
	rule:      valueRule,
	title:     "Value request",
	page:      "/fill/",
	path:      "/v1/value-requests/",
	done:      "filled",
	doneText:  " is filled: the value is set, and you will not see it.",
	expired:   " has expired without a value.",
	thing:     "value",
	checkTool: "get_env_value_request",
}

const requestEnvValueSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id", "set", "key"],
  "properties": {
    "project_id": {"type": "string", "description": "Project UUID."},
    "set": {"type": "string", "description": "Env set name. The set must exist."},
    "key": {"type": "string", "description": "Env key, for example STRIPE_API_KEY. It must be new to the set or a placeholder that has no value yet."},
    "reason": {"type": "string", "maxLength": 200, "description": "Optional short note shown to the person on the fill page, as plain text. At most 200 characters, no line breaks. Never put a value here."}
  }
}`

const getEnvValueRequestSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["request_id"],
  "properties": {
    "request_id": {"type": "string", "description": "The request_id that request_env_value returned."}
  }
}`

func registerValueRequests(server *mcp.Server, api *apiclient.Client) {
	closedWorld := false
	addElicitingTool(server, &mcp.Tool{
		Name: "request_env_value",
		Description: valueRule + " Ask the signed-in person to enter one env value in the Deploybot console. " +
			"Deploybot makes a one-time link, valid for 15 minutes, that only that person can use; they type or paste the value " +
			"(or let Deploybot generate one) in their own browser, and it is stored encrypted as a secret. You get the link, never the value. " +
			"The result always has the link as text; show it to the person. A client on MCP 2026-07-28 that supports URL elicitation also offers the link to the person directly before the result. " +
			"Needs owner or admin on the project. The key must be new to the set or a placeholder without a value; a key that already has a value is changed in the console. " +
			"Asking again for the same key replaces your earlier link, which then stops working. " +
			"Check the outcome with get_env_value_request. Does not contact any host.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closedWorld},
		InputSchema: json.RawMessage(requestEnvValueSchema),
	}, func(ctx context.Context, call requestCall) *mcp.CallToolResult {
		return requestEnvValue(ctx, api, call)
	})

	addTool(server, &mcp.Tool{
		Name: "get_env_value_request",
		Description: valueRule + " Read the state of a value request you made with request_env_value: pending, filled, declined or expired. " +
			"Only the person who asked can read it. Reads stored API data. Does not contact any host.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(getEnvValueRequestSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			RequestID string `json:"request_id"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if !confirmationIDPattern.MatchString(in.RequestID) {
			return badRequest(errors.New("request_id must be the 43-character id from request_env_value"))
		}
		payload, fail := get(ctx, api, valueRequestKind.readPath(in.RequestID), nil)
		if fail != nil {
			return fail
		}
		view, err := projectValueRequest(payload)
		if err != nil {
			return toolError("error", errStatusShape.Error())
		}
		return present(view.raw, view.summary())
	})
}

type requestEnvValueArgs struct {
	ProjectID string  `json:"project_id"`
	Set       string  `json:"set"`
	Key       string  `json:"key"`
	Reason    *string `json:"reason"`
}

func (a requestEnvValueArgs) validate() error {
	if err := requireUUID("project_id", a.ProjectID); err != nil {
		return err
	}
	if !envSetPattern.MatchString(a.Set) {
		return errors.New("set must be an env set name (lowercase letters, digits and dashes)")
	}
	if !envKeyPattern.MatchString(a.Key) {
		return errors.New("key must be an env key (A-Z, 0-9 and _, not starting with a digit)")
	}
	if a.Reason != nil && !reasonOK(*a.Reason) {
		return errors.New("reason must be at most 200 characters of plain text")
	}
	return nil
}

// subject is how the summaries name the requested key.
func (a requestEnvValueArgs) subject() string { return a.Set + "." + a.Key }

func requestEnvValue(ctx context.Context, api *apiclient.Client, call requestCall) *mcp.CallToolResult {
	var in requestEnvValueArgs
	if err := decodeArgs(call.args, &in); err != nil {
		return badRequest(err)
	}
	if err := in.validate(); err != nil {
		return badRequest(err)
	}
	if call.state != "" {
		return requestEnvValueRetry(ctx, api, in, call)
	}
	body := map[string]any{"key": in.Key}
	if in.Reason != nil {
		body["reason"] = *in.Reason
	}
	payload, fail := post(ctx, api, idPath("v1", "projects", in.ProjectID, "env-sets", in.Set, "value-requests"), body)
	if fail != nil {
		return keepFixedMessage(fail, "conflict", in.Key+" already has a value; edit it in the console")
	}
	created, ok := valueRequestKind.parseCreated(payload)
	if !ok {
		return toolError("error", errStatusShape.Error())
	}
	if call.wantsURLInput() {
		return valueRequestKind.inputRequired(created, fillMessage(in))
	}
	return valueRequestKind.result(created, in.subject(), "", elicitNotSupported)
}

// requestEnvValueRetry answers the retry after the URL elicitation. It reads
// the request's state and never creates a second request; the set and key
// must match.
func requestEnvValueRetry(ctx context.Context, api *apiclient.Client, in requestEnvValueArgs, call requestCall) *mcp.CallToolResult {
	created, ok := valueRequestKind.decodeState(call.state)
	if !ok {
		return badRequest(errors.New("requestState is not one this tool returned"))
	}
	payload, fail := get(ctx, api, valueRequestKind.readPath(created.RequestID), nil)
	if fail != nil {
		return fail
	}
	view, err := projectValueRequest(payload)
	if err != nil {
		return toolError("error", errStatusShape.Error())
	}
	if view.set != in.Set || view.key != in.Key {
		return badRequest(errors.New("requestState belongs to another set or key"))
	}
	created.ExpiresAt = view.expiresAt
	return valueRequestKind.result(created, in.subject(), view.state, elicitOutcome(call.responses[fillInputID]))
}

func fillMessage(in requestEnvValueArgs) string {
	return fmt.Sprintf("Deploybot needs a value for %s.%s. Open this link in your browser to enter it; the agent never sees the value.", in.Set, in.Key)
}

type valueRequestView struct {
	raw       json.RawMessage
	set       string
	key       string
	state     string
	expiresAt string
}

// projectValueRequest allowlists the ValueRequest object (api-v1 v1.28 item 1).
func projectValueRequest(payload json.RawMessage) (valueRequestView, error) {
	var body struct {
		RequestID string `json:"request_id"`
		ProjectID string `json:"project_id"`
		Set       string `json:"set"`
		Key       string `json:"key"`
		State     string `json:"state"`
		ExpiresAt string `json:"expires_at"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(payload, &body); err != nil || !valueRequestKind.stateOK(body.State) ||
		!envSetPattern.MatchString(body.Set) || !envKeyPattern.MatchString(body.Key) || !uuidPattern.MatchString(body.ProjectID) {
		return valueRequestView{}, errStatusShape
	}
	out, err := json.Marshal(map[string]any{
		"request_id": publicString(body.RequestID),
		"project_id": body.ProjectID,
		"set":        body.Set,
		"key":        body.Key,
		"state":      body.State,
		"expires_at": publicString(body.ExpiresAt),
		"created_at": publicString(body.CreatedAt),
	})
	if err != nil {
		return valueRequestView{}, errStatusShape
	}
	return valueRequestView{raw: out, set: body.Set, key: body.Key, state: body.State, expiresAt: body.ExpiresAt}, nil
}

func (v valueRequestView) summary() string {
	switch v.state {
	case "filled":
		return fmt.Sprintf("%s.%s: filled. The value is set; you will not see it.", v.set, v.key)
	case "declined":
		return fmt.Sprintf("%s.%s: declined by the person.", v.set, v.key)
	case "expired":
		return fmt.Sprintf("%s.%s: expired without a value. Ask again with request_env_value if it is still needed.", v.set, v.key)
	default:
		return fmt.Sprintf("%s.%s: pending. The person has not entered the value yet.", v.set, v.key)
	}
}
