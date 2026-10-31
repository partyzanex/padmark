package server

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/partyzanex/padmark/internal/clifactory"
	"github.com/urfave/cli/v3"
)

func migrateCommand() *cli.Command {
	return clifactory.Command(func(command *cli.Command) {
		command.Name = encryptionMigrate
		command.Usage = "Apply pending database migrations and exit"
		command.Flags = migrateFlags()
		command.Action = migrateAction
	})
}

func migrateFlags() []cli.Flag {
	return []cli.Flag{
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagStorage
			flag.Sources = cli.EnvVars(EnvStorage)
			flag.Value = DefaultStorage
			flag.Usage = "Storage backend: sqlite, postgres"
		}),
		clifactory.StringFlag(func(flag *cli.StringFlag) {
			flag.Name = FlagDSN
			flag.Sources = cli.EnvVars(EnvDSN)
			flag.Value = DefaultDSN
			flag.Usage = "Database DSN"
		}),
		skipMigrationBackupFlag(),
	}
}

func migrateAction(ctx context.Context, cmd *cli.Command) error {
	storage := cmd.String(FlagStorage)
	dsn := cmd.String(FlagDSN)

	log := slog.Default()

	db, err := openDB(ctx, storage, dsn, log)
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
		_, err = initSQLiteStorage(ctx, db, log, migrationOptions{dsn: dsn, skipBackup: cmd.Bool(FlagSkipMigrationBackup)})
	}

	if err != nil {
		return fmt.Errorf("init sqlite storage: %w", err)
	}

	log.InfoContext(ctx, "migrations applied successfully")

	return nil
}
