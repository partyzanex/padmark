// Command padmark-mcp runs a Model Context Protocol server over stdio for a Padmark server.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/partyzanex/padmark/internal/adapters/mcp"
)

func main() {
	app := mcp.NewApp()

	err := app.Run(context.Background(), os.Args)
	if err != nil {
		// stderr only: stdout belongs to the MCP protocol.
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
