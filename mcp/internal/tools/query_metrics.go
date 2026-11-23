package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/more-than-code/bot-control-plane/mcp/internal/apiclient"
)

const queryMetricsSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["host_id", "metric"],
  "properties": {
    "host_id": {"type": "string", "format": "uuid"},
    "metric": {"type": "string", "enum": ["cpu", "mem", "disk", "diskio", "net", "system", "temp"]},
    "from": {"type": "string", "format": "date-time"},
    "to": {"type": "string", "format": "date-time"},
    "step": {"type": "integer", "minimum": 10, "maximum": 3600},
    "path": {"type": "string", "minLength": 1, "maxLength": 1024}
  }
}`

func registerQueryMetrics(server *mcp.Server, api *apiclient.Client) {
	addTool(server, &mcp.Tool{
		Name:        "query_metrics",
		Description: "Read stored metrics for a host. metric is one of cpu, mem, disk, diskio, net, system, temp. Optional from and to are RFC3339 UTC timestamps (default last 1 hour; span at most 31 days). Optional step is the bucket size in seconds (10–3600). mem has used_percent, used, total and available (bytes). disk is one mount: optional path (default /; an unknown mount gives an empty series). Does not contact the host.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(queryMetricsSchema),
	}, func(ctx context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			HostID string  `json:"host_id"`
			Metric string  `json:"metric"`
			From   *string `json:"from"`
			To     *string `json:"to"`
			Step   *int    `json:"step"`
			Path   *string `json:"path"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		if err := validateMetrics(in.HostID, in.Metric, in.From, in.To, in.Step); err != nil {
			return badRequest(err)
		}
		if in.Path != nil && (in.Metric != "disk" || *in.Path == "" || len(*in.Path) > 1024 || strings.ContainsAny(*in.Path, "\x00\r\n")) {
			return badRequest(errors.New("path is a disk mount such as /data, for metric disk only"))
		}
		query := url.Values{}
		query.Set("m", in.Metric)
		if in.From != nil {
			query.Set("from", *in.From)
		}
		if in.To != nil {
			query.Set("to", *in.To)
		}
		if in.Step != nil {
			query.Set("step", strconv.Itoa(*in.Step))
		}
		if in.Path != nil {
			query.Set("path", *in.Path)
		}
		path := idPath("v1", "hosts", in.HostID, "metrics")
		payload, fail := get(ctx, api, path, query)
		if fail != nil {
			return fail
		}
		return present(payload, summarizeMetrics(in.Metric, payload))
	})
}

func validateMetrics(hostID, metric string, from, to *string, step *int) error {
	if err := requireUUID("host_id", hostID); err != nil {
		return err
	}
	if !metricNames[metric] {
		return errors.New("metric must be one of cpu, mem, disk, diskio, net, system, temp")
	}
	fromVal, err := optionalTime("from", from)
	if err != nil {
		return err
	}
	toVal, err := optionalTime("to", to)
	if err != nil {
		return err
	}
	if err := requireSpan(fromVal, toVal, maxMetricSpan); err != nil {
		return err
	}
	if step != nil && (*step < 10 || *step > 3600) {
		return errors.New("step must be between 10 and 3600")
	}
	return nil
}
