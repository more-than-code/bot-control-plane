package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSummarizeProblems(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"items": []any{
		map[string]any{"kind": "host_offline", "level": "needs_action", "title": "pi2-2 is offline", "opened_at": "2026-11-21T08:00:00Z", "resolved_at": nil},
		map[string]any{"kind": "deploy", "level": "heads_up", "title": "web deploy rolled back on pi1\u0007", "opened_at": "2026-11-21T09:30:00Z", "resolved_at": nil},
		map[string]any{"kind": "deploy", "level": "heads_up", "title": "old", "opened_at": "2026-11-20T09:30:00Z", "resolved_at": "2026-11-20T10:00:00Z"},
	}})
	text := summarizeProblems(payload)
	for _, want := range []string{
		"2 open problems: pi2-2 is offline since 2026-11-21 08:00 UTC (needs_action) — check the machine and its bot-agent service",
		"web deploy rolled back on pi1� since 2026-11-21 09:30 UTC (heads_up) — read deploy_history for the reason",
		"1 resolved.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary %q lacks %q", text, want)
		}
	}
	empty, _ := json.Marshal(map[string]any{"items": []any{}})
	if got := summarizeProblems(empty); got != "No open problems." {
		t.Fatalf("empty %q", got)
	}
}

// api-v1 v1.44: a host_file problem names the console tab; no file content goes over MCP.
func TestSummarizeHostFileProblem(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"items": []any{
		map[string]any{"kind": "host_file", "level": "needs_action", "title": "dd-policy on pi1 could not be restored", "opened_at": "2026-11-22T08:00:00Z", "resolved_at": nil},
	}})
	if text := summarizeProblems(payload); !strings.Contains(text, "dd-policy on pi1 could not be restored since 2026-11-22 08:00 UTC (needs_action) — only an organization owner or admin can fix it, on the host's Config files tab") {
		t.Fatalf("summary %q", text)
	}
}
