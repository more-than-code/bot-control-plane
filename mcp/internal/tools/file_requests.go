package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

// File requests (api-v1 v1.30, spec-file-request §3.4). The agent names a
// managed file; the person who asked pastes it or picks it from disk on the
// console page. No file content ever passes through these tools. The link,
// URL input request and retry are those of every request tool (requests.go).

// fileRule starts both tool descriptions (spec §3.4).
const fileRule = "File contents are never sent through this tool or shown to you."

// managedFilePattern is the v1.9 file name rule.
var managedFilePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// fileRequestKind is what the agent sees of file requests (requests.go).
var fileRequestKind = requestKind{
	rule:      fileRule,
	title:     "File request",
	page:      "/fill-file/",
	path:      "/v1/file-requests/",
	done:      "filled",
	doneText:  " is filled: the file is added, and you will not see its content.",
	expired:   " has expired without a file.",
	thing:     "file",
	checkTool: "get_file_request",
}

const requestFileSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id", "name"],
  "properties": {
    "project_id": {"type": "string", "description": "Project UUID."},
    "name": {"type": "string", "description": "Managed file name, for example nginx.conf: lowercase letters, digits, dots, underscores and hyphens, at most 64 characters. The file must not exist yet."},
    "reason": {"type": "string", "maxLength": 200, "description": "Optional short note shown to the person on the page, as plain text. At most 200 characters, no line breaks. Never put file content here."},
    "secret": {"type": "boolean", "description": "Store the file encrypted as a secret (default true). The person may make a plain request secret, never the other way."}
  }
}`

const getFileRequestSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["request_id"],
  "properties": {
    "request_id": {"type": "string", "description": "The request_id that request_file returned."}
  }
}`

func registerFileRequests(server *mcp.Server, api *apiclient.Client) {
	closedWorld := false
	addElicitingTool(server, &mcp.Tool{
		Name: "request_file",
		Description: fileRule + " Ask the signed-in person to add one managed file to a project in the Deploybot console. " +
			"Deploybot makes a one-time link, valid for 15 minutes, that only that person can use; they paste the content or pick a text file " +
			"from their computer in their own browser, and it is stored (encrypted when secret, the default). You get the link, never the content. " +
			"The result always has the link as text, alone on its first line, and the call returns at once; show it to the person. " + severalLinks + " " +
			"Needs owner or admin on the project. The file must not exist yet; an existing file is replaced in the console. Files are UTF-8 text of at most 64 KiB, at most 64 per project. " +
			"Asking again for the same name replaces your earlier link, which then stops working. " +
			"Check the outcome with get_file_request or check_readiness. Does not contact any host.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, OpenWorldHint: &closedWorld},
		InputSchema: json.RawMessage(requestFileSchema),
	}, func(ctx context.Context, call requestCall) *mcp.CallToolResult {
		return requestFile(ctx, api, call)
	})

	addTool(server, &mcp.Tool{
		Name: "get_file_request",
		Description: fileRule + " Read the state of a file request you made with request_file: pending, filled, declined or expired. " +
			"Only the person who asked can read it. Reads stored API data. Does not contact any host.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(getFileRequestSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			RequestID string `json:"request_id"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if !confirmationIDPattern.MatchString(in.RequestID) {
			return badRequest(errors.New("request_id must be the 43-character id from request_file"))
		}
		payload, fail := get(ctx, api, fileRequestKind.readPath(in.RequestID), nil)
		if fail != nil {
			return fail
		}
		view, err := projectFileRequest(payload)
		if err != nil {
			return toolError("error", errStatusShape.Error())
		}
		return present(view.raw, view.summary())
	})
}

type requestFileArgs struct {
	ProjectID string  `json:"project_id"`
	Name      string  `json:"name"`
	Reason    *string `json:"reason"`
	Secret    *bool   `json:"secret"`
}

func (a requestFileArgs) validate() error {
	if err := requireUUID("project_id", a.ProjectID); err != nil {
		return err
	}
	if !managedFilePattern.MatchString(a.Name) {
		return errors.New("name must be a file name (lowercase letters, digits, dots, underscores and hyphens, at most 64 characters)")
	}
	if a.Reason != nil && !reasonOK(*a.Reason) {
		return errors.New("reason must be at most 200 characters of plain text")
	}
	return nil
}

