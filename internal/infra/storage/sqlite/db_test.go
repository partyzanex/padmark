package sqlite

import (
	"bytes"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithPragmas(t *testing.T) {
	const appended = "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&" +
		"_pragma=journal_mode(WAL)&_txlock=immediate"

	// Bare path: every setting appended, "?" first then "&".
	require.Equal(t, "padmark.db?"+appended, WithPragmas("padmark.db"))

	// Existing query string: everything appended with "&".
	require.Equal(t, "file:db?cache=shared&"+appended, WithPragmas("file:db?cache=shared"))

	// Per-setting idempotency: a pre-set foreign_keys is not duplicated; the others are added.
	got := WithPragmas("db?_pragma=foreign_keys(0)")
	require.Equal(t, 1, strings.Count(got, "_pragma=foreign_keys"), "foreign_keys must not be duplicated")
	require.Contains(t, got, "_pragma=busy_timeout(5000)")
	require.Contains(t, got, "_pragma=journal_mode(WAL)")
	require.Contains(t, got, "_txlock=immediate")

	// An operator who deliberately picked a different transaction mode keeps it.
	deferred := WithPragmas("db?_txlock=deferred")
	require.Equal(t, 1, strings.Count(deferred, "_txlock="), "_txlock must not be duplicated")
	require.NotContains(t, deferred, "_txlock=immediate")
}

// skipWithoutPOSIXPerms skips permission tests on Windows, where the POSIX mode bits these tests
// assert on are not how file access is controlled.
func skipWithoutPOSIXPerms(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply on windows")
	}
}

// touchDB opens dsn, runs a write so the database file (and, in WAL mode, its -wal/-shm
// sidecars) exist on disk, and returns the still-open handle's closer.
func touchDB(t *testing.T, dsn string) func() {
	t.Helper()

	return touchDBLogged(t, dsn, slog.New(slog.DiscardHandler))
}

