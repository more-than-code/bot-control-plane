package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Helpers shared by the request tools: request_env_value (value_requests.go,
// api-v1 v1.28) and request_file (file_requests.go, v1.30). The agent asks;
// the person who asked answers on a console page. Every client gets the link
// as text; a client on MCP 2026-07-28 that declares elicitation.url first
// gets a URL input request (SEP-2322), and its retry reads the state.

const maxReasonRunes = 200

// newProtocol is the first MCP version with input-required results
// (SEP-2322): the server returns the elicitation and the client retries.
const newProtocol = "2026-07-28"

// fillInputID names the one URL elicitation in an input-required result.
const fillInputID = "open_fill_page"

// Elicitation outcomes in the structured result.
const (
	elicitNotSupported = "not_supported"
	elicitAccepted     = "accepted"
	elicitDeclined     = "declined"
	elicitCancelled    = "cancelled"
)

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

// requestCall is what an eliciting tool reads from one tools/call.
type requestCall struct {
	args      json.RawMessage
	protocol  string
	caps      *mcp.ClientCapabilities
	state     string
	responses mcp.InputResponseMap
}

// wantsURLInput reports a 2026-07-28 client whose capabilities on this
// request declare elicitation.url. Older clients never get an input request:
// on a stateless gateway the SDK could not deliver it to them.
func (c requestCall) wantsURLInput() bool {
	return c.protocol >= newProtocol && c.caps != nil && c.caps.Elicitation != nil && c.caps.Elicitation.URL != nil
}

// addElicitingTool registers a request tool. It uses server.AddTool directly
// (not addTool) because it reads the protocol version, the per-request
// client capabilities and a retry's requestState and inputResponses.
func addElicitingTool(server *mcp.Server, tool *mcp.Tool, h func(context.Context, requestCall) *mcp.CallToolResult) {
	server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (res *mcp.CallToolResult, err error) {
		defer func() {
			if recover() != nil {
				// The panic value can contain arguments. Log only the tool name.
				slog.Default().Error("tool panicked", "tool", tool.Name)
				res, err = toolError("error", "internal error"), nil
			}
		}()
		var call requestCall
		if req != nil {
			call.protocol = req.ProtocolVersion()
			call.caps = req.ClientCapabilities()
			if req.Params != nil {
				call.args = req.Params.Arguments
				call.state = req.Params.RequestState
				call.responses = req.Params.InputResponses
			}
		}
		return h(ctx, call), nil
	})
}

// createdRequest is the API 201 body of a create.
type createdRequest struct {
	RequestID string `json:"request_id"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
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

// requestState is the requestState of an input-required result. It names
// only the request id and its link, which the agent sees anyway, so it is
// not signed; the API answers only the person who asked.
type requestState struct {
	RequestID string `json:"r"`
	URL       string `json:"u"`
}

func encodeRequestState(created createdRequest) string {
	raw, _ := json.Marshal(requestState{RequestID: created.RequestID, URL: created.URL})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// pageURLOK checks an API url: http(s), ending in <page><request_id>, with
// no query, fragment or user info.
func pageURLOK(raw, page, id string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return false
	}
	return u.RawQuery == "" && u.Fragment == "" && strings.HasSuffix(u.Path, page+id)
}

// keepFixedMessage undoes the generic redaction for a fixed API create
// text that names only a key or file name (fixed), which the redaction
// can mangle (it reads "KEY already has a value" as a KEY=value pair, and
// a name such as credentials.json as a secret). Any other error is
// returned as it is.
func keepFixedMessage(fail *mcp.CallToolResult, code, fixed string) *mcp.CallToolResult {
	if len(fail.Content) != 1 {
		return fail
	}
	text, ok := fail.Content[0].(*mcp.TextContent)
	if !ok {
		return fail
	}
	if text.Text == code+": "+redactText(fixed) {
		return toolError(code, fixed)
	}
	return fail
}

// requestKind is what differs between request types in what the agent sees.
type requestKind struct {
	rule      string // the first sentence of the descriptions and of every summary
	title     string // "Value request"
	page      string // the console page the url ends in, before the id
	path      string // the API read path, before the id
	done      string // the done state; pending, declined and expired are shared
	doneText  string // the done sentence of the summary
	expired   string // the expired sentence of the summary
	thing     string // what the person adds: "value", "file"
	checkTool string // the tool that reads the state
}

// stateOK allowlists a state of this request type.
func (k requestKind) stateOK(state string) bool {
	return state == "pending" || state == "declined" || state == "expired" || state == k.done
}

// parseCreated checks the API 201 body: the id pattern and the url of this page.
func (k requestKind) parseCreated(payload json.RawMessage) (createdRequest, bool) {
	var created createdRequest
	if err := json.Unmarshal(payload, &created); err != nil || !confirmationIDPattern.MatchString(created.RequestID) || !pageURLOK(created.URL, k.page, created.RequestID) {
		return createdRequest{}, false
	}
	return created, true
}

// decodeState reads a requestState this tool returned.
func (k requestKind) decodeState(s string) (createdRequest, bool) {
	if len(s) > 2048 {
		return createdRequest{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return createdRequest{}, false
	}
	var st requestState
	if err := json.Unmarshal(raw, &st); err != nil || !confirmationIDPattern.MatchString(st.RequestID) || !pageURLOK(st.URL, k.page, st.RequestID) {
		return createdRequest{}, false
	}
	return createdRequest{RequestID: st.RequestID, URL: st.URL}, true
}

// readPath is the API read of one request.
func (k requestKind) readPath(id string) string {
	return k.path + url.PathEscape(id)
}

// inputRequired is the first answer to a client that wants URL input: one
// URL elicitation and the state for its retry. It carries no content (the
// SDK refuses a result that has both): the link text comes with the retry.
func (k requestKind) inputRequired(created createdRequest, message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{fillInputID: &mcp.ElicitParams{
			Mode:          "url",
			Message:       message,
			URL:           created.URL,
			ElicitationID: created.RequestID,
		}},
		RequestState: encodeRequestState(created),
	}
}

// result is the result every client ends with: the link as text. state is
// empty on the first call and the request's state on a retry.
func (k requestKind) result(created createdRequest, subject, state, elicitation string) *mcp.CallToolResult {
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
	return present(encoded, k.summary(created, subject, state, elicitation))
}

func (k requestKind) summary(created createdRequest, subject, state, elicitation string) string {
	var b strings.Builder
	b.WriteString(k.rule)
	fmt.Fprintf(&b, " %s %s for %s", k.title, created.RequestID, subject)
	open := state == "" || state == "pending"
	switch {
	case open:
		fmt.Fprintf(&b, " is pending; it can be filled until %s.", summaryField(created.ExpiresAt))
	case state == k.done:
		b.WriteString(k.doneText)
	case state == "declined":
		b.WriteString(" was declined by the person.")
	default:
		b.WriteString(k.expired)
	}
	switch elicitation {
	case elicitAccepted:
		if open {
			fmt.Fprintf(&b, " The person agreed to open the link; that does not mean the %s is saved yet.", k.thing)
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
	fmt.Fprintf(&b, "\nCheck the outcome with %s.", k.checkTool)
	return b.String()
}
