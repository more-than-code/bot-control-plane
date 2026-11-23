package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
	"github.com/more-than-code/bot-control-plane/mcp/internal/auth"
)

// Host alert settings (api-v1 v1.49 items 3 and 6). The API accepts the
// delegated PUT directly, so the confirmation lives here: set_alert_settings
// checks the host is visible and keeps the values in pendingAlertSettings
// (local_plans.go); confirm with that id and key sends the PUT with the
// caller's own token.

const getAlertSettingsSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["host_id"],
  "properties": {
    "host_id": {"type": "string", "format": "uuid"}
  }
}`

const setAlertSettingsSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["host_id", "alerts_enabled", "idempotency_key"],
  "properties": {
    "host_id": {"type": "string", "format": "uuid"},
    "alerts_enabled": {"type": "boolean"},
    "mem_min_available_percent": {"type": ["integer", "null"], "minimum": 5, "maximum": 50},
    "disk_max_used_percent": {"type": ["integer", "null"], "minimum": 50, "maximum": 99},
    "cpu_max_busy_percent": {"type": ["integer", "null"], "minimum": 50, "maximum": 100},
    "idempotency_key": {"type": "string", "pattern": "^[A-Za-z0-9._:-]{1,128}$"}
  }
}`

// alertSettings is the API body; a nil threshold is the default.
type alertSettings struct {
	AlertsEnabled          bool `json:"alerts_enabled"`
	MemMinAvailablePercent *int `json:"mem_min_available_percent"`
	DiskMaxUsedPercent     *int `json:"disk_max_used_percent"`
	CPUMaxBusyPercent      *int `json:"cpu_max_busy_percent"`
}

// errAlertSettingsShape is fixed. It must not include the body.
var errAlertSettingsShape = errors.New("alert settings response was not usable")