// touchDBLogged is touchDB with an explicit logger, for tests asserting on what Open reports.
func touchDBLogged(t *testing.T, dsn string, log *slog.Logger) func() {
	t.Helper()

	db, err := Open(dsn, log)
	require.NoError(t, err)

	_, err = db.ExecContext(t.Context(), `CREATE TABLE IF NOT EXISTS perm_probe (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)

	_, err = db.ExecContext(t.Context(), `INSERT INTO perm_probe DEFAULT VALUES`)
	require.NoError(t, err)

	return func() { require.NoError(t, db.Close()) }
}

// requireOwnerOnly asserts the database file at path is exactly 0600 and that every sidecar
// SQLite may have created next to it grants nothing to group or other.
func requireOwnerOnly(t *testing.T, path string) {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "database file %s must be owner-only", path)

	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		sidecar, statErr := os.Stat(path + suffix)
		if os.IsNotExist(statErr) {
			continue
		}

		require.NoError(t, statErr)
		require.Zero(t, sidecar.Mode().Perm()&0o077, "sidecar %s must be owner-only, got %v",
			path+suffix, sidecar.Mode().Perm())
	}
}

// TestOpen_CreatesDatabaseFileOwnerOnly pins that a fresh database is not world-readable: the
// file holds password hashes, sealed TOTP secrets, token hashes and content envelopes, and the
// driver's default (SQLITE_DEFAULT_FILE_PERMISSIONS, 0644) would hand all of it to every other
// local account on the host.
func TestOpen_CreatesDatabaseFileOwnerOnly(t *testing.T) {
	skipWithoutPOSIXPerms(t)

	path := filepath.Join(t.TempDir(), "padmark.db")

	closeDB := touchDB(t, path)
	defer closeDB()

	// WAL is on, so the sidecars exist while the handle is open: the sidecar check is not vacuous.
	require.FileExists(t, path+"-wal")
	require.FileExists(t, path+"-shm")

	requireOwnerOnly(t, path)
}

// TestOpen_FileURICreatesDatabaseFileOwnerOnly covers the "file:" URI DSN shape with a query
// string, which must resolve to the same on-disk path as the bare form.
func TestOpen_FileURICreatesDatabaseFileOwnerOnly(t *testing.T) {
	skipWithoutPOSIXPerms(t)

	path := filepath.Join(t.TempDir(), "padmark.db")

	closeDB := touchDB(t, "file:"+path+"?cache=shared")
	defer closeDB()

	requireOwnerOnly(t, path)
}

// TestOpen_TightensExistingWorldReadableFile pins that a database left world-readable by an
// earlier version (or a careless restore) is tightened on the next start, sidecars included.
func TestOpen_TightensExistingWorldReadableFile(t *testing.T) {
	skipWithoutPOSIXPerms(t)

	path := filepath.Join(t.TempDir(), "padmark.db")

	// Hold the first handle open so the WAL sidecars survive; loosen everything it left behind.
	closeFirst := touchDB(t, path)

	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		chmodErr := os.Chmod(name, 0o644) //nolint:gosec // deliberately loosened to reproduce the old default
		if !os.IsNotExist(chmodErr) {
			require.NoError(t, chmodErr)
		}
	}

	closeSecond := touchDB(t, path)

	requireOwnerOnly(t, path)

	closeSecond()
	closeFirst()
}

// TestOpen_InMemoryCreatesNoFile pins that in-memory DSNs never touch the filesystem: a named
// shared-cache memory database ("file:name?mode=memory") must not leave a file called name in
// the working directory.
func TestOpen_InMemoryCreatesNoFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	for _, dsn := range []string{
		":memory:",
		"file::memory:?cache=shared",
		"file:permmem?mode=memory&cache=shared",
	} {
		closeDB := touchDB(t, dsn)
		closeDB()
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries, "in-memory DSNs must not create files")
}

// TestOpen_ReadOnlyURIDoesNotCreateFile pins that mode=ro keeps failing on a missing database
// instead of being handed an empty file by the permission pre-create.
func TestOpen_ReadOnlyURIDoesNotCreateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")

	db, err := Open("file:"+path+"?mode=ro", slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	defer func() { require.NoError(t, db.Close()) }()

	require.Error(t, db.PingContext(t.Context()))
	require.NoFileExists(t, path)
}

func TestDBFilePath(t *testing.T) {
	cases := []struct {
		dsn    string
		path   string
		create bool
	}{
		{dsn: "padmark.db", path: "padmark.db", create: true},
		{dsn: "/var/lib/padmark/padmark.db?_pragma=foreign_keys(1)", path: "/var/lib/padmark/padmark.db", create: true},
		{dsn: "file:padmark.db", path: "padmark.db", create: true},
		{dsn: "file:/var/lib/p.db?cache=shared&_txlock=immediate", path: "/var/lib/p.db", create: true},
		{dsn: "file:///var/lib/p.db", path: "/var/lib/p.db", create: true},
		{dsn: "file://localhost/var/lib/p.db", path: "/var/lib/p.db", create: true},
		{dsn: "file:/var/lib/my%20notes.db#frag", path: "/var/lib/my notes.db", create: true},
		{dsn: "file:/var/lib/p.db?mode=rwc", path: "/var/lib/p.db", create: true},
		{dsn: "file:/var/lib/p.db?mode=ro", path: "/var/lib/p.db", create: false},
		{dsn: "file:/var/lib/p.db?mode=rw", path: "/var/lib/p.db", create: false},
		{dsn: ""},
		{dsn: ":memory:"},
		{dsn: "file::memory:"},
		{dsn: "file::memory:?cache=shared"},
		{dsn: "file:named?mode=memory&cache=shared"},
		{dsn: "file:/p.db?vfs=memdb"},
		{dsn: "file:"},
		{dsn: "file://otherhost/p.db"},
	}

	for _, tc := range cases {
		path, create := dbFilePath(tc.dsn)
		require.Equal(t, tc.path, path, "path for %q", tc.dsn)
		require.Equal(t, tc.create, create, "create for %q", tc.dsn)
	}
}

// recordingLogger returns a logger that writes text records into the returned buffer.
func recordingLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}

	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// writeLooseDB creates an empty (valid, zero-page) database file with mode 0644, as an older
// version or a restore would have left it.
func writeLooseDB(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "padmark.db")
	require.NoError(t, os.WriteFile(path, nil, 0o644)) //nolint:gosec // deliberately loose to reproduce the old default
	require.NoError(t, os.Chmod(path, 0o644))          //nolint:gosec // defeat the umask

	return path
}

// TestOpen_NewFileLogsNothing pins that a freshly created 0600 file is not reported as tightened.
func TestOpen_NewFileLogsNothing(t *testing.T) {
	skipWithoutPOSIXPerms(t)

	log, buf := recordingLogger()
	path := filepath.Join(t.TempDir(), "padmark.db")

	touchDBLogged(t, path, log)()

	requireOwnerOnly(t, path)
	require.Empty(t, buf.String())
}

// TestOpen_LogsWhenTighteningExistingFile pins that narrowing a pre-existing file is never silent:
// the operator must be able to see why group/other access disappeared.
func TestOpen_LogsWhenTighteningExistingFile(t *testing.T) {
	skipWithoutPOSIXPerms(t)

	log, buf := recordingLogger()
	path := writeLooseDB(t)

	touchDBLogged(t, path, log)()

	requireOwnerOnly(t, path)
	require.Contains(t, buf.String(), "level=INFO")
	require.Contains(t, buf.String(), "tightened")
	require.Contains(t, buf.String(), "file="+path)
	require.Contains(t, buf.String(), "from=0644")
	require.Contains(t, buf.String(), "to=0600")
	require.NotContains(t, buf.String(), "level=WARN")
}

// TestOpen_AlreadyOwnerOnlyIsUntouched pins that a file with no group/other bits is neither
// chmod-ed nor reported.
func TestOpen_AlreadyOwnerOnlyIsUntouched(t *testing.T) {
	skipWithoutPOSIXPerms(t)

	path := filepath.Join(t.TempDir(), "padmark.db")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	calls := 0

	t.Cleanup(func() { chmod = os.Chmod })

	chmod = func(name string, mode fs.FileMode) error {
		calls++

		return os.Chmod(name, mode)
	}

	log, buf := recordingLogger()

	touchDBLogged(t, path, log)()

	require.Zero(t, calls, "an owner-only file must not be chmod-ed")
	require.Empty(t, buf.String())
}

// TestOpen_ChmodFailureIsSoft pins that a pre-existing file whose permissions cannot be tightened
// (foreign owner, read-only mount) does not block startup: a WARN is logged and the database works.
func TestOpen_ChmodFailureIsSoft(t *testing.T) {
	skipWithoutPOSIXPerms(t)

	path := writeLooseDB(t)

	t.Cleanup(func() { chmod = os.Chmod })

	chmod = func(name string, _ fs.FileMode) error {
		return &fs.PathError{Op: "chmod", Path: name, Err: syscall.EPERM}
	}

	log, buf := recordingLogger()

	touchDBLogged(t, path, log)()

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "the failed chmod must leave the mode alone")

	require.Contains(t, buf.String(), "level=WARN")
	require.Contains(t, buf.String(), "file="+path)
	require.Contains(t, buf.String(), "mode=0644")
	require.Contains(t, buf.String(), "operation not permitted")
	require.NotContains(t, buf.String(), "level=INFO")
}

func TestDBFilePathExported(t *testing.T) {
	path, create := DBFilePath("file:/var/lib/p.db?cache=shared")
	require.Equal(t, "/var/lib/p.db", path)
	require.True(t, create)

	path, create = DBFilePath(":memory:")
	require.Empty(t, path)
	require.False(t, create)
}
