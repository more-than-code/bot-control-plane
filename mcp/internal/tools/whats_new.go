package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	changelog "github.com/more-than-code/bot-control-plane"
)

const (
	whatsNewDefault = 3
	whatsNewMax     = 10
)

const whatsNewSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "count": {"type": "integer", "minimum": 1, "maximum": 10, "default": 3}
  }
}`

// registerWhatsNew adds whats_new (api-v1 v1.34 item 3): the newest
// CHANGELOG.md entries, read from this server's build. No API call.
func registerWhatsNew(server *mcp.Server) {
	addTool(server, &mcp.Tool{
		Name:        "whats_new",
		Description: "What changed in Deploybot: the newest releases of this server (version, date and the list of changes), newest first. count is 1–10 (default 3). Use it when the user asks what is new, or when a tool you expected is missing or behaves differently. Reads the release notes built into this server. Does not contact any host or call the API.",
		Annotations: readOnlyAnnotations(false),
		InputSchema: json.RawMessage(whatsNewSchema),
	}, func(_ context.Context, raw json.RawMessage) *mcp.CallToolResult {
		var in struct {
			Count *int `json:"count"`
		}
		if err := decodeArgs(raw, &in); err != nil {
			return badRequest(err)
		}
		count := whatsNewDefault
		if in.Count != nil {
			count = *in.Count
		}
		if count < 1 || count > whatsNewMax {
			return badRequest(errors.New("count must be 1–10"))
		}
		return whatsNewResult(changelog.Version(), changelog.Entries(count))
	})
}

func whatsNewResult(version string, entries []changelog.Entry) *mcp.CallToolResult {
	payload, err := json.Marshal(struct {
		Version string            `json:"version"`
		Entries []changelog.Entry `json:"entries"`
	}{version, entries})
	if err != nil {
		return toolError("error", "internal error")
	}
	return present(payload, summarizeWhatsNew(version, entries))
}

// whatsNewItemRunes caps one change line in the text summary.
const whatsNewItemRunes = 300

// summarizeWhatsNew is one block per release: "v0.2.37 (2026-11-17)"
// followed by its bullets, each field clamped like the other summaries.
func summarizeWhatsNew(version string, entries []changelog.Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Deploybot server v%s", summaryField(version))
	for _, e := range entries {
		fmt.Fprintf(&b, "\n\nv%s (%s)", summaryField(e.Version), summaryField(e.Date))
		for _, item := range e.Items {
			b.WriteString("\n- " + clampField(item, whatsNewItemRunes))
		}
	}
	return b.String()
}

// Instructions is the MCP server instructions text (spec-update-awareness
// §2.3): what Deploybot is, then the version and up to 3 new items.
func Instructions() string {
	text := "Deploybot deploys and monitors services on the user's own hosts; these tools act as the signed-in user, with the same permissions as the Deploybot console. " +
		"Privileged steps are the person's, never the agent's: a host's runner.toml, registry and GitHub logins, secret values and files, host access and domains. For those, results carry console_url; post that link in the chat for the person to open and confirm."
	latest, ok := changelog.Latest()
	if !ok {
		return text
	}
	items := latest.Items
	if len(items) > 3 {
		items = items[:3]
	}
	parts := make([]string, len(items))
	for i, item := range items {
		parts[i] = strings.TrimRight(item, ".")
	}
	return fmt.Sprintf("%s Server v%s (%s). New in this release: %s. Call whats_new for the full list.", text, latest.Version, latest.Date, strings.Join(parts, "; "))
}
