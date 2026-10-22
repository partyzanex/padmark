package mcp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/infra/logging"
)

// NewApp returns the root command for the padmark-mcp binary. The single subcommand (`trust`)
// exists only for trust-on-first-use bookkeeping — it has its own action and never touches
// stdout. The default (no subcommand) action serves the MCP protocol over stdio, so stdout
// remains a protocol-only surface: all advisories and logs go to stderr.
func NewApp() *urcli.Command {
	return &urcli.Command{
		Name:  "padmark-mcp",
		Usage: "Model Context Protocol server for the Padmark notes service (stdio transport)",
		Commands: []*urcli.Command{
			trustCommand(),
		},
		Flags: []urcli.Flag{
			&urcli.StringFlag{
				Name: FlagLogLevel, Sources: urcli.EnvVars(EnvLogLevel), Value: DefaultLogLevel,
				Usage: "Log level: debug, info, warn or error (stderr only)", Validator: logging.ValidateLevel,
			},
			&urcli.StringFlag{
				Name:    FlagBaseURL,
				Aliases: []string{"u"},
				Sources: urcli.EnvVars(EnvBaseURL),
				Value:   DefaultBaseURL,
				Usage:   "Padmark server URL (overrides the URL embedded in a pmk_ envelope token)",
			},
			&urcli.StringFlag{
				Name:    FlagToken,
				Sources: urcli.EnvVars(EnvToken),
				Usage: "Bearer token, pmk_ envelope accepted (env: PADMARK_TOKEN; falls back to ~/.config/padmark/token). " +
					"Flag values are visible in process listings (ps); prefer the env var or token file",
			},
			&urcli.DurationFlag{
				Name:    FlagTimeout,
				Sources: urcli.EnvVars(EnvTimeout),
				Value:   DefaultTimeout,
				Usage:   "Per-request HTTP timeout; 0 disables it (env: PADMARK_TIMEOUT)",
			},
		},
		Action: runAction,
	}
}

// stderrWriter resolves the command's error output, falling back to os.Stderr when unset.
// All advisories go to stderr — stdout carries the MCP protocol and must stay clean.
func stderrWriter(cmd *urcli.Command) io.Writer {
	if out := cmd.Root().ErrWriter; out != nil {
		return out
	}

	return os.Stderr
}

// runAction builds the padmark client from the resolved token/url and serves MCP over stdio
// until the client disconnects.
func runAction(ctx context.Context, cmd *urcli.Command) error {
	level, err := logging.ParseLevel(cmd.String(FlagLogLevel))
	if err != nil {
		return fmt.Errorf("parse log level: %w", err)
	}

	log := slog.New(slog.NewTextHandler(stderrWriter(cmd), &slog.HandlerOptions{
		Level:       level,
		AddSource:   false,
		ReplaceAttr: nil,
	}))

	cl, err := newPadmarkClient(cmd)
	if err != nil {
		return err
	}

	return NewManager(cl, log).Run(ctx)
}
