// Command deploybot is the user CLI: deploybot login|logout.
package main

import (
	"os"

	"github.com/more-than-code/bot-control-plane/mcp/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
