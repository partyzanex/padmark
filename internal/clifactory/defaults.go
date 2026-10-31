// Package clifactory initializes CLI commands and flags with explicit defaults.
package clifactory

import cli "github.com/urfave/cli/v3"

// Command applies configuration to the CLI library's zero defaults.
func Command(configure func(*cli.Command)) *cli.Command {
	value := &cli.Command{
		Name: "", Aliases: nil, Usage: "", UsageText: "", ArgsUsage: "", Version: "", Description: "",
		DefaultCommand: "", Category: "", Commands: nil, Flags: nil, HideHelp: false, HideHelpCommand: false,
		HideVersion: false, EnableShellCompletion: false, ShellCompletionCommandName: "", ShellComplete: nil,
		ConfigureShellCompletionCommand: nil, Before: nil, After: nil, ArgValidator: nil, Action: nil,
		CommandNotFound: nil, OnUsageError: nil, InvalidFlagAccessHandler: nil, Hidden: false, Deprecated: "",
		Authors: nil, Copyright: "", Reader: nil, Writer: nil, ErrWriter: nil, ExitErrHandler: nil,
		Metadata: nil, ExtraInfo: nil, CustomRootCommandHelpTemplate: "", SliceFlagSeparator: "",
		DisableSliceFlagSeparator: false, MapFlagKeyValueSeparator: "", UseShortOptionHandling: false,
		Suggest: false, AllowExtFlags: false, SkipFlagParsing: false, CustomHelpTemplate: "",
		PrefixMatchCommands: false, SuggestCommandFunc: nil, MutuallyExclusiveFlags: nil, Arguments: nil,
		ReadArgsFromStdin: false, StopOnNthArg: nil,
	}
	configure(value)

	return value
}

// StringFlag applies configuration to the CLI library's zero defaults.
func StringFlag(configure func(*cli.StringFlag)) *cli.StringFlag {
	value := &cli.StringFlag{
		Name: "", Category: "", DefaultText: "", HideDefault: false, Usage: "",
		Sources: cli.ValueSourceChain{Chain: nil}, Required: false, Hidden: false, Deprecated: "",
		Local: false, Value: "", Destination: nil, Aliases: nil, TakesFile: false, Action: nil,
		Config: cli.StringConfig{TrimSpace: false}, OnlyOnce: false, Validator: nil, ValidateDefaults: false,
	}
	configure(value)

	return value
}

// BoolFlag applies configuration to the CLI library's zero defaults.
func BoolFlag(configure func(*cli.BoolFlag)) *cli.BoolFlag {
	value := &cli.BoolFlag{
		Name: "", Category: "", DefaultText: "", HideDefault: false, Usage: "",
		Sources: cli.ValueSourceChain{Chain: nil}, Required: false, Hidden: false, Deprecated: "",
		Local: false, Value: false, Destination: nil, Aliases: nil, TakesFile: false, Action: nil,
		Config: cli.BoolConfig{Count: nil}, OnlyOnce: false, Validator: nil, ValidateDefaults: false,
	}
	configure(value)

	return value
}

// IntFlag applies configuration to the CLI library's zero defaults.
func IntFlag(configure func(*cli.IntFlag)) *cli.IntFlag {
	return integerFlag(configure)
}

// Int64Flag applies configuration to the CLI library's zero defaults.
func Int64Flag(configure func(*cli.Int64Flag)) *cli.Int64Flag {
	return integerFlag(configure)
}

func integerFlag[T int | int64, VC cli.ValueCreator[T, cli.IntegerConfig]](
	configure func(*cli.FlagBase[T, cli.IntegerConfig, VC]),
) *cli.FlagBase[T, cli.IntegerConfig, VC] {
	value := &cli.FlagBase[T, cli.IntegerConfig, VC]{
		Name: "", Category: "", DefaultText: "", HideDefault: false, Usage: "",
		Sources: cli.ValueSourceChain{Chain: nil}, Required: false, Hidden: false, Deprecated: "",
		Local: false, Value: 0, Destination: nil, Aliases: nil, TakesFile: false, Action: nil,
		Config: cli.IntegerConfig{Base: 0}, OnlyOnce: false, Validator: nil, ValidateDefaults: false,
	}
	configure(value)

	return value
}

// DurationFlag applies configuration to the CLI library's zero defaults.
func DurationFlag(configure func(*cli.DurationFlag)) *cli.DurationFlag {
	value := &cli.DurationFlag{
		Name: "", Category: "", DefaultText: "", HideDefault: false, Usage: "",
		Sources: cli.ValueSourceChain{Chain: nil}, Required: false, Hidden: false, Deprecated: "",
		Local: false, Value: 0, Destination: nil, Aliases: nil, TakesFile: false, Action: nil,
		Config: cli.NoConfig{}, OnlyOnce: false, Validator: nil, ValidateDefaults: false,
	}
	configure(value)

	return value
}
