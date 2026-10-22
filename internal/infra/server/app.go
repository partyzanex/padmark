package server

import (
	"github.com/urfave/cli/v3"
)

// NewApp builds the CLI application with all subcommands configured.
func NewApp() *cli.Command {
	return &cli.Command{ //nolint:exhaustruct_v5 // optional fields stay at zero values = features disabled
		Name:  "padmark",
		Usage: "Markdown notes HTTP service",
		Commands: []*cli.Command{
			serveCommand(),
			migrateCommand(),
			encryptionCommand(),
		},
		// Run serve when no subcommand is given for backwards compatibility.
		Action: serverAction,
		Flags:  appFlags(),
	}
}
