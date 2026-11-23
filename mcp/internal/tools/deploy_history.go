package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

// deploy_history (api-v1 v1.47, spec docs/spec-deploy-reason.md): recent
// deploys of an entry on a host with why each one did not succeed, and the
// stored log tail of one of them.

const deployHistorySchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id", "entry", "host_id"],
  "properties": {
    "project_id": {"type": "string", "format": "uuid"},
    "entry": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,47}$"},
    "host_id": {"type": "string", "format": "uuid"},
    "deploy_id": {"type": "string", "format": "uuid"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 20}
  }
}`

const (
	deployHistoryDefault = 10
	// deployTailPreview is how many log lines the text shows; the
	// structured result keeps all stored lines (at most 50).
	deployTailPreview = 20
	// deployReasonRunes caps a reason in the text.
	deployReasonRunes = 300
)

const deployHistoryHint = "Run deploy_history for this entry and host to read why it failed and the stored log tail."

func registerDeployHistory(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name:        "deploy_history",
		Description: "Show why deploys of a catalog image entry on a host succeeded or failed, from what the API stored (api-v1 v1.47). Lists the newest deploys, rollbacks and automatic agent actions (limit, default 10, at most 20) with outcome, short digest, time, who started it, the reason it did not succeed (for example the health check that failed, the pull error, the exit code, or that the host refused the task) and the container's final state. Also returns the stored log tail (at most 50 redacted lines captured when the deploy ended) of deploy_id, or of the newest finished deploy when deploy_id is left out. Use it after deploy_entry and confirm when the outcome is not succeeded, when confirm said the deploy is still running, or before deploying again, so you fix the cause instead of retrying blind. Needs any catalog permission on the entry; the log tail needs the logs or deploy permission. Reads stored data and works when the host is offline. Does not contact the host; entry_logs reads the live container log instead.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(deployHistorySchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			ProjectID string  `json:"project_id"`
			Entry     string  `json:"entry"`
			HostID    string  `json:"host_id"`
			DeployID  *string `json:"deploy_id"`
			Limit     *int    `json:"limit"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := validateEntryLocation(in.ProjectID, in.Entry, in.HostID); err != nil {
			return badRequest(err)
		}
		if in.DeployID != nil {
			if err := requireUUID("deploy_id", *in.DeployID); err != nil {
				return badRequest(err)
			}
		}
		limit := deployHistoryDefault
		if in.Limit != nil {
			if *in.Limit < 1 || *in.Limit > 20 {
				return badRequest(fmt.Errorf("limit must be 1 to 20"))
			}
			limit = *in.Limit
		}
		listed, fail := fetchDeployList(ctx, api, in.ProjectID, in.Entry, in.HostID)
		if fail != nil {
			return fail
		}
		rows := projectDeployRows(listed, limit)
		target := ""
		if in.DeployID != nil {
			target = strings.ToLower(*in.DeployID)
		} else {
			for _, row := range rows {
				if row["outcome"] != "in_flight" {
					target, _ = row["id"].(string)
					break
				}
			}
		}
		out := map[string]any{"deploys": rows}
		if target != "" {
			path := idPath("v1", "projects", in.ProjectID, "catalog", in.Entry, "deploys", target)
			q := url.Values{}
			q.Set("host_id", in.HostID)
			one, fail := get(ctx, api, path, q)
			if fail != nil {
				return fail
			}
			if detail, ok := projectDeployDetail(one); ok {
				out["detail"] = detail
			}
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			return toolError("error", "deploy history was not usable")
		}
		return present(encoded, summarizeDeployHistory(out))
	})
}

func fetchDeployList(ctx context.Context, api *apiclient.Client, projectID, entry, hostID string) (json.RawMessage, *mcp.CallToolResult) {
	path := idPath("v1", "projects", projectID, "catalog", entry, "deploys")
	q := url.Values{}
	q.Set("host_id", hostID)
	return get(ctx, api, path, q)
}

// projectDeployRows keeps the safe fields of the newest limit rows.
func projectDeployRows(raw json.RawMessage, limit int) []map[string]any {
	var body struct {
		Items []json.RawMessage `json:"items"`
	}
	out := []map[string]any{}
	if err := json.Unmarshal(raw, &body); err != nil {
		return out
	}
	for _, item := range body.Items {
		if len(out) == limit {
			break
		}
		if row, ok := projectDeployRow(item); ok {
			out = append(out, row)
		}
	}
	return out
}

