package server

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/pressly/goose/v3"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/pgdriver"
	"github.com/uptrace/bun/driver/sqliteshim"

	"github.com/partyzanex/padmark/internal/infra/storage/postgres"
	"github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

// dbOpener creates a *bun.DB from a DSN without connecting.
type dbOpener func(dsn string) (*bun.DB, error)

func openPostgresDB(dsn string) (*bun.DB, error) {
	sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn)))

	return bun.NewDB(sqldb, pgdialect.New()), nil
}

func openSQLiteDB(dsn string) (*bun.DB, error) {
	sqldb, err := sql.Open(sqliteshim.DriverName(), dsn)
	if err != nil {
		return nil, fmt.Errorf("sql open sqlite: %w", err)
	}

	return bun.NewDB(sqldb, sqlitedialect.New()), nil
}

func openDB(ctx context.Context, storage, dsn string) (*bun.DB, error) {
	openers := map[string]dbOpener{
		"postgres": openPostgresDB,
		"sqlite":   openSQLiteDB,
	}

	opener, ok := openers[storage]
	if !ok {
		return nil, fmt.Errorf("unknown storage backend %q: supported backends: postgres, sqlite", storage)
	}

	db, err := opener(dsn)
	if err != nil {
		return nil, err
	}

	err = db.PingContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("db ping: %w", err)
	}

	return db, nil
}

// storageInit runs migrations and returns a ready-to-use repository.
type storageInit func(ctx context.Context, db *bun.DB, log *slog.Logger) (notes.Storage, error)

//nolint:ireturn // multiple implementations (sqlite, postgres) require interface return
func initPostgresStorage(ctx context.Context, db *bun.DB, log *slog.Logger) (notes.Storage, error) {
	results, err := postgres.Migrate(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("postgres migrate: %w", err)
	}

	logMigrations(ctx, log, results)

	return postgres.NewNoteRepository(db), nil
}

//nolint:ireturn // multiple implementations (sqlite, postgres) require interface return
func initSQLiteStorage(ctx context.Context, db *bun.DB, log *slog.Logger) (notes.Storage, error) {
	results, err := sqlite.Migrate(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("sqlite migrate: %w", err)
	}

	logMigrations(ctx, log, results)

	return sqlite.NewNoteRepository(db), nil
}

//nolint:ireturn // multiple implementations (sqlite, postgres) require interface return
func initStorage(ctx context.Context, storage string, db *bun.DB, log *slog.Logger) (notes.Storage, error) {
	inits := map[string]storageInit{
		"postgres": initPostgresStorage,
		"sqlite":   initSQLiteStorage,
	}

	fn, ok := inits[storage]
	if !ok {
		return nil, fmt.Errorf("unknown storage backend %q: supported backends: postgres, sqlite", storage)
	}

	return fn(ctx, db, log)
}

// parseTrustedProxies parses a comma-separated list of CIDRs or bare IP addresses.
// Bare IPs are converted to /32 (IPv4) or /128 (IPv6) host CIDRs.
func parseTrustedProxies(raw string) ([]*net.IPNet, error) {
	if raw == "" {
		return nil, nil
	}

	var result []*net.IPNet

	for part := range strings.SplitSeq(raw, ",") {
		cidr := strings.TrimSpace(part)
		if cidr == "" {
			continue
		}

		if !strings.Contains(cidr, "/") {
			ip := net.ParseIP(cidr)
			if ip == nil {
				return nil, fmt.Errorf("invalid trusted proxy address: %q", cidr)
			}

			if ip.To4() != nil {
				cidr += "/32"
			} else {
				cidr += "/128"
			}
		}

		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", cidr, err)
		}

		result = append(result, ipNet)
	}

	return result, nil
}

// redactDSN removes credentials from a DSN before logging.
// URL-format DSNs (postgres://user:pass@host/db) have the password replaced with "***".
// Key-value DSNs containing "password=" are replaced entirely with "<redacted>".
// Plain file paths (sqlite) are returned as-is.
func redactDSN(dsn string) string {
	parsedURL, parseErr := url.Parse(dsn)
	if parseErr == nil && parsedURL.User != nil {
		if _, hasPassword := parsedURL.User.Password(); hasPassword {
			parsedURL.User = url.UserPassword(parsedURL.User.Username(), "***")
		}

		return parsedURL.String()
	}

	if strings.Contains(dsn, "password=") {
		return "<redacted>"
	}

	return dsn
}

func logMigrations(ctx context.Context, log *slog.Logger, results []*goose.MigrationResult) {
	applied := 0

	for _, res := range results {
		if res.Empty {
			continue
		}

		applied++

		log.InfoContext(ctx, "migration applied",
			"version", res.Source.Version,
			"file", filepath.Base(res.Source.Path),
			"duration", res.Duration,
		)
	}

	if applied == 0 {
		log.InfoContext(ctx, "migrations: already up to date")
	} else {
		log.InfoContext(ctx, "migrations: done", "applied", applied)
	}
}
