package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	urcli "github.com/urfave/cli/v3"
)

// tokenConfigSubdir is the per-application directory under the XDG config home holding the token.
const tokenConfigSubdir = "padmark"

// resolveToken returns the bearer token in precedence order: the --token flag or PADMARK_TOKEN
// env (both surfaced through cmd.String), then the token config file ~/.config/padmark/token.
// The token is issued by an admin through /admin and copied into the file by the user; the CLI
// only reads it. A missing or unreadable file yields an empty token — auth is simply omitted.
func resolveToken(cmd *urcli.Command) string {
	if token := cmd.String(FlagToken); token != "" {
		return token
	}

	return readTokenFile()
}

// readTokenFile reads and trims the token from the config file, returning "" when the file is
// absent or unreadable. The token is optional, so file-access failures are not surfaced as errors.
func readTokenFile() string {
	path, err := tokenFilePath()
	if err != nil {
		return ""
	}

	data, err := os.ReadFile(path) //nolint:gosec // path derived from the user's own config dir, not external input
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(data))
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
