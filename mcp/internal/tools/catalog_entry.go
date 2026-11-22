package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

// errCatalogEntryShape is fixed. It must not include the entry body.
var errCatalogEntryShape = errors.New("catalog entry response was not usable")

// maxImageRefBytes bounds set_entry_image's image before the API is called.
// The API parses the reference and names any other problem.
const maxImageRefBytes = 1024

const getCatalogEntrySchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id", "name"],
  "properties": {
    "project_id": {"type": "string", "format": "uuid"},
    "name": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,47}$"}
  }
}`

const setEntryImageSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id", "name", "image", "idempotency_key"],
  "properties": {
    "project_id": {"type": "string", "format": "uuid"},
    "name": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,47}$"},
    "image": {"type": "string", "minLength": 1, "maxLength": 1024},
    "idempotency_key": {"type": "string", "pattern": "^[A-Za-z0-9._:-]{1,128}$"}
  }
}`

func registerGetCatalogEntry(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name:        "get_catalog_entry",
		Description: "Read one catalog entry in full. For an image entry it returns the whole spec as update_catalog_entry takes it: image, kind, command, entrypoint, ports, inline env values (plain values only), secret_env as references (\"<set>.<KEY>\"), env set names, volumes and files with their targets, the healthcheck command and timings, logging, health_policy, restart, user, memory and wait times. Secret values and file contents are never returned. A program entry returns name, kind, command_display and my_actions only; programs are edited in the console. To change only the image, use set_entry_image instead of rewriting the spec. Does not contact the host.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(getCatalogEntrySchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			ProjectID string `json:"project_id"`
			Name      string `json:"name"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := requireUUID("project_id", in.ProjectID); err != nil {
			return badRequest(err)
		}
		if err := requireCatalogName(in.Name); err != nil {
			return badRequest(err)
		}
		payload, fail := get(ctx, api, idPath("v1", "projects", in.ProjectID, "catalog", in.Name), nil)
		if fail != nil {
			return fail
		}
		projected, summary, err := projectFullCatalogEntry(payload)
		if err != nil {
			return toolError("error", errCatalogEntryShape.Error())
		}
		return present(projected, summary)
	})
}

func registerSetEntryImage(server *mcp.Server, api *apiclient.Client) {
	addTool(server, catalogWriteTool(
		"set_entry_image",
		"Image entries only. Change only the image reference of an image catalog entry and keep every other setting (env, secret_env, env sets, volumes, files, healthcheck, logging, health policy, ports and the rest) exactly as stored. Use it to roll an entry forward to a new version: pass the new reference, for example ghcr.io/org/app:1.4.0 or ghcr.io/org/app@sha256:<64 hex>. An owner or admin may change it. The plan shows the image change (image: old → new). Nothing changes until confirm after the user agrees; then call deploy_entry for each host to run the new image. rollback_entry only takes a digest this entry deployed before. Program entries are edited in the console. Does not contact the host.",
		setEntryImageSchema,
	), func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			ProjectID      string `json:"project_id"`
			Name           string `json:"name"`
			Image          string `json:"image"`
			IdempotencyKey string `json:"idempotency_key"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := requireUUID("project_id", in.ProjectID); err != nil {
			return badRequest(err)
		}
		if err := requireCatalogName(in.Name); err != nil {
			return badRequest(err)
		}
		if err := requireImageRef(in.Image); err != nil {
			return badRequest(err)
		}
		if err := requireIdempotencyKey(in.IdempotencyKey); err != nil {
			return badRequest(err)
		}
		path := idPath("v1", "projects", in.ProjectID, "catalog", in.Name)
		payload, fail := get(ctx, api, path, nil)
		if fail != nil {
			return fail
		}
		spec, err := storedImageSpec(payload)
		if err != nil {
			return toolError("bad_request", err.Error())
		}
		image, err := json.Marshal(in.Image)
		if err != nil {
			return badRequest(errors.New("invalid image"))
		}
		spec["image"] = image
		body := catalogReplaceBody{Kind: "image", IdempotencyKey: in.IdempotencyKey}
		if body.Spec, err = json.Marshal(spec); err != nil {
			return toolError("error", errCatalogEntryShape.Error())
		}
		status, payload, fail := putStatus(ctx, api, path, body)
		if fail != nil {
			return fail
		}
		return confirmationCatalog(status, payload)
	})
}

func requireImageRef(image string) error {
	if image == "" {
		return errors.New("image is required")
	}
	if len(image) > maxImageRefBytes {
		return errors.New("image must be at most 1024 bytes")
	}
	for _, r := range image {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return errors.New("image must not contain spaces or control characters")
		}
	}
	return nil
}

