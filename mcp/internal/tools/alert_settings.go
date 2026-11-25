package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

// Host alert settings (api-v1 v1.49 items 3 and 6). Since v1.60 changing
// them is the person's step: the API refuses a delegated PUT, and
// set_alert_settings returns a console link that stages the change on the
// host's Alerts card.

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
  "required": ["host_id", "alerts_enabled"],
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
		Description: "Propose new resource alert settings for a host. Only the person can change them, in the console, as an organization owner or admin there; this tool changes nothing. Send alerts_enabled, and mem_min_available_percent (5–50), disk_max_used_percent (50–99) and cpu_max_busy_percent (50–100), each null or left out for the default. With a threshold set, a problem resolves only when the value is 5 points kinder, and for memory the percent alone decides (the default also keeps a 96 MiB open and 128 MiB resolve floor). Returns current, proposed and changes, and console_url: the host's Alerts card with the change filled in, for the person to review and Save. " + linkRule + " There is no confirm step for alert settings. Read the current settings first with get_alert_settings. Does not contact the host.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(setAlertSettingsSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			HostID                 string `json:"host_id"`
			AlertsEnabled          *bool  `json:"alerts_enabled"`
			MemMinAvailablePercent *int   `json:"mem_min_available_percent"`
			DiskMaxUsedPercent     *int   `json:"disk_max_used_percent"`
			CPUMaxBusyPercent      *int   `json:"cpu_max_busy_percent"`
			// Accepted and ignored for one release (api-v1 v1.60), so an
			// agent that still sends it is not refused.
			IdempotencyKey string `json:"idempotency_key"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if in.AlertsEnabled == nil {
			// A missing value would silently turn alerts off.
			return badRequest(errors.New("alerts_enabled is required"))
		}
		proposed := alertSettings{AlertsEnabled: *in.AlertsEnabled, MemMinAvailablePercent: in.MemMinAvailablePercent,
			DiskMaxUsedPercent: in.DiskMaxUsedPercent, CPUMaxBusyPercent: in.CPUMaxBusyPercent}
		if err := requireUUID("host_id", in.HostID); err != nil {
			return badRequest(err)
		}
		if err := validateAlertSettings(proposed); err != nil {
			return badRequest(err)
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
		payload, fail = get(ctx, api, idPath("v1", "hosts", in.HostID, "alert-settings"), nil)
		if fail != nil {
			return fail
		}
		var current alertSettings
		if !alertSettingsPayload(payload) || json.Unmarshal(payload, &current) != nil {
			return toolError("error", errAlertSettingsShape.Error())
		}
		name := summaryField(host.Name)
		changes := alertChanges(current, proposed)
		result := map[string]any{"host": host.Name, "current": current, "proposed": proposed, "changes": changes}
		if len(changes) == 0 {
			result["plan"] = "Nothing to change on " + name + ": the alert settings are already " + thresholdsSummary(current) + "."
			out, _ := json.Marshal(result)
			return present(out, result["plan"].(string))
		}
		link := hostAlertsURL(api, in.HostID, current, proposed)
		plan := "Proposed for " + name + ": " + strings.Join(changes, "; ") + ". Nothing has changed: "
		if link != "" {
			plan += "post console_url for the person to review and Save."
		} else {
			plan += "ask the person to open this host's Alerts card in the console and make the change there."
		}
		result["console_url"] = link
		result["plan"] = plan
		out, _ := json.Marshal(result)
		return present(out, plan)
	})
}

// alertField is one threshold: its JSON and link name, its words and its
// default.
type alertField struct {
	link  string
	label string
	def   int
	get   func(alertSettings) *int
}

var alertFields = []alertField{
	{"alerts.mem", "memory available under", 15, func(s alertSettings) *int { return s.MemMinAvailablePercent }},
	{"alerts.disk", "disk at", 90, func(s alertSettings) *int { return s.DiskMaxUsedPercent }},
	{"alerts.cpu", "CPU at", 90, func(s alertSettings) *int { return s.CPUMaxBusyPercent }},
}

func sameThreshold(a, b *int) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func thresholdText(v *int, def int) string {
	if v == nil {
		return strconv.Itoa(def) + " % (default)"
	}
	return strconv.Itoa(*v) + " %"
}

// alertChanges lists what differs, for example "alerts on → off" or
// "disk at 90 % (default) → 95 %".
func alertChanges(from, to alertSettings) []string {
	changes := []string{}
	if from.AlertsEnabled != to.AlertsEnabled {
		changes = append(changes, "alerts "+onOff(from.AlertsEnabled)+" → "+onOff(to.AlertsEnabled))
	}
	for _, f := range alertFields {
		if a, b := f.get(from), f.get(to); !sameThreshold(a, b) {
			changes = append(changes, f.label+" "+thresholdText(a, f.def)+" → "+thresholdText(b, f.def))
		}
	}
	return changes
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// thresholdsSummary is "on: memory available under 15 % (default), …".
func thresholdsSummary(s alertSettings) string {
	return onOff(s.AlertsEnabled) + ", " + thresholdsText(s)
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
// settings object.
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
