package cmd

import (
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
		Action: serverAction,
		Flags:  appFlags(),
	}
}
