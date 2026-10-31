package server

import (
	"github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clifactory"
)

// newCommand builds a *cli.Command with the Name/Usage/Flags/Action fields filled in.
// Every optional cli.Command field is unused in padmark (no aliases, no custom usage template,
// no pre/post hooks, no per-command error handler), so clifactory keeps their zero values as the
// "feature disabled" signal. See NewApp in app.go for the same convention on the root command.
func newCommand(name, usage string, flags []cli.Flag, action cli.ActionFunc) *cli.Command {
	return clifactory.Command(func(command *cli.Command) {
		command.Name = name
		command.Usage = usage
		command.Flags = flags
		command.Action = action
	})
}