func requestFile(ctx context.Context, api *apiclient.Client, call requestCall) *mcp.CallToolResult {
	var in requestFileArgs
	if err := decodeArgs(call.args, &in); err != nil {
		return badRequest(err)
	}
	if err := in.validate(); err != nil {
		return badRequest(err)
	}
	if call.state != "" {
		return requestFileRetry(ctx, api, in, call)
	}
	body := map[string]any{"name": in.Name}
	if in.Reason != nil {
		body["reason"] = *in.Reason
	}
	if in.Secret != nil {
		body["secret"] = *in.Secret
	}
	payload, fail := post(ctx, api, idPath("v1", "projects", in.ProjectID, "file-requests"), body)
	if fail != nil {
		return keepFixedLinked(fail, "conflict", "file "+in.Name+" already exists; replace it in the console", projectSettingsURL(api, in.ProjectID, "files"))
	}
	created, ok := fileRequestKind.parseCreated(payload)
	if !ok {
		return toolError("error", errStatusShape.Error())
	}
	return fileRequestKind.result(created, in.Name, "", elicitNotSupported)
}

// requestFileRetry answers the retry after the URL elicitation. It reads the
// request's state and never creates a second request; the project and file
// name must match.
func requestFileRetry(ctx context.Context, api *apiclient.Client, in requestFileArgs, call requestCall) *mcp.CallToolResult {
	created, ok := fileRequestKind.decodeState(call.state)
	if !ok {
		return badRequest(errors.New("requestState is not one this tool returned"))
	}
	payload, fail := get(ctx, api, fileRequestKind.readPath(created.RequestID), nil)
	if fail != nil {
		return fail
	}
	view, err := projectFileRequest(payload)
	if err != nil {
		return toolError("error", errStatusShape.Error())
	}
	if !strings.EqualFold(view.projectID, in.ProjectID) || view.name != in.Name {
		return badRequest(errors.New("requestState belongs to another project or file"))
	}
	created.ExpiresAt = view.expiresAt
	return fileRequestKind.result(created, in.Name, view.state, elicitOutcome(call.responses[fillInputID]))
}

type fileRequestView struct {
	raw       json.RawMessage
	projectID string
	name      string
	state     string
	expiresAt string
}

// projectFileRequest allowlists the FileRequest object (api-v1 v1.30 item 1).
func projectFileRequest(payload json.RawMessage) (fileRequestView, error) {
	var body struct {
		RequestID string `json:"request_id"`
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
		Secret    *bool  `json:"secret"`
		State     string `json:"state"`
		ExpiresAt string `json:"expires_at"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(payload, &body); err != nil || !fileRequestKind.stateOK(body.State) || body.Secret == nil ||
		!managedFilePattern.MatchString(body.Name) || !uuidPattern.MatchString(body.ProjectID) {
		return fileRequestView{}, errStatusShape
	}
	out, err := json.Marshal(map[string]any{
		"request_id": publicString(body.RequestID),
		"project_id": body.ProjectID,
		"name":       body.Name,
		"secret":     *body.Secret,
		"state":      body.State,
		"expires_at": publicString(body.ExpiresAt),
		"created_at": publicString(body.CreatedAt),
	})
	if err != nil {
		return fileRequestView{}, errStatusShape
	}
	return fileRequestView{raw: out, projectID: body.ProjectID, name: body.Name, state: body.State, expiresAt: body.ExpiresAt}, nil
}

func (v fileRequestView) summary() string {
	switch v.state {
	case "filled":
		return fmt.Sprintf("%s: filled. The file is added; you will not see its content.", v.name)
	case "declined":
		return fmt.Sprintf("%s: declined by the person.", v.name)
	case "expired":
		return fmt.Sprintf("%s: expired without a file. Ask again with request_file if it is still needed.", v.name)
	default:
		return fmt.Sprintf("%s: pending. The person has not added the file yet.", v.name)
	}
}
