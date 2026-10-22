package tokenfile_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/infra/tokenfile"
)

// isolateConfig points the config dir at a fresh temp dir so tests never touch the real
// ~/.config/padmark.
func isolateConfig(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	return dir
}

// TestOrigin pins the origin extraction: scheme and host[:port] are kept (they decide where
// credentials go), the path is dropped, and a URL without scheme or host is rejected rather
// than trusted.
func TestOrigin(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		serverURL string
		want      string
		wantErr   bool
	}{
		{"plain https", "https://example.com", "https://example.com", false},
		{"port and path", "https://example.com:8443/api/base", "https://example.com:8443", false},
		{"http", "http://localhost:8080", "http://localhost:8080", false},
		{"scheme kept distinct", "http://example.com", "http://example.com", false},
		{"host lowercased", "https://A.Example.COM", "https://a.example.com", false},
		{"scheme lowercased", "HTTPS://example.com", "https://example.com", false},
		{"https default port stripped", "https://A.com:443", "https://a.com", false},
		{"http default port stripped", "http://example.com:80/x", "http://example.com", false},
		{"cross-scheme default port kept", "https://example.com:80", "https://example.com:80", false},
		{"http port 443 kept", "http://example.com:443", "http://example.com:443", false},
		{"empty port dropped", "https://example.com:", "https://example.com", false},
		{"ipv6 with port", "http://[::1]:8080", "http://[::1]:8080", false},
		{"ipv6 default port stripped", "https://[::1]:443", "https://[::1]", false},
		{"missing host", "https:///path", "", true},
		{"missing scheme", "example.com:8080", "", true},
		{"empty", "", "", true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			origin, err := tokenfile.Origin(testCase.serverURL)

			if testCase.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, testCase.want, origin)
		})
	}
}

// TestKnownOriginRoundtrip covers the add/check cycle: nothing is trusted before the first
// record, AddKnownOrigin makes exactly that origin trusted (scheme-sensitive), and a second
// add of the same origin does not duplicate the line.
func TestKnownOriginRoundtrip(t *testing.T) {
	isolateConfig(t)

	assert.False(t, tokenfile.IsKnownOrigin("https://example.com"), "empty hosts file must trust nothing")

	require.NoError(t, tokenfile.AddKnownOrigin("https://example.com"))

	assert.True(t, tokenfile.IsKnownOrigin("https://example.com"))
	assert.False(t, tokenfile.IsKnownOrigin("http://example.com"), "a different scheme is a different origin")

	require.NoError(t, tokenfile.AddKnownOrigin("https://example.com"), "re-adding a known origin is not an error")

	path, err := tokenfile.HostsPath()
	require.NoError(t, err)

	data, err := os.ReadFile(path) //nolint:gosec // path from the isolated test config dir
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), "https://example.com\n"), "known origin must not be duplicated")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "hosts file must be owner-only")
}

// TestAddKnownOrigin_MissingConfigDir verifies the config dir is created on demand.
func TestAddKnownOrigin_MissingConfigDir(t *testing.T) {
	dir := isolateConfig(t)

	require.NoError(t, tokenfile.AddKnownOrigin("https://first.example"))

	_, err := os.Stat(filepath.Join(dir, "padmark", "known_hosts"))
	assert.NoError(t, err)
}

// TestAddKnownOrigin_RejectsEmpty verifies an empty origin cannot poison the hosts file.
func TestAddKnownOrigin_RejectsEmpty(t *testing.T) {
	isolateConfig(t)

	require.Error(t, tokenfile.AddKnownOrigin(""))
}

