package cmd

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/urfave/cli/v3"
)

func migrateCommand() *cli.Command {
	return &cli.Command{
		Name:   "migrate",
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

	_, err = initStorage(ctx, storage, db)
	if err != nil {
		return err
	}

	log.InfoContext(ctx, "migrations applied successfully")

	return nil
}
