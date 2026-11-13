package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func TestDispatch(t *testing.T) {
	tests := []struct {
		name      string
		argv      []string
		mode      string
		args      []string
		code      int
		wantUsage bool
	}{
		{name: "api", argv: []string{"deploybot-server", "api"}, mode: "api", args: []string{}},
		{name: "api migrate", argv: []string{"/usr/local/bin/deploybot-server", "api", "--migrate"}, mode: "api", args: []string{"--migrate"}},
		{name: "mcp", argv: []string{"deploybot-server", "mcp"}, mode: "mcp", args: []string{}},
		{name: "argv0 api", argv: []string{"/opt/deploybot/bin/bot-api-server", "--migrate"}, mode: "api", args: []string{"--migrate"}},
		{name: "argv0 api no args", argv: []string{"bot-api-server"}, mode: "api", args: []string{}},
		{name: "argv0 mcp", argv: []string{"./bot-mcp-server"}, mode: "mcp", args: []string{}},
		{name: "argv0 api keeps api word", argv: []string{"bot-api-server", "mcp"}, mode: "api", args: []string{"mcp"}},
		{name: "missing", argv: []string{"deploybot-server"}, code: 2, wantUsage: true},
		{name: "empty argv", argv: nil, code: 2, wantUsage: true},
		{name: "unknown", argv: []string{"deploybot-server", "web"}, code: 2, wantUsage: true},
		{name: "flag first", argv: []string{"deploybot-server", "--migrate"}, code: 2, wantUsage: true},
		{name: "help", argv: []string{"deploybot-server", "--help"}, code: 0, wantUsage: true},
		{name: "short help", argv: []string{"deploybot-server", "-h"}, code: 0, wantUsage: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			mode, args, code := dispatch(tt.argv, &stderr)
			if mode != tt.mode || code != tt.code {
				t.Fatalf("mode=%q code=%d, want mode=%q code=%d", mode, code, tt.mode, tt.code)
			}
			if tt.mode != "" && !slices.Equal(args, tt.args) {
				t.Fatalf("args=%q, want %q", args, tt.args)
			}
			gotUsage := strings.Contains(stderr.String(), "usage: deploybot-server")
			if gotUsage != tt.wantUsage {
				t.Fatalf("usage printed=%v, want %v (stderr %q)", gotUsage, tt.wantUsage, stderr.String())
			}
		})
	}
}
