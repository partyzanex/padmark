package server

import (
	"github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/buildinfo"
	"github.com/partyzanex/padmark/internal/clifactory"
)

// NewApp builds the CLI application with all subcommands configured.
func NewApp() *cli.Command {
	return clifactory.Command(func(command *cli.Command) {
		command.Name = "padmark"
		command.Usage = "Markdown notes HTTP service"
		command.Version = buildinfo.Version()
		command.Commands = []*cli.Command{
			serveCommand(),
			migrateCommand(),
			encryptionCommand(),
		}
		// Run serve when no subcommand is given for backwards compatibility.
		command.Action = serverAction
		command.Flags = appFlags()
	})
}
