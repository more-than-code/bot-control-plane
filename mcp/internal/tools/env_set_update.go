package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

// update_env_set and delete_env_set (api-v1 v1.33 items 1 and 2): plans the
// person confirms with confirm. The only value a tool sends is a plain
// key's value; no tool returns a value or a preview.

const updateEnvSetSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id", "name", "idempotency_key"],
  "properties": {
    "project_id": {"type": "string", "format": "uuid"},
    "name": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,47}$", "description": "A shared env set. An entry's private set changes through the entry."},
    "idempotency_key": {"type": "string", "pattern": "^[A-Za-z0-9._:-]{1,128}$"},
    "set": {
      "type": "object",
      "description": "New values for existing PLAIN keys, KEY: value. ` + plainValueRule + ` A secret key is refused: change it with request_env_value.",
      "additionalProperties": {"type": "string", "maxLength": 4096},
      "propertyNames": {"pattern": "^[A-Z_][A-Z0-9_]{0,63}$"}
    },
    "add": {
      "type": "array",
      "maxItems": 64,
      "description": "New keys, as create_env_set keys: generated, a placeholder, or a plain key with its value.",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["key"],
        "properties": {
          "key": {"type": "string", "pattern": "^[A-Z_][A-Z0-9_]{0,63}$"},
          "generate": {
            "type": "object",
            "additionalProperties": false,
            "required": ["kind"],
            "properties": {
              "kind": {"type": "string", "enum": ["password", "hex", "base64url"]},
              "length": {"type": "integer", "minimum": 16, "maximum": 64, "description": "Password only; default 32."}
            }
          },
          "is_secret": {"type": "boolean", "description": "Placeholders and plain values; default true. A generated key is always secret."},
          "value": {"type": "string", "maxLength": 4096, "description": "` + plainValueRule + `"}
        }
      }
    },
    "remove": {"type": "array", "maxItems": 64, "items": {"type": "string", "pattern": "^[A-Z_][A-Z0-9_]{0,63}$"}, "description": "Keys to remove. Refused while an entry uses the key; update that entry first."},
    "make_secret": {"type": "array", "maxItems": 64, "items": {"type": "string", "pattern": "^[A-Z_][A-Z0-9_]{0,63}$"}, "description": "Plain keys to make secret. One way: a secret key never becomes plain. A key with an empty value is refused (give it a value first); an empty placeholder stays a placeholder."}
  }
}`

const deleteEnvSetSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["project_id", "name", "idempotency_key"],
  "properties": {
    "project_id": {"type": "string", "format": "uuid"},
    "name": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,47}$"},
    "idempotency_key": {"type": "string", "pattern": "^[A-Za-z0-9._:-]{1,128}$"}
  }
}`

type updateEnvSetArgs struct {
	ProjectID      string            `json:"project_id"`
	Name           string            `json:"name"`
	IdempotencyKey string            `json:"idempotency_key"`
	Set            map[string]string `json:"set,omitempty"`
	Add            []envSetKeyArg    `json:"add,omitempty"`
	Remove         []string          `json:"remove,omitempty"`
	MakeSecret     []string          `json:"make_secret,omitempty"`
}

func (a updateEnvSetArgs) validate() error {
	if err := requireUUID("project_id", a.ProjectID); err != nil {
		return err
	}
	if err := requireCatalogName(a.Name); err != nil {
		return err
	}
	if err := requireIdempotencyKey(a.IdempotencyKey); err != nil {
		return err
	}
	if len(a.Set) == 0 && len(a.Add) == 0 && len(a.Remove) == 0 && len(a.MakeSecret) == 0 {
		return errors.New("send at least one of set, add, remove or make_secret")
	}
	if len(a.Set) > maxEnvSetKeys || len(a.Add) > maxEnvSetKeys || len(a.Remove) > maxEnvSetKeys || len(a.MakeSecret) > maxEnvSetKeys {
		return errors.New("each list may name at most 64 keys")
	}
	seen := map[string]bool{}
	claim := func(key string) error {
		if !envKeyPattern.MatchString(key) {
			return errors.New("invalid key name")
		}
		if seen[key] {
			return fmt.Errorf("key %s is named more than once", key)
		}
		seen[key] = true
		return nil
	}
	for key, value := range a.Set {
		if err := claim(key); err != nil {
			return err
		}
		if len(value) > maxEnvValueBytes || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("invalid env value")
		}
	}
	if len(a.Add) > 0 {
		// The create rules, without the 1-key minimum of a new set.
		if err := validateCreateEnvSet(a.ProjectID, a.Name, a.IdempotencyKey, a.Add); err != nil {
			return err
		}
		for _, k := range a.Add {
			if err := claim(k.Key); err != nil {
				return err
			}
		}
	}
	for _, list := range [][]string{a.Remove, a.MakeSecret} {
		for _, key := range list {
			if err := claim(key); err != nil {
				return err
			}
		}
	}
	return nil
}

