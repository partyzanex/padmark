package sqlite

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"
)

// Open connects to a SQLite database at dsn, applying WithPragmas first so every caller gets the
// same connection settings the server uses in production — repository tests and integration
// tests included. Prefer this over calling sql.Open(sqliteshim.DriverName(), dsn) directly: a raw
// DSN silently runs without foreign-key enforcement, WAL, or the immediate-transaction-lock
// setting WithPragmas documents, so a test built that way can pass or fail for reasons that have
// nothing to do with what actually happens against the pragma-tuned production connection.
func Open(dsn string) (*bun.DB, error) {
	sqldb, err := sql.Open(sqliteshim.DriverName(), WithPragmas(dsn))
	if err != nil {
		return nil, fmt.Errorf("sql open sqlite: %w", err)
	}

	return bun.NewDB(sqldb, sqlitedialect.New()), nil
}

// WithPragmas appends the connection settings padmark relies on (modernc syntax), each only when
// not already present in the DSN:
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
func WithPragmas(dsn string) string {
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