// storedImageSpec returns the stored spec of an image entry with every field
// kept as the API sent it, so a GET then PUT changes only what the caller sets
// (api-v1 v1.24.1). A program entry or a body without a spec object is refused.
func storedImageSpec(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var row struct {
		Kind string          `json:"kind"`
		Spec json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(raw), &row); err != nil {
		return nil, errCatalogEntryShape
	}
	if row.Kind == "program" {
		return nil, errors.New("set_entry_image is for image entries; program entries are edited in the console")
	}
	if row.Kind != "image" {
		return nil, errCatalogEntryShape
	}
	dec := json.NewDecoder(bytes.NewReader(row.Spec))
	dec.UseNumber()
	var spec map[string]json.RawMessage
	if err := dec.Decode(&spec); err != nil || spec == nil {
		return nil, errCatalogEntryShape
	}
	return spec, nil
}

// projectFullCatalogEntry is get_catalog_entry's allowlist. An image entry
// keeps the ImageSpec fields update_catalog_entry accepts, with volumes,
// files, ports and healthcheck limited to their documented keys. Strings
// have control characters replaced. A program entry keeps what list_catalog
// shows for it.
func projectFullCatalogEntry(raw json.RawMessage) (json.RawMessage, string, error) {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	dec.UseNumber()
	var row map[string]any
	if err := dec.Decode(&row); err != nil || row == nil {
		return nil, "", errCatalogEntryShape
	}
	name := replaceControls(stringField(row, "name"))
	if name == "" {
		return nil, "", errCatalogEntryShape
	}
	out := map[string]any{
		"name":             name,
		"kind":             replaceControls(stringField(row, "kind")),
		"command_display":  publicString(stringField(row, "command_display")),
		"last_deployed_at": lastDeployedAt(row),
		"my_actions":       stringList(row["my_actions"]),
	}
	summary := "catalog entry " + summaryField(name)
	switch stringField(row, "kind") {
	case "program":
		summary += " (program; its spec is edited in the console)"
	case "image":
		spec, ok := row["spec"].(map[string]any)
		if !ok {
			return nil, "", errCatalogEntryShape
		}
		out["spec"] = projectFullImageSpec(spec)
		summary += " (image " + summaryField(stringField(spec, "kind")) + "): " + summaryField(publicString(stringField(spec, "image")))
	default:
		return nil, "", errCatalogEntryShape
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, "", errCatalogEntryShape
	}
	return body, summary, nil
}

// subKeys names the keys kept inside a spec field's objects. A field not
// listed here is copied whole (strings, numbers, booleans, string lists and
// string maps), with controls replaced.
var subKeys = map[string]map[string]struct{}{
	"ports":       {"host": {}, "container": {}, "public": {}},
	"volumes":     {"name": {}, "target": {}, "read_only": {}},
	"files":       {"file": {}, "target": {}},
	"healthcheck": {"test": {}, "interval_sec": {}, "timeout_sec": {}, "retries": {}, "start_period_sec": {}},
	"logging":     {"max_size_mb": {}, "max_files": {}},
	"health_policy": {
		"on_deploy_failure": {}, "on_unhealthy": {}, "unhealthy_after_sec": {}, "max_actions_per_hour": {},
	},
}

func projectFullImageSpec(spec map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range spec {
		if _, ok := imageSpecKeys[key]; !ok {
			continue
		}
		if keep, ok := subKeys[key]; ok {
			out[key] = keepKeys(value, keep)
			continue
		}
		out[key] = cleanJSON(value)
	}
	return out
}

// keepKeys keeps the listed keys of an object, or of each object in a list.
func keepKeys(v any, keep map[string]struct{}) any {
	switch t := v.(type) {
	case map[string]any:
		obj := map[string]any{}
		for key, value := range t {
			if _, ok := keep[key]; ok {
				obj[key] = cleanJSON(value)
			}
		}
		return obj
	case []any:
		list := make([]any, 0, len(t))
		for _, item := range t {
			if obj, ok := item.(map[string]any); ok {
				list = append(list, keepKeys(obj, keep))
			}
		}
		return list
	default:
		return cleanJSON(v)
	}
}

// cleanJSON copies a decoded JSON value and replaces control characters in
// every string and object key.
func cleanJSON(v any) any {
	switch t := v.(type) {
	case string:
		return replaceControls(t)
	case map[string]any:
		obj := make(map[string]any, len(t))
		for key, value := range t {
			obj[replaceControls(key)] = cleanJSON(value)
		}
		return obj
	case []any:
		list := make([]any, len(t))
		for i, item := range t {
			list[i] = cleanJSON(item)
		}
		return list
	default:
		return v
	}
}
