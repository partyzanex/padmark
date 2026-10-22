// Package logging holds the log-level parsing shared by padmark-server and padmark-mcp, so
// both binaries accept the same level names and reject unknown ones the same way.
package logging

import (
	"fmt"
	"log/slog"
	"strings"
)

// ParseLevel accepts the documented server/MCP levels, without silent fallback.
func ParseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("invalid log level %q: use debug, info, warn or error", value)
	}
}

// ValidateLevel reports whether value is a level ParseLevel accepts, returning ParseLevel's
// error otherwise. Its signature fits urfave/cli flag validators, so an invalid --log-level is
// rejected at flag parsing instead of silently falling back to info.
func ValidateLevel(value string) error {
	_, err := ParseLevel(value)

	return err
}
