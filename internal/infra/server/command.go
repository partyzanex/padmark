package server

import "github.com/urfave/cli/v3"

// newCommand builds a *cli.Command with the Name/Usage/Flags/Action fields filled in
// and every other field left at its Go zero value. Every optional cli.Command field is
// unused in padmark (no aliases, no custom usage template, no pre/post hooks, no
// per-command error handler), so zeros are the correct "feature disabled" signal —
// exhaustruct is silenced here, but we keep the explicit zero idiom in one place so
// callers don't have to repeat the rationale. See NewApp in app.go for the same
// convention applied to the root command.
func newCommand(name, usage string, flags []cli.Flag, action cli.ActionFunc) *cli.Command {
	return &cli.Command{ //nolint:exhaustruct_v5 // optional fields stay at zero values = features disabled
		Name:   name,
		Usage:  usage,
		Flags:  flags,
		Action: action,
	}
}
