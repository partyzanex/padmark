package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/pressly/goose/v3"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"

	"github.com/partyzanex/padmark/internal/infra/storage/postgres"
	"github.com/partyzanex/padmark/internal/infra/storage/sqlite"
)

const storagePostgres = "postgres"

// dbOpener creates a *bun.DB from a DSN without connecting.
type dbOpener func(dsn string) (*bun.DB, error)

// errInvalidPostgresDSN is returned for any DSN the driver cannot parse. Its text is fixed:
// url.Parse errors and pgdriver panics echo the raw DSN, password included, so no part of the
// underlying failure may reach the message. Startup is fail-hard: a bad DSN is never tolerated.
var errInvalidPostgresDSN = errors.New("invalid postgres dsn")

// openPostgresDB creates a connector from a postgres:// (or unix://) URL DSN without connecting.
// pgdriver.WithDSN panics on a parse error, so the DSN is validated first and the connector is
// built under a recover as a backstop; neither path ever includes the DSN in the returned error.
func openPostgresDB(dsn string) (db *bun.DB, err error) {
	err = validatePostgresDSN(dsn)
	if err != nil {
		return nil, err
	}

	defer func() {
		if recover() != nil {
			db, err = nil, fmt.Errorf("%w: unsupported or malformed parameter "+
				"(check sslmode, timeouts and other query options)", errInvalidPostgresDSN)
		}
	}()

	sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn)))

	return bun.NewDB(sqldb, pgdialect.New()), nil
}

// validatePostgresDSN rejects DSNs that are not well-formed URLs. Errors name the problem and,
// where safe, the host; they never contain the password or the raw string.
func validatePostgresDSN(dsn string) error {
	parsedURL, err := url.Parse(dsn)
	if err != nil {
		// The url.Error text embeds the full raw URL, so it is deliberately dropped.
		return fmt.Errorf("%w: malformed URL (check percent-encoding of user, password and host, "+
			"and that the port is numeric)", errInvalidPostgresDSN)
	}

	switch parsedURL.Scheme {
	case storagePostgres, "postgresql", "unix":
	default:
		return fmt.Errorf("%w: expected a postgres:// URL, key=value DSNs are not supported",
			errInvalidPostgresDSN)
	}

	if parsedURL.Scheme == "unix" && parsedURL.Path == "" {
		return fmt.Errorf("%w: unix socket DSN requires a socket path", errInvalidPostgresDSN)
	}

	return nil
}

func openDB(ctx context.Context, storage, dsn string) (*bun.DB, error) {
	openers := map[string]dbOpener{
		storagePostgres: openPostgresDB,
		// sqlite.Open applies sqlite.WithPragmas (foreign keys, busy_timeout, WAL,
		// _txlock=immediate) — see its doc comment for why each matters. Repository and
		// integration tests must go through the same function, not a bare sql.Open, or they
		// exercise a different connection than production ever runs.
		"sqlite": sqlite.Open,
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
	results, err := postgres.MigrateWithWarning(ctx, db, log)
	if err != nil {
		return nil, fmt.Errorf("postgres migrate: %w", err)
	}

	logMigrations(ctx, log, results)

	return postgres.NewNoteRepository(db), nil
}

// migrationOptions carries what the sqlite backup-before-migrate step needs.
type migrationOptions struct {
	// dsn is the database DSN, from which the file to back up is resolved.
	dsn string
	// skipBackup disables the backup (--skip-migration-backup).
	skipBackup bool
}

// initSQLiteStorage runs sqlite migrations, backing the database up first when any are pending
// (unless opts.skipBackup), and returns a ready-to-use note repository.
func initSQLiteStorage(
	ctx context.Context, db *bun.DB, log *slog.Logger, opts migrationOptions,
) (*sqlite.NoteRepository, error) {
	var (
		results []*goose.MigrationResult
		err     error
	)

	if opts.skipBackup {
		results, err = sqlite.Migrate(ctx, db)
	} else {
		results, err = sqlite.MigrateWithBackup(ctx, db, opts.dsn, log)
	}

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
	if parseErr != nil {
		// Never echo a string that failed to parse: it may hold an unredacted password.
		return "<unparseable dsn>"
	}

	if parsedURL.User != nil {
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
