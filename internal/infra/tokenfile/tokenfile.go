// Package tokenfile holds the shared on-disk bearer-token storage used by both padmark-cli
// and padmark-mcp: ~/.config/padmark/token (XDG_CONFIG_HOME-aware). Keeping one implementation
// avoids drift between the two binaries on permissions and error messages — a token file is a
// bearer secret, so the 0600 invariant must hold wherever it is read.
package tokenfile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// configSubdir is the per-application directory under the XDG config home holding the token.
const configSubdir = "padmark"

const (
	// fileMode is the only permission a token file (a bearer secret) may carry: owner
	// read/write, nothing for group or other.
	fileMode os.FileMode = 0o600
	// permGroupOther masks the group- and other-permission bits; any set bit means the
	// token is readable or writable beyond its owner and must be tightened.
	permGroupOther os.FileMode = 0o077
	// osWindows is runtime.GOOS on Windows, where POSIX permission bits are not meaningful.
	osWindows = "windows"
)

// Path resolves ~/.config/padmark/token, honouring XDG_CONFIG_HOME when set.
func Path() (string, error) {
	return configPath("token")
}

// configPath resolves a file inside ~/.config/padmark (XDG_CONFIG_HOME-aware) — the shared
// location for the token and the trust-on-first-use hosts record (see hosts.go).
func configPath(filename string) (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, configSubdir, filename), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}

	return filepath.Join(home, ".config", configSubdir, filename), nil
}

// Read reads and trims the token from the config file, returning "" when the file is
// absent or unreadable. The token is optional, so file-access failures are not surfaced as
// errors. Because the file holds a bearer secret, its permissions are re-checked and
// tightened to 0600 on every read (see Secure); a warning is written to errOut if they
// cannot be fixed.
func Read(errOut io.Writer) string {
	path, err := Path()
	if err != nil {
		return ""
	}

	data, err := os.ReadFile(path) //nolint:gosec // path derived from the user's own config dir, not external input
	if err != nil {
		return ""
	}

	Secure(path, errOut)

	return strings.TrimSpace(string(data))
}

// Secure enforces owner-only (0600) permissions on the token file on every read.
// A token file readable or writable by group/other leaks a bearer credential to other local
// accounts, so any such bit is cleared in place. When the mode cannot be fixed, a best-effort
// warning is written to errOut and the token is still used — a warning beats silently dropping
// auth. Permission bits are POSIX-specific and unreliable on Windows, so the check is skipped there.
func Secure(path string, errOut io.Writer) {
	if runtime.GOOS == osWindows {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		return
	}

	perm := info.Mode().Perm()
	if perm&permGroupOther == 0 {
		return // already owner-only; nothing to do
	}

	msg := fmt.Sprintf("warning: token file %s was accessible by group/other (%#o); tightened to 600\n", path, perm)

	chmodErr := os.Chmod(path, fileMode)
	if chmodErr != nil {
		msg = fmt.Sprintf(
			"warning: token file %s is accessible by group/other (%#o) and could not be secured: %v; "+
				"restrict it manually with: chmod 600 %s\n",
			path, perm, chmodErr, path)
	}

	if errOut == nil {
		return
	}

	io.WriteString(errOut, msg) //nolint:errcheck,gosec // advisory write; a failed warning must not abort
}
