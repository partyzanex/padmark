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
)

const storagePostgres = "postgres"

// dbOpener creates a *bun.DB from a DSN without connecting.
type dbOpener func(dsn string) (*bun.DB, error)

func openPostgresDB(dsn string) (*bun.DB, error) {
	sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn)))

	return bun.NewDB(sqldb, pgdialect.New()), nil
}

func openSQLiteDB(dsn string) (*bun.DB, error) {
	sqldb, err := sql.Open(sqliteshim.DriverName(), withSQLitePragmas(dsn))
	if err != nil {
		return nil, fmt.Errorf("sql open sqlite: %w", err)
	}

	return bun.NewDB(sqldb, sqlitedialect.New()), nil
}

// withSQLitePragmas appends the connection settings padmark relies on (modernc syntax), each
// only when not already present in the DSN:
//   - foreign_keys(1):    enforce FK constraints (off by default) so ON DELETE CASCADE works.
//   - busy_timeout(5000): wait up to 5s for a lock instead of failing immediately with
//     SQLITE_BUSY, so contending writers queue rather than error out.
//   - journal_mode(WAL):  readers don't block the writer (better read-heavy concurrency).
//     Ignored for in-memory databases.
//   - _txlock=immediate:  take the write lock when the transaction opens, not when it first
//     writes. Not a pragma but a driver option, hence the different key shape.
//
// The last one is what makes the second one work for this codebase's transactions. Every
// RunInTx here reads before it writes (count-then-insert for API tokens, select-then-update for
// invite and reveal consume). With SQLite's default deferred BEGIN such a transaction holds a
// read lock by the time it tries to write, and SQLite refuses to promote it: promoting while
// holding a read lock risks deadlock, so it returns SQLITE_BUSY — or SQLITE_BUSY_SNAPSHOT once
// another writer has committed past the snapshot — *without* consulting the busy handler.
// busy_timeout is therefore powerless exactly where it looks most needed, and the failure
// surfaces to the caller as a 500.
//
// Measured with 8 concurrent writers doing 200 read-then-write transactions each: deferred lost
// 1480 of 1600 transactions (1186 SQLITE_BUSY, 294 SQLITE_BUSY_SNAPSHOT), immediate lost none.
// See TestSQLiteConcurrentWriters_NoLockContention.
//
// The cost is that a read-only transaction would now serialize behind writers too. Nothing here
// opens one — RunInTx is used for read-modify-write only — but a future read-only transaction
// should use a plain query or its own connection rather than reverting this.
func withSQLitePragmas(dsn string) string {
	pragmas := []struct{ key, value string }{
		{"_pragma=foreign_keys", "_pragma=foreign_keys(1)"},
		{"_pragma=busy_timeout", "_pragma=busy_timeout(5000)"},
		{"_pragma=journal_mode", "_pragma=journal_mode(WAL)"},
		{"_txlock=", "_txlock=immediate"},
	}

	for i := range pragmas {
		if strings.Contains(dsn, pragmas[i].key) {
			continue
		}

		if strings.Contains(dsn, "?") {
			dsn += "&" + pragmas[i].value
		} else {
			dsn += "?" + pragmas[i].value
		}
	}

	return dsn
}

func openDB(ctx context.Context, storage, dsn string) (*bun.DB, error) {
	openers := map[string]dbOpener{
		storagePostgres: openPostgresDB,
		"sqlite":        openSQLiteDB,
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

// initPostgresStorage runs postgres migrations and returns a ready-to-use note repository.
func initPostgresStorage(ctx context.Context, db *bun.DB, log *slog.Logger) (*postgres.NoteRepository, error) {
	results, err := postgres.Migrate(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("postgres migrate: %w", err)
	}

	logMigrations(ctx, log, results)

	return postgres.NewNoteRepository(db), nil
}

// initSQLiteStorage runs sqlite migrations and returns a ready-to-use note repository.
func initSQLiteStorage(ctx context.Context, db *bun.DB, log *slog.Logger) (*sqlite.NoteRepository, error) {
	results, err := sqlite.Migrate(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("sqlite migrate: %w", err)
	}

	logMigrations(ctx, log, results)

	return sqlite.NewNoteRepository(db), nil
}

// newPostgresLinkRepository returns a ready-to-use links repository for the postgres backend.
// Migrations for the links table already ran as part of the storage init (same db handle, same
// migration set), so this only needs to construct the repository, not migrate again.
func newPostgresLinkRepository(db *bun.DB) *postgres.LinkRepository {
	return postgres.NewLinkRepository(db, slog.Default())
}

// newSQLiteLinkRepository returns a ready-to-use links repository for the sqlite backend.
func newSQLiteLinkRepository(db *bun.DB) *sqlite.LinkRepository {
	return sqlite.NewLinkRepository(db, slog.Default())
}

// newPostgresNotesChecker returns a links.NotesChecker for the postgres backend — the same note
// repository the storage init built, reconstructed here typed to expose Exists. notes.Storage
// deliberately doesn't declare Exists — it's not something notes.Manager itself needs — so
// links.Manager's cross-table check needs its own, separately typed handle onto the same underlying
// table rather than widening notes.Storage for a consumer it doesn't otherwise serve. Repositories
// are stateless wrappers over *bun.DB, so constructing a second one is free.
func newPostgresNotesChecker(db *bun.DB) *postgres.NoteRepository {
	return postgres.NewNoteRepository(db)
}

// newSQLiteNotesChecker returns a links.NotesChecker for the sqlite backend — see newPostgresNotesChecker.
func newSQLiteNotesChecker(db *bun.DB) *sqlite.NoteRepository {
	return sqlite.NewNoteRepository(db)
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

// minIPv4TrustedPrefix / minIPv6TrustedPrefix are the shortest prefix lengths a trusted-proxy
// entry may have before overlyBroadProxyCIDRs flags it. They are set to the shortest prefixes
// with a real-world use: 10.0.0.0/8 is the standard private IPv4 range, and fc00::/7 is the
// IPv6 unique-local range. Anything shorter spans a slice of the public internet no proxy
// fleet occupies, so it can only be a mistake.
const (
	minIPv4TrustedPrefix = 8
	minIPv6TrustedPrefix = 7
)

// overlyBroadProxyCIDRs returns the trusted-proxy entries wide enough to be a configuration
// error, formatted for logging. Trusting an address means trusting the X-Forwarded-For and
// X-Real-IP headers it sends; trusting a range that covers arbitrary clients therefore lets
// any client name its own address. X-Forwarded-For survives this (an all-trusted chain
// collapses back to RemoteAddr), but X-Real-IP is a single value with no chain to walk, so it
// is taken verbatim — which lets a client pick a fresh rate-limit bucket per request and slip
// the TOTP login cap entirely.
//
// The threshold is deliberately far below any legitimate proxy range (published CDN ranges run
// /12 and narrower) so a correct configuration never trips it.
func overlyBroadProxyCIDRs(proxies []*net.IPNet) []string {
	var broad []string

	for _, cidr := range proxies {
		ones, bits := cidr.Mask.Size()

		limit := minIPv6TrustedPrefix
		if bits == net.IPv4len*8 {
			limit = minIPv4TrustedPrefix
		}

		if ones < limit {
			broad = append(broad, cidr.String())
		}
	}

	return broad
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
