// Command padmark-cli is the command-line client for a Padmark server.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/partyzanex/padmark/internal/adapters/cli"
)

func main() {
	app := cli.NewApp()

	err := app.Run(context.Background(), os.Args)
	if err != nil {
		// The process exits next: a failed write to stderr has no other place to go.
		_, _ = fmt.Fprintln(os.Stderr, "error:", err)

		os.Exit(1)
	}
}