func projectDeployRow(raw json.RawMessage) (map[string]any, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var row map[string]any
	if err := dec.Decode(&row); err != nil || row == nil {
		return nil, false
	}
	id, _ := row["id"].(string)
	if requireUUID("id", id) != nil {
		return nil, false
	}
	out := map[string]any{"id": strings.ToLower(id)}
	for _, key := range []string{"outcome", "digest", "started_at", "finished_at", "ends_by", "via", "reason"} {
		if s, ok := row[key].(string); ok && s != "" {
			out[key] = publicString(s)
		}
	}
	if n, ok := row["exit_code"].(json.Number); ok {
		out["exit_code"] = n
	}
	if fs, ok := row["final_status"].(map[string]any); ok {
		final := map[string]any{}
		if s, ok := fs["state"].(string); ok && imageStateOK(s) {
			final["state"] = s
		}
		if n, ok := fs["exit_code"].(json.Number); ok {
			final["exit_code"] = n
		}
		if n, ok := fs["restart_count"].(json.Number); ok {
			final["restart_count"] = n
		}
		if len(final) > 0 {
			out["final_status"] = final
		}
	}
	return out, true
}

// projectDeployDetail is one row plus its lines. lines is null when the
// caller may not read logs.
func projectDeployDetail(raw json.RawMessage) (map[string]any, bool) {
	raw = redactLines(raw)
	var body struct {
		Deploy json.RawMessage `json:"deploy"`
		Lines  []any           `json:"lines"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		return nil, false
	}
	row, ok := projectDeployRow(body.Deploy)
	if !ok {
		return nil, false
	}
	if body.Lines == nil {
		row["lines"] = nil
	} else {
		row["lines"] = stringList(body.Lines)
	}
	return row, true
}

func summarizeDeployHistory(out map[string]any) string {
	rows, _ := out["deploys"].([]map[string]any)
	var b strings.Builder
	if len(rows) == 0 {
		b.WriteString("No deploys of this entry on this host yet.")
	} else {
		fmt.Fprintf(&b, "%d recent deploys, newest first:", len(rows))
		for _, row := range rows {
			b.WriteString("\n")
			b.WriteString(deployRowLine(row))
		}
	}
	detail, _ := out["detail"].(map[string]any)
	if detail == nil {
		return b.String()
	}
	id, _ := detail["id"].(string)
	outcome, _ := detail["outcome"].(string)
	fmt.Fprintf(&b, "\nlog tail of %s (%s):", id, summaryField(outcome))
	lines, ok := detail["lines"].([]any)
	switch {
	case !ok:
		b.WriteString(" not shown: needs the logs or deploy permission")
	case len(lines) == 0:
		b.WriteString(" none stored (a deploy before API v1.47, a refusal or a timeout keeps no lines)")
	default:
		if len(lines) > deployTailPreview {
			fmt.Fprintf(&b, " last %d of %d lines", deployTailPreview, len(lines))
			lines = lines[len(lines)-deployTailPreview:]
		}
		for _, line := range lines {
			s, _ := line.(string)
			b.WriteString("\n")
			b.WriteString(summaryField(s))
		}
	}
	return b.String()
}

func deployRowLine(row map[string]any) string {
	outcome, _ := row["outcome"].(string)
	parts := []string{summaryField(outcome)}
	if d, ok := row["digest"].(string); ok {
		parts = append(parts, shortDigest(d))
	}
	when, _ := row["finished_at"].(string)
	if when == "" {
		when, _ = row["started_at"].(string)
	}
	if when != "" {
		parts = append(parts, summaryField(when))
	}
	if via, ok := row["via"].(string); ok {
		parts = append(parts, "via "+summaryField(via))
	}
	if fs, ok := row["final_status"].(map[string]any); ok {
		if s, ok := fs["state"].(string); ok {
			state := "container " + s
			if n, ok := fs["exit_code"].(json.Number); ok {
				state += " exit " + n.String()
			}
			parts = append(parts, state)
		}
	}
	line := strings.Join(parts, " ")
	if r, ok := row["reason"].(string); ok && r != "" {
		line += ": " + clampField(r, deployReasonRunes)
	}
	if id, ok := row["id"].(string); ok {
		line += " [" + id + "]"
	}
	return line
}
