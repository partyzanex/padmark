package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/domain"
)

// tokenConfigSubdir is the per-application directory under the XDG config home holding the token.
const tokenConfigSubdir = "padmark"

const (
	// tokenFileMode is the only permission a token file (a bearer secret) may carry: owner
	// read/write, nothing for group or other.
	tokenFileMode os.FileMode = 0o600
	// tokenFilePermGroupOther masks the group- and other-permission bits; any set bit means the
	// token is readable or writable beyond its owner and must be tightened.
	tokenFilePermGroupOther os.FileMode = 0o077
	// osWindows is runtime.GOOS on Windows, where POSIX permission bits are not meaningful.
	osWindows = "windows"
)

// resolveToken returns the bearer token in precedence order: the --token flag or PADMARK_TOKEN
// env (both surfaced through cmd.String), then the token config file ~/.config/padmark/token.
// The token is issued by an admin through /admin and copied into the file by the user; the CLI
// only reads it. A missing or unreadable file yields an empty token — auth is simply omitted.
func resolveToken(cmd *urcli.Command) string {
	if token := cmd.String(FlagToken); token != "" {
		return token
	}

	return readTokenFile(commandErrWriter(cmd))
}

// readTokenFile reads and trims the token from the config file, returning "" when the file is
// absent or unreadable. The token is optional, so file-access failures are not surfaced as errors.
// Because the file holds a bearer secret, its permissions are re-checked and tightened to 0600 on
// every read (see secureTokenFile); a warning is written to errOut if they cannot be fixed.
func readTokenFile(errOut io.Writer) string {
	path, err := tokenFilePath()
	if err != nil {
		return ""
	}

	data, err := os.ReadFile(path) //nolint:gosec // path derived from the user's own config dir, not external input
	if err != nil {
		return ""
	}

	secureTokenFile(path, errOut)

	return strings.TrimSpace(string(data))
}

// secureTokenFile enforces owner-only (0600) permissions on the token file on every read.
// A token file readable or writable by group/other leaks a bearer credential to other local
// accounts, so any such bit is cleared in place. When the mode cannot be fixed, a best-effort
// warning is written to errOut and the token is still used — a warning beats silently dropping
// auth. Permission bits are POSIX-specific and unreliable on Windows, so the check is skipped there.
func secureTokenFile(path string, errOut io.Writer) {
	if runtime.GOOS == osWindows {
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		return
	}

	perm := info.Mode().Perm()
	if perm&tokenFilePermGroupOther == 0 {
		return // already owner-only; nothing to do
	}

	msg := fmt.Sprintf("warning: token file %s was accessible by group/other (%#o); tightened to 600\n", path, perm)

	chmodErr := os.Chmod(path, tokenFileMode)
	if chmodErr != nil {
		msg = fmt.Sprintf(
			"warning: token file %s is accessible by group/other (%#o) and could not be secured: %v; "+
				"restrict it manually with: chmod 600 %s\n",
			path, perm, chmodErr, path)
	}

	ewr := &errWriter{w: errOut}
	ewr.printf("%s", msg)
}

// tokenFilePath resolves ~/.config/padmark/token, honouring XDG_CONFIG_HOME when set.
func tokenFilePath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, tokenConfigSubdir, "token"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}

	return filepath.Join(home, ".config", tokenConfigSubdir, "token"), nil
}

// splitToken separates a configured token value into the bearer key sent to the server and any
// server base URL embedded in it. It is the CLI-side counterpart of
// domain.EncodeAPITokenEnvelope: an envelope token yields its key and URL, while a legacy bare
// key yields itself as the bearer and an empty URL.
func splitToken(raw string) (bearer, baseURL string) {
	if url, key, ok := domain.DecodeAPITokenEnvelope(raw); ok {
		return key, url
	}

	return raw, ""
}

// pickServerURL chooses the base server URL: an explicit --url/PADMARK_URL wins, otherwise the URL
// embedded in an envelope token, otherwise the --url default.
func pickServerURL(cmd *urcli.Command, embeddedURL string) string {
	if embeddedURL != "" && !cmd.IsSet(FlagURL) {
		return embeddedURL
	}

	return cmd.String(FlagURL)
}

// resolveServerURL returns the base server URL the client will actually use — for display in
// ping/create output as well as for requests — so what the user sees matches where requests go.
func resolveServerURL(cmd *urcli.Command) string {
	_, embeddedURL := splitToken(resolveToken(cmd))

	return pickServerURL(cmd, embeddedURL)
}
