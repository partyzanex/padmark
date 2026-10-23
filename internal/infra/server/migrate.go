package server

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/urfave/cli/v3"
)

func migrateCommand() *cli.Command {
	return &cli.Command{ //nolint:exhaustruct_v5 // optional fields stay at zero values = features disabled
		Name:   encryptionMigrate,
		Usage:  "Apply pending database migrations and exit",
		Flags:  migrateFlags(),
		Action: migrateAction,
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
		_, err = initSQLiteStorage(ctx, db, log)
	}

	if err != nil {
		return err
	}

	log.InfoContext(ctx, "migrations applied successfully")

	return nil
}
