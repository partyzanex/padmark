package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"
	"github.com/urfave/cli/v3"

	adaptershttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/infra/render"
	"github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

const (
	shutdownTimeout   = 10 * time.Second
	readHeaderTimeout = 5 * time.Second
)

// NewApp builds the CLI application with all flags configured.
func NewApp() *cli.Command {
	return &cli.Command{
		Name:  "padmark",
		Usage: "Markdown notes HTTP service",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    FlagAddr,
				Sources: cli.EnvVars(EnvAddr),
				Value:   DefaultAddr,
				Usage:   "HTTP listen address",
			},
			&cli.StringFlag{
				Name:    FlagDSN,
				Sources: cli.EnvVars(EnvDSN),
				Value:   DefaultDSN,
				Usage:   "SQLite DSN (file path or :memory:)",
			},
			&cli.StringFlag{
				Name:    FlagLogLevel,
				Sources: cli.EnvVars(EnvLogLevel),
				Value:   DefaultLogLevel,
				Usage:   "Log level: debug, info, warn, error",
			},
			&cli.StringFlag{
				Name:    FlagLogFormat,
				Sources: cli.EnvVars(EnvLogFormat),
				Value:   DefaultLogFormat,
				Usage:   "Log format: json, text",
			},
			&cli.StringFlag{
				Name:    FlagAuthTokens,
				Sources: cli.EnvVars(EnvAuthTokens),
				Usage:   "Comma-separated Bearer tokens for write endpoints (empty = no auth)",
			},
		},
		Action: action,
	}
}

// Run starts the application with the provided context and os.Args.
func Run(ctx context.Context) error {
	return NewApp().Run(ctx, os.Args) //nolint:wrapcheck // top-level delegation, cli errors are self-descriptive
}

func action(ctx context.Context, cmd *cli.Command) error {
	// 1. Logger
	log := newLogger(cmd.String(FlagLogLevel), cmd.String(FlagLogFormat))

	// 2. Storage
	db, err := openDB(ctx, cmd.String(FlagDSN))
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}

	defer func() {
		closeErr := db.Close()
		if closeErr != nil {
			log.ErrorContext(ctx, "close db", "err", closeErr)
		}
	}()

	err = sqlite.Migrate(ctx, db)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	repo := sqlite.NewRepository(db)

	// 3. Renderer
	renderer := render.NewRenderer()

	// 4. Manager
	manager := notes.NewManager(repo, renderer, log)

	// 5. Handler + Router
	handler := adaptershttp.NewHandler(manager, log).WithPinger(db.DB)

	var tokens []string

	if raw := cmd.String(FlagAuthTokens); raw != "" {
		for part := range strings.SplitSeq(raw, ",") {
			tok := strings.TrimSpace(part)
			if tok != "" {
				tokens = append(tokens, tok)
			}
		}
	}

	router := adaptershttp.NewRouter(handler, tokens)

	// 6. Server
	srv := &http.Server{
		Addr:              cmd.String(FlagAddr),
		Handler:           router,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	stopCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)

	go func() {
		log.InfoContext(ctx, "server started", "addr", srv.Addr, "dsn", cmd.String(FlagDSN))

		serveErr := srv.ListenAndServe()
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}

		close(errCh)
	}()

	select {
	case err = <-errCh:
		return err
	case <-stopCtx.Done():
	}

	// stopCtx is already cancelled at this point, so we use a fresh background context
	// to give the server the full shutdownTimeout to drain in-flight requests.
	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	log.InfoContext(ctx, "shutting down")

	err = srv.Shutdown(shutCtx) //nolint:contextcheck
	if err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	return <-errCh
}

func openDB(ctx context.Context, dsn string) (*bun.DB, error) {
	sqldb, err := sql.Open(sqliteshim.DriverName(), dsn)
	if err != nil {
		return nil, fmt.Errorf("sql open: %w", err)
	}

	db := bun.NewDB(sqldb, sqlitedialect.New())

	err = db.PingContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("db ping: %w", err)
	}

	return db, nil
}

func newLogger(level, format string) *slog.Logger {
	var lvl slog.Level

	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}

	if format == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}

	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}
