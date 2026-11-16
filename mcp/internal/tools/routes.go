package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

// set_routes and list_routes (api-v1 v1.35 item 6). Routes are an entry's
// public addresses (hostname and path to a port) served by the host's
// Deploybot edge. Domains and the edge switch are console only.

const maxRoutes = 16

var (
	routeHostPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	routePathPattern = regexp.MustCompile(`^/$|^(/[A-Za-z0-9._~-]+){1,16}$`)
	noStorePattern   = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+){1,16}/?$`)
)

const setRoutesSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id", "entry", "routes", "idempotency_key"],
  "properties": {
    "project_id": {"type": "string", "format": "uuid"},
    "entry": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,47}$"},
    "idempotency_key": {"type": "string", "pattern": "^[A-Za-z0-9._:-]{1,128}$"},
    "routes": {
      "type": "array",
      "maxItems": 16,
      "description": "The entry's complete list of routes; it replaces the current one. [] removes them all.",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["hostname", "port"],
        "properties": {
          "hostname": {"type": "string", "maxLength": 253, "description": "Lowercase, under a verified domain an org admin assigned to the project."},
          "path_prefix": {"type": "string", "description": "/ (default) or /seg[/seg…]."},
          "port": {"type": "integer", "minimum": 1, "maximum": 65535, "description": "For an image entry, a container port the entry publishes in ports (127.0.0.1 is enough). For a program, the port it listens on."},
          "client_ip_from": {"type": "string", "enum": ["cloudflare"], "description": "Only when the site sits behind Cloudflare: trust CF-Connecting-IP from Cloudflare's ranges."},
          "no_store_paths": {"type": "array", "maxItems": 8, "items": {"type": "string"}, "description": "Path prefixes whose responses get Cache-Control: private, no-store."}
        }
      }
    }
  }
}`

const listRoutesSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id"],
  "properties": {
    "project_id": {"type": "string", "format": "uuid"},
    "entry": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,47}$", "description": "One entry's routes; omit for the whole project."}
  }
}`

type routeArg struct {
	Hostname     string   `json:"hostname"`
	PathPrefix   *string  `json:"path_prefix,omitempty"`
	Port         int      `json:"port"`
	ClientIPFrom *string  `json:"client_ip_from,omitempty"`
	NoStorePaths []string `json:"no_store_paths,omitempty"`
}

// confirmationRoutes is confirmationCatalog for a routes plan, whose API
// text puts each address that goes live or stops on its own line: the
// line breaks are kept, every other control character is replaced.
func confirmationRoutes(status int, payload json.RawMessage) *mcp.CallToolResult {
	if status != http.StatusAccepted {
		return toolError("error", "expected a confirmation")
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil || body == nil {
		return toolError("error", errConfirmationShape.Error())
	}
	plan, _ := body["plan"].(string)
	lines := strings.Split(plan, "\n")
	for i, line := range lines {
		lines[i] = replaceControls(line)
	}
	out := map[string]any{}
	if v, ok := body["confirmation_id"].(string); ok {
		out["confirmation_id"] = publicString(v)
	}
	if v, ok := body["expires_at"].(string); ok {
		out["expires_at"] = publicString(v)
	}
	if plan != "" {
		out["plan"] = strings.Join(lines, "\n")
	}
	if len([]rune(plan)) > maxCatalogPlanRunes {
		return toolError("error", "plan too long to show")
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return toolError("error", errConfirmationShape.Error())
	}
	return finishConfirmation(encoded, nil)
}

func validateRoutes(routes []routeArg) error {
	if routes == nil {
		return errors.New("routes is required; [] removes all routes")
	}
	if len(routes) > maxRoutes {
		return fmt.Errorf("at most %d routes per entry", maxRoutes)
	}
	for _, r := range routes {
		if len(r.Hostname) > 253 || !routeHostPattern.MatchString(r.Hostname) {
			return errors.New("invalid hostname: a lowercase name such as app.example.com")
		}
		if r.PathPrefix != nil && !routePathPattern.MatchString(*r.PathPrefix) {
			return errors.New("invalid path_prefix: / or /seg[/seg…]")
		}
		if r.Port < 1 || r.Port > 65535 {
			return errors.New("invalid port")
		}
		if r.ClientIPFrom != nil && *r.ClientIPFrom != "cloudflare" {
			return errors.New("client_ip_from must be cloudflare or absent")
		}
		if len(r.NoStorePaths) > 8 {
			return errors.New("at most 8 no_store_paths per route")
		}
		for _, p := range r.NoStorePaths {
			if !noStorePattern.MatchString(p) {
				return errors.New("invalid no_store_paths: path prefixes such as /evidence/")
			}
		}
	}
	return nil
}

func registerRoutes(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name: "set_routes",
		Description: "Replace a catalog entry's public routes (hostname and optional path to a port), served over HTTPS with automatic certificates by the Deploybot edge on the host where the entry runs. Nothing changes until confirm after the user agrees; the plan names every address that goes live or stops, one per line. " +
			"Each hostname must be under a domain an org admin verified and assigned to the project in the console (domains and turning the edge on are console only). For an image entry the port is a container port the entry publishes in ports (127.0.0.1 is enough). " +
			"A hostname and path already routed elsewhere is refused. A route stays pending until the entry runs on a host whose edge is on; check_readiness reports what is missing. Routes never change the entry's spec or restart it. " +
			"Needs project owner or admin. Does not contact any host; after confirm the API updates the host's edge.",
		Annotations: mutatingAnnotations(false),
		InputSchema: json.RawMessage(setRoutesSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			ProjectID      string     `json:"project_id"`
			Entry          string     `json:"entry"`
			Routes         []routeArg `json:"routes"`
			IdempotencyKey string     `json:"idempotency_key"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := requireUUID("project_id", in.ProjectID); err != nil {
			return badRequest(err)
		}
		if err := requireEntryName(in.Entry); err != nil {
			return badRequest(err)
		}
		if err := requireIdempotencyKey(in.IdempotencyKey); err != nil {
			return badRequest(err)
		}
		if err := validateRoutes(in.Routes); err != nil {
			return badRequest(err)
		}
		body := struct {
			Routes         []routeArg `json:"routes"`
			IdempotencyKey string     `json:"idempotency_key"`
		}{in.Routes, in.IdempotencyKey}
		status, payload, fail := callStatus(ctx, api, http.MethodPut, idPath("v1", "projects", in.ProjectID, "catalog", in.Entry, "routes"), nil, body)
		if fail != nil {
			return fail
		}
		return confirmationRoutes(status, payload)
	})

	addTool(server, &mcp.Tool{
		Name:        "list_routes",
		Description: "List a project's routes, or one entry's: hostname, path_prefix, port, options, the host and the state (live, pending until the entry runs on a host whose edge is on and the config is applied, or error). Reads stored API data. Does not contact any host.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(listRoutesSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			ProjectID string `json:"project_id"`
			Entry     string `json:"entry"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := requireUUID("project_id", in.ProjectID); err != nil {
			return badRequest(err)
		}
		path := idPath("v1", "projects", in.ProjectID, "routes")
		if in.Entry != "" {
			if err := requireEntryName(in.Entry); err != nil {
				return badRequest(err)
			}
			path = idPath("v1", "projects", in.ProjectID, "catalog", in.Entry, "routes")
		}
		payload, fail := get(ctx, api, path, nil)
		if fail != nil {
			return fail
		}
		var body struct {
			Items []routeRow `json:"items"`
		}
		if err := json.Unmarshal(payload, &body); err != nil {
			return toolError("error", "routes response was not usable")
		}
		return present(payload, summarizeRoutes(fmt.Sprintf("%d routes", len(body.Items)), body.Items))
	})
}

type routeRow struct {
	Entry      string `json:"entry"`
	Hostname   string `json:"hostname"`
	PathPrefix string `json:"path_prefix"`
	Port       int    `json:"port"`
	State      string `json:"state"`
}

func summarizeRoutes(head string, rows []routeRow) string {
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("https://%s%s → %s port %d (%s)", summaryField(r.Hostname), summaryField(r.PathPrefix), summaryField(r.Entry), r.Port, summaryField(r.State)))
	}
	return withPreview(head, parts)
}

// routesConfirmed is the confirm result of a routes plan: {"entry","routes"}.
func routesConfirmed(payload json.RawMessage) (*mcp.CallToolResult, bool) {
	var body struct {
		Entry  *string    `json:"entry"`
		Routes []routeRow `json:"routes"`
	}
	if json.Unmarshal(payload, &body) != nil || body.Entry == nil || body.Routes == nil {
		return nil, false
	}
	return present(payload, summarizeRoutes("routes for "+summaryField(*body.Entry)+" set: "+fmt.Sprintf("%d routes", len(body.Routes)), body.Routes)), true
}