// TestIsKnownOrigin_UnsetConfigDir verifies a user without a resolvable home gets
// fail-closed behavior: nothing is trusted, never "everything".
func TestIsKnownOrigin_UnsetConfigDir(t *testing.T) {
	isolateConfig(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")

	assert.False(t, tokenfile.IsKnownOrigin("https://example.com"))
}

// TestIsKnownOrigin_Normalised verifies trust survives cosmetic differences in how an origin is
// spelled — host case and an explicit default port — in both the query and the stored record,
// so files written before canonicalisation (or by hand) keep working.
func TestIsKnownOrigin_Normalised(t *testing.T) {
	cases := []struct {
		name   string
		stored string
		query  string
		want   bool
	}{
		{"query with default port", "https://a.com", "https://A.com:443", true},
		{"stored with default port and case", "https://A.COM:443", "https://a.com", true},
		{"stored with trailing path", "https://a.com/base", "https://a.com", true},
		{"non-default port differs", "https://a.com", "https://a.com:8443", false},
		{"scheme differs", "http://a.com", "https://a.com", false},
		{"garbage line skipped", "not a url\nhttps://b.com", "https://b.com", true},
		{"unparsable query rejected", "https://a.com", "a.com", false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			writeHostsFile(t, testCase.stored+"\n", 0o600)

			assert.Equal(t, testCase.want, tokenfile.IsKnownOrigin(testCase.query))
		})
	}
}

// TestIsKnownOrigin_FailsClosedOnUnsafeFile verifies a hosts file that others could have
// written to — group/other-writable, or not a regular file — trusts nothing, even when it
// lists the queried origin.
func TestIsKnownOrigin_FailsClosedOnUnsafeFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}

	cases := []struct {
		name string
		mode os.FileMode
		want bool
	}{
		{"owner-only", 0o600, true},
		{"group/other readable", 0o644, true},
		{"group writable", 0o620, false},
		{"other writable", 0o602, false},
		{"world writable", 0o666, false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			writeHostsFile(t, "https://example.com\n", testCase.mode)

			assert.Equal(t, testCase.want, tokenfile.IsKnownOrigin("https://example.com"))
		})
	}
}

// TestIsKnownOrigin_FailsClosedOnDirectory verifies a non-regular hosts path (here a
// directory) is never treated as a trust record.
func TestIsKnownOrigin_FailsClosedOnDirectory(t *testing.T) {
	dir := isolateConfig(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "padmark", "known_hosts"), 0o700))

	assert.False(t, tokenfile.IsKnownOrigin("https://example.com"))
}

// TestAddKnownOrigin_HealsWritableFile verifies running trust over a group/other-writable
// hosts file re-tightens it to 0600, after which the record is honoured again.
func TestAddKnownOrigin_HealsWritableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}

	path := writeHostsFile(t, "https://example.com\n", 0o666)
	require.False(t, tokenfile.IsKnownOrigin("https://example.com"), "precondition: unsafe file trusts nothing")

	require.NoError(t, tokenfile.AddKnownOrigin("https://example.com"))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "trust must re-tighten the hosts file")
	assert.True(t, tokenfile.IsKnownOrigin("https://example.com"))
}

// TestAddKnownOrigin_StoresCanonicalOrigin verifies the record is written in canonical form,
// so the file never accumulates spelling variants of one origin.
func TestAddKnownOrigin_StoresCanonicalOrigin(t *testing.T) {
	isolateConfig(t)

	require.NoError(t, tokenfile.AddKnownOrigin("https://Example.COM:443"))
	require.NoError(t, tokenfile.AddKnownOrigin("https://example.com"))

	path, err := tokenfile.HostsPath()
	require.NoError(t, err)

	data, err := os.ReadFile(path) //nolint:gosec // path from the isolated test config dir
	require.NoError(t, err)
	assert.Equal(t, "https://example.com\n", string(data))
}

// writeHostsFile isolates the config dir and writes the hosts file with an exact mode,
// returning its path.
func writeHostsFile(t *testing.T, contents string, mode os.FileMode) string {
	t.Helper()

	dir := isolateConfig(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "padmark"), 0o700))

	path := filepath.Join(dir, "padmark", "known_hosts")
	require.NoError(t, os.WriteFile(path, []byte(contents), mode))
	// WriteFile respects the umask, so force the exact mode under test.
	require.NoError(t, os.Chmod(path, mode))

	return path
}
