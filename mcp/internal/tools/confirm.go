package tools

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

const confirmSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["confirmation_id", "idempotency_key"],
  "properties": {
    "confirmation_id": {"type": "string", "pattern": "^[A-Za-z0-9_-]{43}$"},
    "idempotency_key": {"type": "string", "pattern": "^[A-Za-z0-9._:-]{1,128}$"},
    "project_id": {"type": "string", "format": "uuid"},
    "entry": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,47}$"}
  }
}`

func registerConfirm(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name:        "confirm",
		Description: "Perform a pending catalog run, stop, restart, deploy, or rollback, or replace health checks, or create, replace, or delete a catalog entry, or create, update, or delete an env set, after the user agrees. The action is real and is audited. This contacts the host now for a program or an image deploy or rollback. Pass the same idempotency_key as the run, stop, restart, deploy_entry, rollback_entry, set_health_checks, create_catalog_entry, update_catalog_entry, delete_catalog_entry, create_env_set, update_env_set, or delete_env_set call that returned this confirmation; a different key is refused and nothing runs. A program confirm with project_id and entry waits up to 30 seconds. A deploy or rollback confirm waits up to 50 seconds, with or without project_id and entry. Pass project_id and entry when you have them for a run, stop, restart, deploy, or rollback: for a program they keep the shorter wait. For any other plan leave entry out; project_id alone is ignored. Returns the program status, or for an image the outcome, short digest, exit code, and last log lines. Right after a program run or restart, its ports and started_at can still be empty; they appear in entry_status after the host's next status report, a few seconds later. A health-check confirm reports how many checks were set. A catalog-entry confirm does not contact the host. It returns the entry name and image reference without env values, or says the entry was deleted. A deploy or rollback still going after 50 seconds keeps running on the host: the result is \"still running on the host\"; check entry_status for the outcome.",
		Annotations: mutatingAnnotations(true),
		InputSchema: json.RawMessage(confirmSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			ConfirmationID string `json:"confirmation_id"`
			IdempotencyKey string `json:"idempotency_key"`
			ProjectID      string `json:"project_id"`
			Entry          string `json:"entry"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := requireConfirmationID(in.ConfirmationID); err != nil {
			return badRequest(err)
		}
		if err := requireIdempotencyKey(in.IdempotencyKey); err != nil {
			return badRequest(err)
		}
		// project_id and entry only choose the wait of a program run, stop or
		// restart, or a deploy or rollback. project_id alone (as an agent
		// passes for an env set or catalog-entry plan) is ignored.
		if in.Entry != "" && in.ProjectID == "" {
			return badRequest(errors.New("entry needs project_id: pass both for a program run, stop or restart or an image deploy or rollback, or leave entry out"))
		}
		if in.Entry == "" {
			in.ProjectID = ""
		}
		if in.ProjectID != "" {
			if err := requireUUID("project_id", in.ProjectID); err != nil {
				return badRequest(err)
			}
			if err := requireEntryName(in.Entry); err != nil {
				return badRequest(err)
			}
		}
		path := idPath("v1", "confirmations", in.ConfirmationID)
		body := struct {
			IdempotencyKey string `json:"idempotency_key"`
		}{IdempotencyKey: in.IdempotencyKey}
		// project_id and entry are not sent. They only choose the wait.
		// The API body stays {"idempotency_key"}.
		var (
			status  int
			payload json.RawMessage
			fail    *mcp.CallToolResult
		)
		if in.ProjectID == "" {
			// Without project_id and entry the action is unknown. It may be
			// an image deploy, so it gets the image budget and still running.
			extendWriteBudget(ctx, apiclient.MaxCallTimeout)
			status, payload, fail = postWithin(ctx, api, path, body, apiclient.MaxCallTimeout)
		} else {
			wait := confirmWaitFor(ctx, api, in.ProjectID, in.Entry)
			if wait.deploy {
				extendWriteBudget(ctx, wait.limit)
				status, payload, fail = postWithin(ctx, api, path, body, wait.limit)
			} else {
				status, payload, fail = postStatus(ctx, api, path, body)
			}
		}
		if fail != nil {
			return fail
		}
		return actedOrPending(status, payload)
	})
}
