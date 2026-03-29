package cmd

import (
	"context"

	"github.com/urfave/cli/v3"
)

// NewApp builds the CLI application with all subcommands configured.
func NewApp() *cli.Command {
	return &cli.Command{
		Name:  "padmark",
		Usage: "Markdown notes HTTP service",
		Commands: []*cli.Command{
			serveCommand(),
			migrateCommand(),
		},
		// Run serve when no subcommand is given for backwards compatibility.
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return serverAction(ctx, cmd)
		},
		Flags: appFlags(),
	}
}
