package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

const entryStatusSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id", "entry"],
  "properties": {
    "project_id": {"type": "string", "format": "uuid"},
    "entry": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,47}$"},
    "host_id": {"type": "string", "format": "uuid", "description": "Optional when the entry's copies run on exactly one host."}
  }
}`

func registerEntryStatus(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name:        "entry_status",
		Description: "Read the live status of a catalog program or image container on a host. This contacts the host now and waits for the agent (up to 30 seconds). For a program, returns the unit state, pid, start time, and ports. For an image, returns the container state, digest, and exit code. Fails if the host is offline or asleep. host_id is optional: without it, the host is taken from the project's running copies of the entry when they are all on one host; with none or several, the result lists the hosts (names and ids) and asks for host_id.",
		Annotations: readOnlyAnnotations(true),
		InputSchema: json.RawMessage(entryStatusSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			ProjectID string `json:"project_id"`
			Entry     string `json:"entry"`
			HostID    string `json:"host_id"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if in.HostID == "" {
			if err := requireUUID("project_id", in.ProjectID); err != nil {
				return badRequest(err)
			}
			if err := requireEntryName(in.Entry); err != nil {
				return badRequest(err)
			}
			hostID, fail := entryHost(ctx, api, in.ProjectID, in.Entry)
			if fail != nil {
				return fail
			}
			in.HostID = hostID
		}
		if err := validateEntryLocation(in.ProjectID, in.Entry, in.HostID); err != nil {
			return badRequest(err)
		}
		path := idPath("v1", "projects", in.ProjectID, "catalog", in.Entry, "status")
		body := struct {
			HostID string `json:"host_id"`
		}{HostID: in.HostID}
		payload, fail := post(ctx, api, path, body)
		if fail != nil {
			return fail
		}
		if imageStateRejected(payload) {
			return toolError("error", errStatusShape.Error())
		}
		return present(payload, summarizeEntryStatus(payload))
	})
}

// entryHostPages bounds the services read that finds an entry's host:
// 5 pages of 200 rows, as the console's Catalog & services tab.
const entryHostPages = 5

// entryHost finds the one host the project's running copies of entry are
// on, from the project services list (view deploybot). With none or several
// it returns a tool error that lists the hosts and asks for host_id. When the
// list is longer than entryHostPages pages it picks nothing and asks too.
func entryHost(ctx context.Context, api *apiclient.Client, projectID, entry string) (string, *mcp.CallToolResult) {
	hosts := map[string]string{}
	cursor := ""
	complete := false
	for page := 0; page < entryHostPages; page++ {
		q := url.Values{}
		q.Set("view", "deploybot")
		q.Set("limit", "200")
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		payload, fail := get(ctx, api, idPath("v1", "projects", projectID, "services"), q)
		if fail != nil {
			return "", fail
		}
		var body struct {
			Items []struct {
				HostID       string  `json:"host_id"`
				HostName     string  `json:"host_name"`
				ProjectID    *string `json:"project_id"`
				CatalogEntry *string `json:"catalog_entry"`
				GoneAt       *string `json:"gone_at"`
			} `json:"items"`
			NextCursor *string `json:"next_cursor"`
		}
		if err := json.Unmarshal(payload, &body); err != nil {
			return "", toolError("error", errStatusShape.Error())
		}
		for _, item := range body.Items {
			if item.CatalogEntry == nil || *item.CatalogEntry != entry || item.GoneAt != nil ||
				item.ProjectID == nil || !strings.EqualFold(*item.ProjectID, projectID) || !uuidPattern.MatchString(item.HostID) {
				continue
			}
			hosts[strings.ToLower(item.HostID)] = item.HostName
		}
		if body.NextCursor == nil || *body.NextCursor == "" {
			complete = true
			break
		}
		cursor = *body.NextCursor
	}
	if !complete {
		// The list was cut at the page bound: a copy on another host may be
		// on a later page, so no host is picked.
		return "", toolError("bad_request", "this project has too many services to pick a host automatically; pass host_id (list_services with project_id shows where the entry runs)")
	}
	if len(hosts) == 1 {
		for id := range hosts {
			return id, nil
		}
	}
	if len(hosts) == 0 {
		return "", toolError("bad_request", fmt.Sprintf("no running copy of %s was found in this project; pass host_id (list_hosts lists the project's hosts)", entry))
	}
	ids := make([]string, 0, len(hosts))
	for id := range hosts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if hosts[ids[i]] != hosts[ids[j]] {
			return hosts[ids[i]] < hosts[ids[j]]
		}
		return ids[i] < ids[j]
	})
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = summaryField(hosts[id]) + " (" + id + ")"
	}
	return "", toolError("bad_request", fmt.Sprintf("%s runs on %d hosts: %s; pass host_id", entry, len(ids), strings.Join(parts, ", ")))
}
