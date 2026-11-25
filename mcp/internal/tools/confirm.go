package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

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
		Description: "Perform a pending catalog run, stop, restart, deploy, or rollback, or replace health checks, or create, replace, or delete a catalog entry, or create, update, or delete an env set, or replace an entry's routes, or upgrade a host's bot-agent (upgrade_agent), or set a host's alert settings, after the user agrees. The action is real and is audited. This contacts the host now for a program or an image deploy or rollback. Pass the same idempotency_key as the run, stop, restart, deploy_entry, rollback_entry, upgrade_agent, set_health_checks, create_catalog_entry, update_catalog_entry, delete_catalog_entry, create_env_set, update_env_set, delete_env_set, set_routes, or set_alert_settings call that returned this confirmation; a different key is refused and nothing runs. A program confirm with project_id and entry waits up to 30 seconds. A deploy or rollback confirm waits up to 50 seconds, with or without project_id and entry. Pass project_id and entry when you have them for a run, stop, restart, deploy, or rollback: for a program they keep the shorter wait. For any other plan leave entry out; project_id alone is ignored. Returns the program status, or for an image the outcome, short digest, exit code, and last log lines. Right after a program run or restart, its ports and started_at can still be empty; they appear in entry_status after the host's next status report, a few seconds later. A health-check confirm reports how many checks were set. A catalog-entry or alert-settings confirm does not contact the host; an alert-settings confirm returns the stored settings (that plan is kept by this server for 10 minutes and is lost if it restarts). A routes confirm returns the entry's routes with their state; the API then updates the host's edge. It returns the entry name and image reference without env values, or says the entry was deleted. A deploy or rollback still going after 50 seconds keeps running on the host: the result is \"still running on the host\" with the time it ends by at the latest when project_id and entry were passed; the entry's deploy history (deploy_entry for that host) then shows the outcome. When the host refuses a deploy or rollback before running it (for example bad_spec, which names the field that failed), the plan is kept: fix the cause, then call confirm again with the same confirmation_id and idempotency_key until the time the message gives; a change to the entry needs a new plan.",
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
		// api-v1 v1.48 item 8 and v1.49: upgrade_agent and set_alert_settings
		// plans are kept by this server (local_plans.go).
		if res, handled := confirmUpgrade(ctx, api, in.ConfirmationID, in.IdempotencyKey); handled {
			return res
		}
		if res, handled := confirmAlertSettings(ctx, api, in.ConfirmationID, in.IdempotencyKey); handled {
			return res
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
			status   int
			payload  json.RawMessage
			fail     *mcp.CallToolResult
			deadline bool
		)
		if in.ProjectID == "" {
			// Without project_id and entry the action is unknown. It may be
			// an image deploy, so it gets the image budget and still running.
			extendWriteBudget(ctx, apiclient.MaxCallTimeout+runningLookupTimeout)
			status, payload, fail, deadline = postWithin(ctx, api, path, body, apiclient.MaxCallTimeout)
		} else {
			wait := confirmWaitFor(ctx, api, in.ProjectID, in.Entry)
			if wait.deploy {
				extendWriteBudget(ctx, wait.limit+runningLookupTimeout)
				status, payload, fail, deadline = postWithin(ctx, api, path, body, wait.limit)
			} else {
				status, payload, fail = postStatus(ctx, api, path, body)
			}
		}
		if deadline {
			return stillRunning(runningEndsBy(ctx, api, in.ProjectID, in.Entry))
		}
		if fail != nil {
			return fail
		}
		return actedOrPending(status, payload)
	})
}

// runningLookupTimeout bounds the ends_by lookup after a confirm deadline.
const runningLookupTimeout = 3 * time.Second

// maxRunningLookupHosts is the most deploy hosts the lookup reads. A project
// with more gets the text without a time.
const maxRunningLookupHosts = 5

// runningEndsBy finds the ends_by of the entry's running deploy or rollback
// (api-v1 v1.42 item 1). confirm does not name the host, so it reads the
// history of each host the project may deploy to, in parallel, and answers
// only when exactly one has a running row. "" when project_id and entry are
// unknown, the project has more than maxRunningLookupHosts deploy hosts, or
// nothing matches.
func runningEndsBy(ctx context.Context, api *apiclient.Client, projectID, entry string) string {
	if projectID == "" || entry == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, runningLookupTimeout)
	defer cancel()
	raw, fail := get(ctx, api, idPath("v1", "projects", projectID, "hosts"), nil)
	if fail != nil {
		return ""
	}
	var hosts struct {
		Items []struct {
			ID          string `json:"id"`
			AccessLevel string `json:"access_level"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &hosts) != nil {
		return ""
	}
	var ids []string
	for _, host := range hosts.Items {
		if host.AccessLevel == "deploy" && requireUUID("host_id", host.ID) == nil {
			ids = append(ids, host.ID)
		}
	}
	if len(ids) > maxRunningLookupHosts {
		return ""
	}
	found := make([]string, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if deploys, ok := fetchDeploys(ctx, api, projectID, entry, id); ok {
				found[i] = runningRowEndsBy(deploys)
			}
		}()
	}
	wg.Wait()
	endsBy := ""
	for _, f := range found {
		if f == "" {
			continue
		}
		if endsBy != "" {
			return ""
		}
		endsBy = f
	}
	return endsBy
}

// runningRowEndsBy is the ends_by of a history list's first row when it is
// in flight, else "".
func runningRowEndsBy(raw json.RawMessage) string {
	var body struct {
		Items []struct {
			Outcome string `json:"outcome"`
			EndsBy  string `json:"ends_by"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &body) != nil || len(body.Items) == 0 || body.Items[0].Outcome != "in_flight" {
		return ""
	}
	if _, err := time.Parse(time.RFC3339, body.Items[0].EndsBy); err != nil {
		return ""
	}
	return body.Items[0].EndsBy
}
