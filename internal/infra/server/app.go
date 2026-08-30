package server

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
		// Unused optional fields kept as zero values: padmark has no aliases, custom shell completion,
		// pre/post hooks, custom usage template, or per-command error handler. Zeros are the correct
		// "feature disabled" signal for every field below.
		Aliases:                         nil,
		UsageText:                       "",
		ArgsUsage:                       "",
		Version:                         "",
		Description:                     "",
		DefaultCommand:                  "",
		Category:                        "",
		HideHelp:                        false,
		HideHelpCommand:                 false,
		HideVersion:                     false,
		EnableShellCompletion:           false,
		ShellCompletionCommandName:      "",
		ShellComplete:                   nil,
		ConfigureShellCompletionCommand: nil,
		Before:                          nil,
		After:                           nil,
		CommandNotFound:                 nil,
		OnUsageError:                    nil,
		InvalidFlagAccessHandler:        nil,
		Hidden:                          false,
		Authors:                         nil,
		Copyright:                       "",
		Reader:                          nil,
		Writer:                          nil,
		ErrWriter:                       nil,
		ExitErrHandler:                  nil,
		Metadata:                        nil,
		ExtraInfo:                       nil,
		CustomRootCommandHelpTemplate:   "",
		SliceFlagSeparator:              "",
		DisableSliceFlagSeparator:       false,
		MapFlagKeyValueSeparator:        "",
		UseShortOptionHandling:          false,
		Suggest:                         false,
		AllowExtFlags:                   false,
		SkipFlagParsing:                 false,
		CustomHelpTemplate:              "",
		PrefixMatchCommands:             false,
		SuggestCommandFunc:              nil,
		MutuallyExclusiveFlags:          nil,
		Arguments:                       nil,
		ReadArgsFromStdin:               false,
		StopOnNthArg:                    nil,
	}
}
