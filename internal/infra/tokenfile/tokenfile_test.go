package tokenfile

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPath_XDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-config")

	path, err := Path()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/tmp/xdg-config", "padmark", "token"), path)
}

func TestPath_HomeFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/tester")

	path, err := Path()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/home/tester", ".config", "padmark", "token"), path)
}

func TestRead_ReadsAndTrimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "padmark"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "padmark", "token"), []byte("  secret-key\n\n"), 0o600))

	assert.Equal(t, "secret-key", Read(nil))
}

func TestRead_Missing_ReturnsEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	assert.Empty(t, Read(nil))
}

// writeMode writes the token file with an explicit permission mode and returns its path,
// so tests can assert that Read tightens an over-permissive file to 0600.
func writeMode(t *testing.T, contents string, mode os.FileMode) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "padmark"), 0o700))

	path := filepath.Join(dir, "padmark", "token")
	require.NoError(t, os.WriteFile(path, []byte(contents), mode))
	// WriteFile respects the umask, so force the exact mode we want to test against.
	require.NoError(t, os.Chmod(path, mode))
	t.Setenv("XDG_CONFIG_HOME", dir)

	return path
}

func TestRead_TightensGroupOtherReadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}

	var errBuf bytes.Buffer

	path := writeMode(t, "secret-key\n", 0o644)

	assert.Equal(t, "secret-key", Read(&errBuf), "token is still returned after tightening")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "over-permissive token file is tightened to 0600")
	assert.Contains(t, errBuf.String(), "tightened to 600", "a warning is emitted when perms are fixed")
}

func TestRead_LeavesOwnerOnlyFileUntouched(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}

	var errBuf bytes.Buffer

	path := writeMode(t, "secret-key\n", 0o600)

	assert.Equal(t, "secret-key", Read(&errBuf))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Empty(t, errBuf.String(), "no warning for an already-secure token file")
}

func TestSecure_NilErrOutDoesNotPanic(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}

	path := writeMode(t, "secret-key\n", 0o644)

	assert.NotPanics(t, func() { Secure(path, nil) })

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
