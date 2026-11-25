package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

const deployEntrySchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id", "entry", "host_id", "idempotency_key"],
  "properties": {
    "project_id": {"type": "string", "format": "uuid"},
    "entry": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,47}$"},
    "host_id": {"type": "string", "format": "uuid"},
    "idempotency_key": {"type": "string", "pattern": "^[A-Za-z0-9._:-]{1,128}$"},
    "force": {"type": "boolean"}
  }
}`

func registerDeployEntry(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name:        "deploy_entry",
		Description: "Deploy a catalog image entry on a host, with the image the entry stores; to deploy a new version, find its tag with list_image_tags and change the entry with set_entry_image first. Requires the deploy permission. The host must run containers. The action is real and is audited. Nothing runs until a second confirm call with the returned confirmation id, after the user agrees. This call does not contact the host. The API refuses when the entry joins a shared network the host does not open to the project, or the host's agent is older than 0.2.9. The result lists the last 3 deploys so a rollback digest can be chosen, with the reason of any that did not succeed; if the last one failed, read deploy_history for that entry and host and fix the cause before deploying again. Run check_readiness first to see what is missing. When confirm reports an outcome other than succeeded or unchanged, its result gives the reason, and deploy_history keeps the reason and the log tail afterwards. If confirm reports the host unavailable after the deploy was sent, the host may still finish it: deploy_history shows it running until its ends_by time, then its real outcome or timeout; check there before deploying again.",
		Annotations: mutatingAnnotations(false),
		InputSchema: json.RawMessage(deployEntrySchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			ProjectID      string `json:"project_id"`
			Entry          string `json:"entry"`
			HostID         string `json:"host_id"`
			IdempotencyKey string `json:"idempotency_key"`
			Force          *bool  `json:"force"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := validateEntryAction(in.ProjectID, in.Entry, in.HostID, in.IdempotencyKey); err != nil {
			return badRequest(err)
		}
		// This POST only reserves a confirmation. The long wait is confirm.
		path := idPath("v1", "projects", in.ProjectID, "catalog", in.Entry, "deploy")
		body := struct {
			HostID         string `json:"host_id"`
			Force          *bool  `json:"force,omitempty"`
			IdempotencyKey string `json:"idempotency_key"`
		}{HostID: in.HostID, Force: in.Force, IdempotencyKey: in.IdempotencyKey}
		status, payload, fail := postStatus(ctx, api, path, body)
		if fail != nil {
			return withReadinessHint(fail)
		}
		if status != http.StatusAccepted {
			return confirmationOnly(status, payload)
		}
		if hist, ok := fetchDeploys(ctx, api, in.ProjectID, in.Entry, in.HostID); ok {
			payload = attachHistory(payload, hist)
		}
		if in.Force != nil && *in.Force {
			payload = markForce(payload)
		}
		return withConfirmHint(pendingConfirmation(payload))
	})
}

// readinessHint ends a deploy_entry refusal (api-v1 v1.29, spec §3.3).
const readinessHint = "Run check_readiness for this entry and host to see what is missing and how to fix it."

// withReadinessHint adds readinessHint to an API refusal (bad_request,
// forbidden or unavailable). Other errors are returned unchanged.
func withReadinessHint(res *mcp.CallToolResult) *mcp.CallToolResult {
	if !res.IsError || len(res.Content) == 0 {
		return res
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		return res
	}
	for _, code := range []string{"bad_request: ", "forbidden: ", "unavailable: "} {
		if strings.HasPrefix(text.Text, code) {
			text.Text += "\n" + readinessHint
			return res
		}
	}
	return res
}

// confirmHint ends a deploy or rollback 202 text.
const confirmHint = "When you confirm, pass project_id and entry as well."

// withConfirmHint adds confirmHint to a pending deploy or rollback result.
// An error result is returned unchanged.
func withConfirmHint(res *mcp.CallToolResult) *mcp.CallToolResult {
	if res.IsError || len(res.Content) == 0 {
		return res
	}
	if text, ok := res.Content[0].(*mcp.TextContent); ok {
		text.Text += "\n" + confirmHint
	}
	return res
}

// markForce records that this confirmation was reserved with force.
// projectConfirmation copies the flag onto the structured result and the plan.
func markForce(raw json.RawMessage) json.RawMessage {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil {
		return raw
	}
	body["force"] = true
	out, err := json.Marshal(body)
	if err != nil {
		return raw
	}
	return out
}

func fetchDeploys(ctx context.Context, api *apiclient.Client, projectID, entry, hostID string) (json.RawMessage, bool) {
	path := idPath("v1", "projects", projectID, "catalog", entry, "deploys")
	q := url.Values{}
	q.Set("host_id", hostID)
	payload, fail := get(ctx, api, path, q)
	if fail != nil {
		return nil, false
	}
	return payload, true
}

// attachHistory keeps digest, outcome, and time for the newest 3 non-automatic
// rows (outcomes starting with auto_ are skipped).
// The digest stays full so rollback_entry can use it. Other fields, including
// anything that could carry an env value, are dropped.
func attachHistory(raw, deploys json.RawMessage) json.RawMessage {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil {
		return raw
	}
	body["history"] = projectHistory(deploys)
	out, err := json.Marshal(body)
	if err != nil {
		return raw
	}
	return out
}

func projectHistory(raw json.RawMessage) []any {
	var body struct {
		Items []struct {
			Digest     string `json:"digest"`
			Outcome    string `json:"outcome"`
			StartedAt  string `json:"started_at"`
			FinishedAt string `json:"finished_at"`
			EndsBy     string `json:"ends_by"`
			Reason     string `json:"reason"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return []any{}
	}
	// Three rows that reached the host, so a rollback digest can be
	// chosen, plus at most the newest refused row (api-v1 v1.47), which
	// has no digest and does not take one of the three places.
	out := make([]any, 0, 4)
	kept, refused := 0, false
	for _, item := range body.Items {
		if kept == 3 {
			break
		}
		if strings.HasPrefix(item.Outcome, "auto_") {
			continue
		}
		if item.Digest == "" && item.Outcome == "" {
			continue
		}
		if item.Outcome == "refused" {
			if refused {
				continue
			}
			refused = true
		} else {
			kept++
		}
		when := item.FinishedAt
		if when == "" {
			when = item.StartedAt
		}
		row := map[string]any{
			"digest":  item.Digest,
			"outcome": item.Outcome,
			"time":    when,
		}
		// A running row (api-v1 v1.42 item 1) has no digest and no
		// finished_at; time is its start and ends_by its latest end.
		if item.Outcome == "in_flight" && item.EndsBy != "" {
			row["ends_by"] = item.EndsBy
		}
		// api-v1 v1.47: why a past deploy did not succeed.
		if item.Reason != "" && !deployOutcomeOK(item.Outcome) {
			row["reason"] = publicString(item.Reason)
		}
		out = append(out, row)
	}
	return out
}