func registerAlertSettings(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name:        "get_alert_settings",
		Description: "Read a host's resource alert settings: alerts_enabled and three thresholds, each a number or null for the default. mem_min_available_percent (default 15 %, or 96 MiB on a small host; a set value is the percent alone, with no MiB floor): a problem opens when available memory stays under it for 10 minutes. disk_max_used_percent (default 90): when a mount stays at or above it for 10 minutes. cpu_max_busy_percent (default 90): when CPU stays that busy for 15 minutes. Load (2 per core for 15 minutes) has no setting. Any user who can see the host may read them. Does not contact the host.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(getAlertSettingsSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			HostID string `json:"host_id"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := requireUUID("host_id", in.HostID); err != nil {
			return badRequest(err)
		}
		payload, fail := get(ctx, api, idPath("v1", "hosts", in.HostID, "alert-settings"), nil)
		if fail != nil {
			return fail
		}
		return alertSettingsResult(payload)
	})
	addTool(server, &mcp.Tool{
		Name:        "set_alert_settings",
		Description: "Plan new resource alert settings for a host (organization owners and admins only; the API checks the role at confirm). Send alerts_enabled, and mem_min_available_percent (5–50), disk_max_used_percent (50–99) and cpu_max_busy_percent (50–100), each null or left out for the default. With a threshold set, a problem resolves only when the value is 5 points kinder, and for memory the percent alone decides (the default also keeps a 96 MiB open and 128 MiB resolve floor). Nothing changes until confirm after the user agrees: show the plan, then call confirm with the returned confirmation_id and the same idempotency_key within 10 minutes. Read the current settings first with get_alert_settings. Does not contact the host.",
		Annotations: mutatingAnnotations(false),
		InputSchema: json.RawMessage(setAlertSettingsSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			HostID                 string `json:"host_id"`
			AlertsEnabled          *bool  `json:"alerts_enabled"`
			MemMinAvailablePercent *int   `json:"mem_min_available_percent"`
			DiskMaxUsedPercent     *int   `json:"disk_max_used_percent"`
			CPUMaxBusyPercent      *int   `json:"cpu_max_busy_percent"`
			IdempotencyKey         string `json:"idempotency_key"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if in.AlertsEnabled == nil {
			// A missing value would silently turn alerts off.
			return badRequest(errors.New("alerts_enabled is required"))
		}
		settings := alertSettings{AlertsEnabled: *in.AlertsEnabled, MemMinAvailablePercent: in.MemMinAvailablePercent,
			DiskMaxUsedPercent: in.DiskMaxUsedPercent, CPUMaxBusyPercent: in.CPUMaxBusyPercent}
		if err := requireUUID("host_id", in.HostID); err != nil {
			return badRequest(err)
		}
		if err := requireIdempotencyKey(in.IdempotencyKey); err != nil {
			return badRequest(err)
		}
		if err := validateAlertSettings(settings); err != nil {
			return badRequest(err)
		}
		caller, ok := auth.CallerKeyFromContext(ctx)
		if !ok {
			return toolError("unauthorized", "missing caller")
		}
		payload, fail := get(ctx, api, idPath("v1", "hosts", in.HostID), nil)
		if fail != nil {
			return fail
		}
		var host struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(payload, &host) != nil || host.Name == "" {
			return toolError("error", "host response was not usable")
		}
		// Whether the caller is an org owner or admin is decided by the API at confirm.
		id, expires, err := pendingAlertSettings.add(caller, in.IdempotencyKey, alertSettingsPlan{hostID: in.HostID, host: host.Name, settings: settings})
		if err != nil {
			return toolError("unavailable", err.Error())
		}
		out, _ := json.Marshal(map[string]any{
			"confirmation_id": id,
			"plan":            alertPlanText(summaryField(host.Name), settings) + ". Ask the user, then call confirm with this confirmation_id and the same idempotency_key.",
			"expires_at":      expires.UTC().Format(time.RFC3339),
		})
		return finishConfirmation(out, nil)
	})
}

// alertSettingsPlan is one pending set_alert_settings call.
type alertSettingsPlan struct {
	hostID   string
	host     string
	settings alertSettings
}

// pendingAlertSettings holds set_alert_settings plans for 10 minutes, 10 per caller.
var pendingAlertSettings = newLocalPlans[alertSettingsPlan](10*time.Minute, "alert settings ")

// alertPlanText is the plan sentence, for example "Set alerts on pi1: on,
// memory available under 10 %, disk at 90 % (default), CPU at 90 % (default)".
func alertPlanText(host string, s alertSettings) string {
	if !s.AlertsEnabled {
		return "Turn resource alerts off on " + host + "; thresholds kept: " + thresholdsText(s)
	}
	return "Set resource alerts on " + host + ": " + thresholdsText(s)
}

// confirmAlertSettings performs a pending set_alert_settings plan. handled
// is false when id is not one of this server's plans, so confirm asks the API.
func confirmAlertSettings(ctx context.Context, api *apiclient.Client, id, key string) (res *mcp.CallToolResult, handled bool) {
	caller, ok := auth.CallerKeyFromContext(ctx)
	if !ok {
		return nil, false
	}
	plan, found, fail := pendingAlertSettings.take(caller, id, key)
	if !found {
		return nil, false
	}
	if fail != nil {
		return fail, true
	}
	status, payload, fail := putStatus(ctx, api, idPath("v1", "hosts", plan.hostID, "alert-settings"), plan.settings)
	if fail != nil {
		return fail, true
	}
	if status != http.StatusOK {
		return toolError("error", errAlertSettingsShape.Error()), true
	}
	return alertSettingsResult(payload), true
}

func validateAlertSettings(s alertSettings) error {
	check := func(name string, v *int, lo, hi int) error {
		if v != nil && (*v < lo || *v > hi) {
			return fmt.Errorf("%s must be %d–%d or null", name, lo, hi)
		}
		return nil
	}
	if err := check("mem_min_available_percent", s.MemMinAvailablePercent, 5, 50); err != nil {
		return err
	}
	if err := check("disk_max_used_percent", s.DiskMaxUsedPercent, 50, 99); err != nil {
		return err
	}
	return check("cpu_max_busy_percent", s.CPUMaxBusyPercent, 50, 100)
}

// alertSettingsPayload reports whether payload is exactly an alert
// settings object (a confirmed set_alert_settings plan).
func alertSettingsPayload(payload json.RawMessage) bool {
	var keys map[string]json.RawMessage
	if json.Unmarshal(payload, &keys) != nil || len(keys) != 4 {
		return false
	}
	for _, k := range []string{"alerts_enabled", "mem_min_available_percent", "disk_max_used_percent", "cpu_max_busy_percent"} {
		if _, ok := keys[k]; !ok {
			return false
		}
	}
	return true
}

// alertSettingsResult projects the four fields and says them in one line,
// for example "Alerts on: memory 15 % (default), disk 95 %, CPU 90 % (default)."
func alertSettingsResult(payload json.RawMessage) *mcp.CallToolResult {
	var s alertSettings
	if !alertSettingsPayload(payload) || json.Unmarshal(payload, &s) != nil {
		return toolError("error", errAlertSettingsShape.Error())
	}
	out, _ := json.Marshal(s)
	return present(out, summarizeAlertSettings(s))
}

func summarizeAlertSettings(s alertSettings) string {
	if !s.AlertsEnabled {
		return "Alerts off for this host; thresholds kept: " + thresholdsText(s) + "."
	}
	return "Alerts on: " + thresholdsText(s) + "."
}

// thresholdsText is "memory available under 15 % (default), disk at 90 %
// (default), CPU at 90 % (default)".
func thresholdsText(s alertSettings) string {
	part := func(label string, v *int, def int) string {
		if v == nil {
			return label + " " + strconv.Itoa(def) + " % (default)"
		}
		return label + " " + strconv.Itoa(*v) + " %"
	}
	return part("memory available under", s.MemMinAvailablePercent, 15) + ", " +
		part("disk at", s.DiskMaxUsedPercent, 90) + ", " + part("CPU at", s.CPUMaxBusyPercent, 90)
}
