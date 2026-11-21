// Command padmark-cli is the command-line client for a Padmark server.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/partyzanex/padmark/internal/adapters/cli"
)

func main() {
	os.Exit(run(os.Args))
}

// run runs the CLI with args and returns the exit code. It is a different function from main,
// so the deferred stop runs before os.Exit, and a test can call it.
func run(args []string) int {
	// SIGINT and SIGTERM cancel the context and do not stop the process at once. Thus a command
	// such as download can remove its temporary and reserved files before the process exits.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := cli.NewApp().Run(ctx, args)
	if err != nil {
		// The process exits next: a failed write to stderr has no other place to go.
		_, _ = fmt.Fprintln(os.Stderr, "error:", err)

		return 1
	}

	return 0
}
