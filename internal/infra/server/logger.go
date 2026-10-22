package server

import (
	"log/slog"
	"os"

	"github.com/partyzanex/padmark/internal/infra/logging"
)

func newLogger(level, format string) *slog.Logger {
	lvl, err := logging.ParseLevel(level)
	if err != nil {
		slog.Warn("invalid log level; falling back to info", "err", err)
	}

	opts := &slog.HandlerOptions{
		Level: lvl,
		// AddSource / ReplaceAttr zero = no source-file annotations and no attribute rewriting.
		AddSource:   false,
		ReplaceAttr: nil,
	}

	if format == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}

	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}
