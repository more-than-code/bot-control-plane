// Command deploybot-server runs the Deploybot API or MCP server:
//
//	deploybot-server api [--migrate]
//	deploybot-server mcp
//
// Called as bot-api-server or bot-mcp-server (for example through a
// symlink), it runs that server with all its arguments.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/more-than-code/bot-control-plane/api"
	"github.com/more-than-code/bot-control-plane/mcp"
)

const usage = `usage: deploybot-server api [--migrate]
       deploybot-server mcp
`

func main() {
	mode, args, code := dispatch(os.Args, os.Stderr)
	switch mode {
	case "api":
		code = api.Run(context.Background(), args)
	case "mcp":
		code = mcp.Run(context.Background(), args)
	}
	os.Exit(code)
}

// dispatch picks the server mode from the program name or the first
// argument. An empty mode means usage was printed and code is the exit code.
func dispatch(argv []string, stderr io.Writer) (mode string, args []string, code int) {
	if len(argv) > 0 {
		switch filepath.Base(argv[0]) {
		case "bot-api-server":
			return "api", argv[1:], 0
		case "bot-mcp-server":
			return "mcp", argv[1:], 0
		}
	}
	if len(argv) < 2 {
		fmt.Fprint(stderr, usage)
		return "", nil, 2
	}
	switch argv[1] {
	case "api", "mcp":
		return argv[1], argv[2:], 0
	case "-h", "--help":
		fmt.Fprint(stderr, usage)
		return "", nil, 0
	default:
		fmt.Fprint(stderr, usage)
		return "", nil, 2
	}
}
