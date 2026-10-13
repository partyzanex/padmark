package mcp

import (
	"context"
	"io"
	"log/slog"
	"os"

	urcli "github.com/urfave/cli/v3"
)

// NewApp returns the root command for the padmark-mcp binary. No subcommands and no shell
// completion: stdout carries the MCP protocol, so the binary has no user-facing stdout
// surface at all — all advisories and logs go to stderr.
func NewApp() *urcli.Command {
	return &urcli.Command{
		Name:  "padmark-mcp",
		Usage: "Model Context Protocol server for the Padmark notes service (stdio transport)",
		Flags: []urcli.Flag{
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
				Usage:   "Bearer token, pmk_ envelope accepted (env: PADMARK_TOKEN; falls back to ~/.config/padmark/token)",
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
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level:       slog.LevelInfo,
		AddSource:   false,
		ReplaceAttr: nil,
	}))

	cl, err := newPadmarkClient(cmd)
	if err != nil {
		return err
	}

	return NewManager(cl, log).Run(ctx)
}
