// Command padmark-server runs the Padmark service for Markdown notes and short links.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/partyzanex/padmark/internal/infra/server"
)

func main() {
	app := server.NewApp()

	err := app.Run(context.Background(), os.Args)
	if err != nil {
		// The process exits next: a failed write to stderr has no other place to go.
		_, _ = fmt.Fprintln(os.Stderr, "error:", err)

		os.Exit(1)
	}
}