func registerUpdateEnvSet(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name: "update_env_set",
		Description: "Change an env set you or the person made before. Nothing changes until confirm after the user agrees. In one plan: set new values for existing plain keys (a secret key is refused: ask for its new value with request_env_value, which replaces it), add keys as in create_env_set (generated, placeholder, or plain with its value), remove keys, and make plain keys secret (one way). " +
			"Removing a key an entry uses is refused with the entry names: update those entries first. value: " + plainValueRule + " If the plan warns that a key looks like a secret, tell the person before confirming. " +
			"The plan and result name keys only. No secret value is sent or returned. Needs project owner or admin. Does not contact any host.",
		Annotations: mutatingAnnotations(false),
		InputSchema: json.RawMessage(updateEnvSetSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in updateEnvSetArgs
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := in.validate(); err != nil {
			return badRequest(err)
		}
		body := struct {
			Set            map[string]string `json:"set,omitempty"`
			Add            []envSetKeyArg    `json:"add,omitempty"`
			Remove         []string          `json:"remove,omitempty"`
			MakeSecret     []string          `json:"make_secret,omitempty"`
			IdempotencyKey string            `json:"idempotency_key"`
		}{in.Set, in.Add, in.Remove, in.MakeSecret, in.IdempotencyKey}
		status, payload, fail := callStatus(ctx, api, http.MethodPatch, idPath("v1", "projects", in.ProjectID, "env-sets", in.Name), nil, body)
		if fail != nil {
			return fail
		}
		return confirmationCatalog(status, payload)
	})
}

func registerDeleteEnvSet(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name: "delete_env_set",
		Description: "Delete a shared env set and all its keys. Nothing changes until confirm after the user agrees. Refused while any catalog entry names the set in env_sets or secret_env (the answer lists them; list_env_sets shows used_by): update those entries first. " +
			"Open value requests for the set stop working. An entry's private set is deleted with its entry instead. No value is sent or returned. Needs project owner or admin. Does not contact any host.",
		Annotations: mutatingAnnotations(false),
		InputSchema: json.RawMessage(deleteEnvSetSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			ProjectID      string `json:"project_id"`
			Name           string `json:"name"`
			IdempotencyKey string `json:"idempotency_key"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := validateCatalogDelete(in.ProjectID, in.Name, in.IdempotencyKey); err != nil {
			return badRequest(err)
		}
		body := struct {
			IdempotencyKey string `json:"idempotency_key"`
		}{in.IdempotencyKey}
		status, payload, fail := callStatus(ctx, api, http.MethodDelete, idPath("v1", "projects", in.ProjectID, "env-sets", in.Name), nil, body)
		if fail != nil {
			return fail
		}
		return confirmationCatalog(status, payload)
	})
}

// envSetUpdated is the confirm result of an update plan: names and flags only.
func envSetUpdated(payload json.RawMessage) *mcp.CallToolResult {
	set, err := projectEnvSet(payload)
	if err != nil {
		return toolError("error", errEnvSetShape.Error())
	}
	out, _ := json.Marshal(set)
	return present(out, fmt.Sprintf("updated env set %s; it has %s. Generated values are revealed once, only by the person: list_env_sets gives each key's link to send them. Fill placeholders with request_env_value.", set.Name, countKeys(len(set.Keys))))
}

// envSetDeletedPayload reports whether a 200 confirm body is a deleted env
// set: exactly {"name", "deleted": true}.
func envSetDeletedPayload(payload json.RawMessage) (string, bool) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(payload, &body); err != nil || len(body) != 2 {
		return "", false
	}
	var name string
	var deleted bool
	if json.Unmarshal(body["name"], &name) != nil || json.Unmarshal(body["deleted"], &deleted) != nil || !deleted || !entryNamePattern.MatchString(name) {
		return "", false
	}
	return name, true
}
