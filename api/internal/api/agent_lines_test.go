package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/more-than-code/bot-api-server/internal/agentws"
)

// agent-protocol v1 caps each log line at 2048 characters, not bytes. A line
// of multi-byte text within that cap is a valid agent answer.
func TestBoundLinesCountsCharacters(t *testing.T) {
	cjk := strings.Repeat("日", 2048) // 2048 characters, 6144 bytes
	if _, ok := boundLines([]string{cjk}, maxLogLines); !ok {
		t.Fatal("a 2048-character line of CJK text was refused")
	}
	if _, ok := boundLines([]string{cjk + "日"}, maxLogLines); ok {
		t.Fatal("a 2049-character line was accepted")
	}
	// The total cap (64 KiB) is counted the same way: 32 lines of 2048 characters fit.
	full := make([]string, 32)
	for i := range full {
		full[i] = strings.Repeat("é", 2048)
	}
	if _, ok := boundLines(full, maxLogLines); !ok {
		t.Fatal("64 KiB characters of accented text were refused")
	}
	if _, ok := boundLines(append(full, "x"), maxLogLines); ok {
		t.Fatal("more than 64 KiB characters were accepted")
	}
}

// The reported case: after a restart the container log holds a long
// non-ASCII line, and Logs showed "invalid agent result" instead of the lines.
func TestImageLogsAcceptsLongMultibyteLine(t *testing.T) {
	line := strings.Repeat("服务已重启", 200) // 1000 characters, 3000 bytes
	payload, _ := json.Marshal(map[string]any{"container": "p-web", "lines": []string{"ok", line}, "truncated": false})
	got := takeLogs(agentws.LiveResult{Payload: payload}, nil, nil)
	if got.request {
		t.Fatalf("logs refused: %s %s", got.code, got.msg)
	}
	if len(got.logs.Lines) != 2 || got.logs.Lines[1] != line {
		t.Fatalf("lines = %d, want the long line back unchanged", len(got.logs.Lines))
	}
}
