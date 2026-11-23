package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

// errTagsShape is fixed. It must not include the registry answer.
var errTagsShape = errors.New("image tags response was not usable")

// tagRule is the v1.16 tag rule; the API drops other tags.
var tagRule = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

const (
	defaultTagLimit = 20
	maxTagLimit     = 50
)

const listImageTagsSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id"],
  "properties": {
    "project_id": {"type": "string", "format": "uuid"},
    "entry": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,47}$", "description": "An image catalog entry; its repository is listed and its current tag marked."},
    "image": {"type": "string", "maxLength": 512, "description": "Any image reference, for example ghcr.io/acme/api or nginx; the tag and digest are ignored."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "At most this many tags (default 20)."}
  }
}`

func registerListImageTags(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name: "list_image_tags",
		Description: "List the tags of an image repository, newest first, to choose the version to deploy. Pass entry (an image catalog entry: its repository is listed and the tag it stores is marked current) or image (any reference). " +
			"order is time when the tags are sorted by push time (ghcr.io with Connect GitHub; each tag then has updated_at), else name (version-like tags highest first, then latest, then the rest); say which when you call one the newest. " +
			"Each item has image, the full reference to pass to set_entry_image; then confirm and call deploy_entry for each host. Program entries have no image tags. Needs owner or admin. Asks the image registry (cached 60 s, 20 lookups per minute). Does not contact any host.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(listImageTagsSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			ProjectID string `json:"project_id"`
			Entry     string `json:"entry"`
			Image     string `json:"image"`
			Limit     *int   `json:"limit"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := requireUUID("project_id", in.ProjectID); err != nil {
			return badRequest(err)
		}
		if (in.Entry == "") == (in.Image == "") {
			return badRequest(errors.New("pass exactly one of entry or image"))
		}
		limit := defaultTagLimit
		if in.Limit != nil {
			if *in.Limit < 1 || *in.Limit > maxTagLimit {
				return badRequest(errors.New("limit must be 1 to 50"))
			}
			limit = *in.Limit
		}
		image := in.Image
		current := ""
		if in.Entry != "" {
			if err := requireCatalogName(in.Entry); err != nil {
				return badRequest(err)
			}
			payload, fail := get(ctx, api, idPath("v1", "projects", in.ProjectID, "catalog", in.Entry), nil)
			if fail != nil {
				return fail
			}
			stored, err := storedImageRef(payload)
			if err != nil {
				return toolError("bad_request", err.Error())
			}
			image, current = stored, stored
		} else if len(image) > 512 || strings.ContainsAny(image, " \t\r\n") {
			return badRequest(errors.New("image must be an image reference"))
		}
		q := url.Values{}
		q.Set("image", image)
		payload, fail := get(ctx, api, idPath("v1", "projects", in.ProjectID, "image-tags"), q)
		if fail != nil {
			return fail
		}
		out, summary, err := projectImageTags(payload, imageRepository(image), current, limit)
		if err != nil {
			return toolError("error", errTagsShape.Error())
		}
		return present(out, summary)
	})
}

// storedImageRef is the image an image entry stores. A program entry is
// refused with a sentence the agent can act on.
func storedImageRef(raw json.RawMessage) (string, error) {
	var row struct {
		Kind string `json:"kind"`
		Spec struct {
			Image string `json:"image"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return "", errCatalogEntryShape
	}
	if row.Kind == "program" {
		return "", errors.New("list_image_tags is for image entries; a program runs the command in its host's runner.toml and has no image tags")
	}
	if row.Kind != "image" || row.Spec.Image == "" || len(row.Spec.Image) > 512 || strings.ContainsAny(row.Spec.Image, " \t\r\n") {
		return "", errCatalogEntryShape
	}
	return row.Spec.Image, nil
}

// imageRepository drops a reference's digest and tag.
func imageRepository(ref string) string {
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndexByte(ref, ':'); i >= 0 && strings.LastIndexByte(ref, '/') < i {
		ref = ref[:i]
	}
	return ref
}

// imageTag is a reference's tag, or "" when it has none.
func imageTag(ref string) string {
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		ref = ref[:i]
	}
	if i := strings.LastIndexByte(ref, ':'); i >= 0 && strings.LastIndexByte(ref, '/') < i {
		return ref[i+1:]
	}
	return ""
}

// projectImageTags keeps tag, updated_at and the full reference of each
// valid item, up to limit. Anything else in the API answer is dropped.
func projectImageTags(raw json.RawMessage, repo, current string, limit int) (json.RawMessage, string, error) {
	var body struct {
		Items []struct {
			Tag       string `json:"tag"`
			UpdatedAt string `json:"updated_at"`
		} `json:"items"`
		Truncated bool   `json:"truncated"`
		Order     string `json:"order"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Items == nil {
		return nil, "", errTagsShape
	}
	order := body.Order
	if order != "time" {
		// An API before v1.46 sends no order: its list is the name order.
		order = "name"
	}
	currentTag := ""
	if current != "" {
		currentTag = imageTag(current)
	}
	items := make([]map[string]any, 0, min(limit, len(body.Items)))
	truncated := body.Truncated
	for _, item := range body.Items {
		if !tagRule.MatchString(item.Tag) {
			continue
		}
		if len(items) == limit {
			truncated = true
			break
		}
		row := map[string]any{"tag": item.Tag, "image": repo + ":" + item.Tag}
		if item.UpdatedAt != "" {
			at, err := time.Parse(time.RFC3339Nano, item.UpdatedAt)
			if err != nil {
				return nil, "", errTagsShape
			}
			row["updated_at"] = at.UTC().Format(time.RFC3339)
		}
		if current != "" {
			row["current"] = item.Tag == currentTag
		}
		items = append(items, row)
	}
	out := map[string]any{"repository": repo, "order": order, "items": items, "truncated": truncated}
	if current != "" {
		out["current"] = current
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, "", errTagsShape
	}
	return encoded, tagsSummary(repo, current, order, items, truncated), nil
}

func tagsSummary(repo, current, order string, items []map[string]any, truncated bool) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(len(items)) + " tags of " + summaryField(repo))
	if order == "time" {
		b.WriteString(", newest push first")
	} else {
		b.WriteString(", by version name (push times unknown)")
	}
	if truncated {
		b.WriteString("; more exist")
	}
	if len(items) > 0 {
		b.WriteString(". First: " + summaryField(items[0]["tag"].(string)))
	}
	if current != "" {
		b.WriteString(". Entry stores " + summaryField(current))
	}
	b.WriteString(".")
	return b.String()
}
