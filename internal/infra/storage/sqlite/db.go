package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
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
//
// For a file-backed dsn Open also makes sure the database file is not accessible to group/other
// before the driver touches it; see restrictDBFile for the policy. In-memory and temporary
// databases are left alone. Anything Open does to the permissions of an existing file is reported
// through log, which is required.
func Open(dsn string, log *slog.Logger) (*bun.DB, error) {
	path, create := dbFilePath(dsn)
	if path != "" {
		err := restrictDBFile(path, create, log)
		if err != nil {
			return nil, err
		}
	}

	sqldb, err := sql.Open(sqliteshim.DriverName(), WithPragmas(dsn))
	if err != nil {
		return nil, fmt.Errorf("sql open sqlite: %w", err)
	}

	return bun.NewDB(sqldb, sqlitedialect.New()), nil
}

// dbFilePerm is the mode of the database file and its sidecars: owner read/write, nothing else.
const dbFilePerm fs.FileMode = 0o600

// groupOtherBits are the permission bits that expose the database to accounts other than its owner.
const groupOtherBits fs.FileMode = 0o077

// chmod is a seam so tests can simulate a file whose mode cannot be changed (foreign owner,
// read-only mount), which a test process cannot reliably produce for real.
var chmod = os.Chmod //nolint:gochecknoglobals // test seam, never reassigned in production code

// restrictDBFile keeps the database file at path, and any sidecar next to it, owner-only. The file
// holds argon2 password hashes, sealed TOTP secrets, session, API-token and invite hashes, reveal
// tokens, session IP/UA and every encrypted content envelope; on a shared host the driver's
// default (SQLITE_DEFAULT_FILE_PERMISSIONS, 0644) would hand all of that to every other local
// account.
//
// Error strategy, split by who owns the file:
//   - A file created here (create set, path missing) is created with 0600 so the driver only ever
//     opens an existing file and never applies its own default. Creation failure is fatal
//     (fail-hard): the database is a critical dependency.
//   - A pre-existing file is the operator's: its permissions are their decision, and a v0.1.6
//     installation may legitimately hold it under another uid, on a read-only bind mount, or
//     shared with a backup user via group read. It is stat-ed first and left alone when it
//     already has no group/other bits. Otherwise a chmod to 0600 is attempted; success is logged
//     at INFO (old and new mode, so the operator knows why group/other access vanished), failure
//     at WARN and startup continues (fail-soft). The mode is never changed silently.
//
// Sidecars need no create step: SQLite's unix VFS creates -wal, -shm and -journal with the main
// file's mode, so only leftovers from a previous run are examined, under the same best-effort
// policy. Logged errors are *fs.PathError values naming the resolved file path only, never the
// rest of the DSN.
func restrictDBFile(path string, create bool, log *slog.Logger) error {
	if create {
		file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, dbFilePerm) //nolint:gosec // operator-configured DSN path
		if err != nil {
			return fmt.Errorf("create sqlite database file: %w", err)
		}

		err = file.Close()
		if err != nil {
			return fmt.Errorf("close sqlite database file: %w", err)
		}
	}

	for _, name := range append([]string{path}, sidecarPaths(path)...) {
		tightenFile(name, log)
	}

	return nil
}

// tightenFile applies the pre-existing-file policy of restrictDBFile to one file. It never fails:
// every problem is logged and startup continues.
func tightenFile(name string, log *slog.Logger) {
	info, err := os.Stat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}

	if err != nil {
		log.Warn("cannot stat sqlite database file to check its permissions", "file", name, "error", err)

		return
	}

	current := info.Mode().Perm()
	if current&groupOtherBits == 0 {
		return
	}

	err = chmod(name, dbFilePerm)
	if err != nil {
		log.Warn("sqlite database file is accessible to group/other and its permissions could not be tightened",
			"file", name, "mode", fmt.Sprintf("%04o", current), "error", err)

		return
	}

	log.Info("sqlite database file permissions tightened to owner-only",
		"file", name, "from", fmt.Sprintf("%04o", current), "to", fmt.Sprintf("%04o", dbFilePerm))
}

// sidecarPaths returns the paths of the sidecar files SQLite may keep next to the database at
// path: the WAL and its shared-memory index (journal_mode(WAL), which WithPragmas sets) and the
// rollback journal (any other journal mode).
func sidecarPaths(path string) []string {
	return []string{path + "-wal", path + "-shm", path + "-journal"}
}

// dbFilePath resolves the on-disk database path a DSN opens, mirroring how the modernc driver and
// SQLite interpret it. It returns "" for databases with no file — ":memory:", "file::memory:",
// "file:name?mode=memory", the memdb VFS and the empty name (a private temporary database) — and
// for DSN shapes it cannot resolve with confidence, which are then left to the driver unchanged.
// create reports whether the DSN lets SQLite create the file: a "file:" URI with mode=ro or
// mode=rw must keep failing on a missing file rather than silently getting an empty one.
//
// Bare DSNs ("padmark.db", "/var/lib/padmark/padmark.db?_pragma=...") are a path with an optional
// query string, which the driver strips before opening. "file:" URIs follow SQLite's URI rules:
// an optional "//" authority that must be empty or "localhost", a percent-encoded path, then an
// optional query and fragment.
func dbFilePath(dsn string) (path string, create bool) {
	uri, isURI := strings.CutPrefix(dsn, "file:")
	if isURI {
		return fileURIPath(uri)
	}

	path, _, _ = strings.Cut(dsn, "?")
	if path == "" || path == ":memory:" {
		return "", false
	}

	return path, true
}

// fileURIPath is dbFilePath for a "file:" URI, given with the "file:" scheme already removed.
func fileURIPath(uri string) (path string, create bool) {
	rest, _, _ := strings.Cut(uri, "#")
	rest, query, _ := strings.Cut(rest, "?")

	mode, vfs := uriParam(query, "mode"), uriParam(query, "vfs")
	if mode == "memory" || vfs == "memdb" {
		return "", false
	}

	if after, ok := strings.CutPrefix(rest, "//"); ok {
		authority, tail, _ := strings.Cut(after, "/")
		if authority != "" && authority != "localhost" {
			return "", false
		}

		rest = "/" + tail
	}

	path, err := url.PathUnescape(rest)
	if err != nil || path == "" || path == ":memory:" {
		return "", false
	}

	return path, mode != "ro" && mode != "rw"
}

// uriParam returns the value of key in a SQLite URI query string, or "" when it is absent. It
// splits by hand rather than with url.ParseQuery so one malformed parameter elsewhere in the DSN
// does not hide the one asked for.
func uriParam(query, key string) string {
	for pair := range strings.SplitSeq(query, "&") {
		name, value, _ := strings.Cut(pair, "=")
		if name == key {
			return value
		}
	}

	return ""
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
