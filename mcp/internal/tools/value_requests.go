package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

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

const maxReasonRunes = 200

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
	server.AddTool(&mcp.Tool{
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
	}, func(ctx context.Context, req *mcp.CallToolRequest) (res *mcp.CallToolResult, err error) {
		defer func() {
			if recover() != nil {
				slog.Default().Error("tool panicked", "tool", "request_env_value")
				res, err = toolError("error", "internal error"), nil
			}
		}()
		var call vrCall
		if req != nil {
			call.protocol = req.ProtocolVersion()
			call.caps = req.ClientCapabilities()
			if req.Params != nil {
				call.args = req.Params.Arguments
				call.state = req.Params.RequestState
				call.responses = req.Params.InputResponses
			}
		}
		return requestEnvValue(ctx, api, call), nil
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
		payload, fail := get(ctx, api, "/v1/value-requests/"+url.PathEscape(in.RequestID), nil)
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

func reasonOK(s string) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxReasonRunes {
		return false
	}
	for _, r := range s {
		// Control, format (bidi overrides, zero-width) and line or
		// paragraph separators: the note must read as it is stored.
		if unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			return false
		}
	}
	return true
}

// createdRequest is the API 201 body.
type createdRequest struct {
	RequestID string `json:"request_id"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

// vrCall is what request_env_value reads from one tools/call.
type vrCall struct {
	args      json.RawMessage
	protocol  string
	caps      *mcp.ClientCapabilities
	state     string
	responses mcp.InputResponseMap
}

// newProtocol is the first MCP version with input-required results
// (SEP-2322): the server returns the elicitation and the client retries.
const newProtocol = "2026-07-28"

// fillInputID names the one URL elicitation in an input-required result.
const fillInputID = "open_fill_page"

// wantsURLInput reports a 2026-07-28 client whose capabilities on this
// request declare elicitation.url. Older clients never get an input request:
// on a stateless gateway the SDK could not deliver it to them.
func (c vrCall) wantsURLInput() bool {
	return c.protocol >= newProtocol && c.caps != nil && c.caps.Elicitation != nil && c.caps.Elicitation.URL != nil
}

func requestEnvValue(ctx context.Context, api *apiclient.Client, call vrCall) *mcp.CallToolResult {
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
		return keepFixedMessage(fail, in.Key)
	}
	var created createdRequest
	if err := json.Unmarshal(payload, &created); err != nil || !confirmationIDPattern.MatchString(created.RequestID) || !fillURLOK(created.URL, created.RequestID) {
		return toolError("error", errStatusShape.Error())
	}
	if call.wantsURLInput() {
		// An input-required result carries no content: the link text comes
		// with the result of the client's retry.
		return &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{fillInputID: &mcp.ElicitParams{
				Mode:          "url",
				Message:       fillMessage(in),
				URL:           created.URL,
				ElicitationID: created.RequestID,
			}},
			RequestState: encodeVRState(created),
		}
	}
	return requestResult(in, created, "", elicitNotSupported)
}

// requestEnvValueRetry answers the retry after the URL elicitation. It reads
// the request's state and never creates a second request. The state names
// only the request id and its link, which the agent sees anyway; the API
// answers only the person who asked, and the set and key must match.
func requestEnvValueRetry(ctx context.Context, api *apiclient.Client, in requestEnvValueArgs, call vrCall) *mcp.CallToolResult {
	created, ok := decodeVRState(call.state)
	if !ok {
		return badRequest(errors.New("requestState is not one this tool returned"))
	}
	payload, fail := get(ctx, api, "/v1/value-requests/"+url.PathEscape(created.RequestID), nil)
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
	return requestResult(in, created, view.state, elicitOutcome(call.responses[fillInputID]))
}

func elicitOutcome(resp mcp.InputResponse) string {
	res, ok := resp.(*mcp.ElicitResult)
	if !ok || res == nil {
		return elicitCancelled
	}
	switch res.Action {
	case "accept":
		return elicitAccepted
	case "decline":
		return elicitDeclined
	default:
		return elicitCancelled
	}
}

func fillMessage(in requestEnvValueArgs) string {
	return fmt.Sprintf("Deploybot needs a value for %s.%s. Open this link in your browser to enter it; the agent never sees the value.", in.Set, in.Key)
}

// requestResult is the result every client ends with: the link as text.
// state is empty on the first call and the request's state on a retry.
func requestResult(in requestEnvValueArgs, created createdRequest, state, elicitation string) *mcp.CallToolResult {
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
	return present(encoded, requestSummary(in, created, state, elicitation))
}

// vrState is the requestState of an input-required result.
type vrState struct {
	RequestID string `json:"r"`
	URL       string `json:"u"`
}

func encodeVRState(created createdRequest) string {
	raw, _ := json.Marshal(vrState{RequestID: created.RequestID, URL: created.URL})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeVRState(s string) (createdRequest, bool) {
	if len(s) > 2048 {
		return createdRequest{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return createdRequest{}, false
	}
	var st vrState
	if err := json.Unmarshal(raw, &st); err != nil || !confirmationIDPattern.MatchString(st.RequestID) || !fillURLOK(st.URL, st.RequestID) {
		return createdRequest{}, false
	}
	return createdRequest{RequestID: st.RequestID, URL: st.URL}, true
}

// keepFixedMessage undoes the generic redaction for the api-v1 v1.28 create
// errors. They hold the key name and fixed text only, and the redaction
// would read "KEY already has a value" as a KEY=value pair. Any other
// error is returned as it is.
func keepFixedMessage(fail *mcp.CallToolResult, key string) *mcp.CallToolResult {
	fixed := map[string]string{
		"conflict: " + key + "=[REDACTED] has a value; edit it in the console": "conflict: " + key + " already has a value; edit it in the console",
	}
	if len(fail.Content) != 1 {
		return fail
	}
	text, ok := fail.Content[0].(*mcp.TextContent)
	if !ok {
		return fail
	}
	if plain, ok := fixed[text.Text]; ok {
		return toolError("conflict", strings.TrimPrefix(plain, "conflict: "))
	}
	return fail
}

// fillURLOK checks the API url: http(s), ending in /fill/<request_id>, with
// no query, fragment or user info.
func fillURLOK(raw, id string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return false
	}
	return u.RawQuery == "" && u.Fragment == "" && strings.HasSuffix(u.Path, "/fill/"+id)
}

// Elicitation outcomes in the structured result.
const (
	elicitNotSupported = "not_supported"
	elicitAccepted     = "accepted"
	elicitDeclined     = "declined"
	elicitCancelled    = "cancelled"
)

func requestSummary(in requestEnvValueArgs, created createdRequest, state, elicitation string) string {
	var b strings.Builder
	b.WriteString(valueRule)
	fmt.Fprintf(&b, " Value request %s for %s.%s", created.RequestID, in.Set, in.Key)
	open := state == "" || state == "pending"
	switch state {
	case "", "pending":
		fmt.Fprintf(&b, " is pending; it can be filled until %s.", summaryField(created.ExpiresAt))
	case "filled":
		b.WriteString(" is filled: the value is set, and you will not see it.")
	case "declined":
		b.WriteString(" was declined by the person.")
	default:
		b.WriteString(" has expired without a value.")
	}
	switch elicitation {
	case elicitAccepted:
		if open {
			b.WriteString(" The person agreed to open the link; that does not mean the value is saved yet.")
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
		b.WriteString(" Show the person this link and ask them to open it in their browser, signed in to Deploybot as themselves.")
	}
	b.WriteString("\nLink: ")
	b.WriteString(created.URL)
	b.WriteString("\nCheck the outcome with get_env_value_request.")
	return b.String()
}

type valueRequestView struct {
	raw       json.RawMessage
	set       string
	key       string
	state     string
	expiresAt string
}

var valueRequestStates = map[string]bool{"pending": true, "filled": true, "declined": true, "expired": true}

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
	if err := json.Unmarshal(payload, &body); err != nil || !valueRequestStates[body.State] ||
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
