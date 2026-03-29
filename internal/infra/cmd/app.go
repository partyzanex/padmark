package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/pgdriver"
	"github.com/uptrace/bun/driver/sqliteshim"
	"github.com/urfave/cli/v3"

	adaptershttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/infra/render"
	"github.com/partyzanex/padmark/internal/infra/storage/postgres"
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
		Name:   "padmark",
		Usage:  "Markdown notes HTTP service",
		Flags:  appFlags(),
		Action: action,
	}
}

func appFlags() []cli.Flag { //nolint:funlen // declarative flag list
	return []cli.Flag{
		&cli.StringFlag{
			Name:    FlagAddr,
			Sources: cli.EnvVars(EnvAddr),
			Value:   DefaultAddr,
			Usage:   "HTTP listen address",
		},
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
			Usage:   "Database DSN (file path for sqlite, connection string for postgres)",
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
		&cli.IntFlag{
			Name:    FlagCookieMaxAge,
			Sources: cli.EnvVars(EnvCookieMaxAge),
			Value:   DefaultCookieMaxAge,
			Usage:   "Auth cookie max-age in seconds (default: 3 months)",
		},
		&cli.IntFlag{
			Name:    FlagReadTimeout,
			Sources: cli.EnvVars(EnvReadTimeout),
			Value:   DefaultReadTimeout,
			Usage:   "HTTP read timeout in seconds",
		},
		&cli.IntFlag{
			Name:    FlagMaxHeaderBytes,
			Sources: cli.EnvVars(EnvMaxHeaderBytes),
			Value:   DefaultMaxHeaderBytes,
			Usage:   "Maximum size of request headers in bytes",
		},
		&cli.IntFlag{
			Name:    FlagMaxBodyBytes,
			Sources: cli.EnvVars(EnvMaxBodyBytes),
			Value:   DefaultMaxBodyBytes,
			Usage:   "Maximum size of request body in bytes",
		},
		&cli.IntFlag{
			Name:    FlagRateLimit,
			Sources: cli.EnvVars(EnvRateLimit),
			Value:   DefaultRateLimit,
			Usage:   "Rate limit: requests per second per IP (0 = disabled)",
		},
		&cli.IntFlag{
			Name:    FlagRateBurst,
			Sources: cli.EnvVars(EnvRateBurst),
			Value:   DefaultRateBurst,
			Usage:   "Rate limit: max burst size per IP",
		},
		&cli.StringFlag{
			Name:    FlagTLSCert,
			Sources: cli.EnvVars(EnvTLSCert),
			Usage:   "Path to TLS certificate file (PEM); enables HTTPS when set together with --tls-key",
		},
		&cli.StringFlag{
			Name:    FlagTLSKey,
			Sources: cli.EnvVars(EnvTLSKey),
			Usage:   "Path to TLS private key file (PEM); enables HTTPS when set together with --tls-cert",
		},
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
	storage := cmd.String(FlagStorage)
	dsn := cmd.String(FlagDSN)

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

	repo, err := initStorage(ctx, storage, db)
	if err != nil {
		return err
	}

	// 3. Renderer
	renderer := render.NewRenderer()

	// 4. Manager
	manager := notes.NewManager(repo, renderer, log)

	// 5. Handler + Router
	handler := adaptershttp.NewHandler(manager, log).WithPinger(db.DB)
	ogenHandler := adaptershttp.NewOgenHandler(manager, db.DB, log)

	var tokens []string

	if raw := cmd.String(FlagAuthTokens); raw != "" {
		for part := range strings.SplitSeq(raw, ",") {
			tok := strings.TrimSpace(part)
			if tok != "" {
				tokens = append(tokens, tok)
			}
		}
	}

	routerOpts := adaptershttp.RouterOptions{
		CookieMaxAge: cmd.Int(FlagCookieMaxAge),
		MaxBodyBytes: cmd.Int(FlagMaxBodyBytes),
		RateLimit:    cmd.Int(FlagRateLimit),
		RateBurst:    cmd.Int(FlagRateBurst),
	}
	router := adaptershttp.NewRouter(handler, ogenHandler, tokens, routerOpts)

	// 6. Server
	srv := &http.Server{
		Addr:              cmd.String(FlagAddr),
		Handler:           router,
		ReadTimeout:       time.Duration(cmd.Int(FlagReadTimeout)) * time.Second,
		ReadHeaderTimeout: readHeaderTimeout,
		MaxHeaderBytes:    cmd.Int(FlagMaxHeaderBytes),
	}

	stopCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	tlsCert := cmd.String(FlagTLSCert)
	tlsKey := cmd.String(FlagTLSKey)

	if (tlsCert == "") != (tlsKey == "") {
		return fmt.Errorf("--tls-cert and --tls-key must both be set to enable TLS")
	}

	errCh := make(chan error, 1)

	go func() {
		log.InfoContext(ctx, "server started",
			"addr", srv.Addr, "storage", storage, "dsn", redactDSN(dsn), "tls", tlsCert != "",
		)

		var serveErr error
		if tlsCert != "" {
			serveErr = srv.ListenAndServeTLS(tlsCert, tlsKey)
		} else {
			serveErr = srv.ListenAndServe()
		}

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

	shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	log.InfoContext(ctx, "shutting down")

	err = srv.Shutdown(shutCtx) //nolint:contextcheck
	if err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	return <-errCh
}

func openDB(ctx context.Context, storage, dsn string) (*bun.DB, error) {
	var db *bun.DB

	switch storage {
	case "postgres":
		sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn)))
		db = bun.NewDB(sqldb, pgdialect.New())

	default: // sqlite
		sqldb, err := sql.Open(sqliteshim.DriverName(), dsn)
		if err != nil {
			return nil, fmt.Errorf("sql open sqlite: %w", err)
		}

		db = bun.NewDB(sqldb, sqlitedialect.New())
	}

	err := db.PingContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("db ping: %w", err)
	}

	return db, nil
}

func initStorage(ctx context.Context, storage string, db *bun.DB) (notes.Storage, error) {
	switch storage {
	case "postgres":
		err := postgres.Migrate(ctx, db)
		if err != nil {
			return nil, fmt.Errorf("postgres migrate: %w", err)
		}

		return postgres.NewRepository(db), nil

	default: // sqlite
		err := sqlite.Migrate(ctx, db)
		if err != nil {
			return nil, fmt.Errorf("sqlite migrate: %w", err)
		}

		return sqlite.NewRepository(db), nil
	}
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

// redactDSN removes credentials from a DSN before logging.
// URL-format DSNs (postgres://user:pass@host/db) have the password replaced with "***".
// Key-value DSNs containing "password=" are replaced entirely with "<redacted>".
// Plain file paths (sqlite) are returned as-is.
func redactDSN(dsn string) string {
	if u, err := url.Parse(dsn); err == nil && u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), "***")
		}

		return u.String()
	}

	if strings.Contains(dsn, "password=") {
		return "<redacted>"
	}

	return dsn
}
