package server

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/urfave/cli/v3"
)

func migrateCommand() *cli.Command {
	return &cli.Command{
		Name:   encryptionMigrate,
		Usage:  "Apply pending database migrations and exit",
		Flags:  migrateFlags(),
		Action: migrateAction,
		// Zero values for every unused optional field — see app.go's comment on the same pattern.
		Aliases:                         nil,
		UsageText:                       "",
		ArgsUsage:                       "",
		Version:                         "",
		Description:                     "",
		DefaultCommand:                  "",
		Category:                        "",
		Commands:                        nil,
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

func migrateFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    FlagStorage,
			Sources: cli.EnvVars(EnvStorage),
			Value:   DefaultStorage,
			Usage:   "Storage backend: sqlite, postgres",
		},
		&cli.StringFlag{
			Name:    FlagDSN,
			Sources: cli.EnvVars(EnvDSN),
			Value:   DefaultDSN,
			Usage:   "Database DSN",
		},
	}
}

func migrateAction(ctx context.Context, cmd *cli.Command) error {
	storage := cmd.String(FlagStorage)
	dsn := cmd.String(FlagDSN)

	log := slog.Default()

	db, err := openDB(ctx, storage, dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}

	defer func() {
		closeErr := db.Close()
		if closeErr != nil {
			log.ErrorContext(ctx, "close db", "err", closeErr)
		}
	}()

	log.InfoContext(ctx, "running migrations", "storage", storage)

	if storage == storagePostgres {
		_, err = initPostgresStorage(ctx, db, log)
	} else {
		_, err = initSQLiteStorage(ctx, db, log)
	}

	if err != nil {
		return err
	}

	log.InfoContext(ctx, "migrations applied successfully")

	return nil
}
