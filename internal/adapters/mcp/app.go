package mcp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clifactory"
	"github.com/partyzanex/padmark/internal/errorcontext"
	"github.com/partyzanex/padmark/internal/infra/logging"
)

// NewApp returns the root command for the padmark-mcp binary. The single subcommand (`trust`)
// exists only for trust-on-first-use bookkeeping — it has its own action and never touches
// stdout. The default (no subcommand) action serves the MCP protocol over stdio, so stdout
// remains a protocol-only surface: all advisories and logs go to stderr.
func NewApp() *urcli.Command {
	return clifactory.Command(func(command *urcli.Command) {
		command.Name = "padmark-mcp"
		command.Usage = "Model Context Protocol server for the Padmark notes service (stdio transport)"
		command.Commands = []*urcli.Command{
			trustCommand(),
		}
		command.Flags = []urcli.Flag{
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagLogLevel
				flag.Sources = urcli.EnvVars(EnvLogLevel)
				flag.Value = DefaultLogLevel
				flag.Usage = "Log level: debug, info, warn or error (stderr only)"
				flag.Validator = logging.ValidateLevel
			}),
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagBaseURL
				flag.Aliases = []string{"u"}
				flag.Sources = urcli.EnvVars(EnvBaseURL)
				flag.Value = DefaultBaseURL
				flag.Usage = "Padmark server URL (overrides the URL embedded in a pmk_ envelope token)"
			}),
			clifactory.StringFlag(func(flag *urcli.StringFlag) {
				flag.Name = FlagToken
				flag.Sources = urcli.EnvVars(EnvToken)
				flag.Usage = "Bearer token, pmk_ envelope accepted (env: PADMARK_TOKEN; " +
					"falls back to ~/.config/padmark/token). " +
					"Flag values are visible in process listings (ps); prefer the env var or token file"
			}),
			clifactory.DurationFlag(func(flag *urcli.DurationFlag) {
				flag.Name = FlagTimeout
				flag.Sources = urcli.EnvVars(EnvTimeout)
				flag.Value = DefaultTimeout
				flag.Usage = "Per-request HTTP timeout; 0 disables it (env: PADMARK_TIMEOUT)"
			}),
			clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
				flag.Name = FlagAllowInsecureHTTP
				flag.Sources = urcli.EnvVars(EnvAllowInsecureHTTP)
				flag.Usage = "Allow sending the bearer token over plain HTTP to a non-loopback host " +
					"(loopback hosts only warn); default refuses (env: PADMARK_ALLOW_INSECURE_HTTP)"
			}),
		}
		command.Flags = append(command.Flags, uploadFlags()...)
		command.Action = runAction
	})
}

// uploadFlags returns the flags that limit upload_file.
func uploadFlags() []urcli.Flag {
	return []urcli.Flag{
		clifactory.StringFlag(func(flag *urcli.StringFlag) {
			flag.Name = FlagUploadRoot
			flag.Sources = urcli.EnvVars(EnvUploadRoot)
			flag.Value = DefaultUploadRoot
			flag.Usage = "Directory from which upload_file may read files; empty turns uploads off. " +
				"Do not put secrets in it (env: PADMARK_MCP_UPLOAD_ROOT)"
		}),
		clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
			flag.Name = FlagUploadAllowHidden
			flag.Sources = urcli.EnvVars(EnvUploadAllowHidden)
			flag.Value = DefaultUploadAllowHidden
			flag.Usage = "Let upload_file read paths with names that start with a dot, such as .env " +
				"(env: PADMARK_MCP_UPLOAD_ALLOW_HIDDEN)"
		}),
		clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
			flag.Name = FlagUploadAllowSlug
			flag.Sources = urcli.EnvVars(EnvUploadAllowSlug)
			flag.Value = DefaultUploadAllowSlug
			flag.Usage = "Let upload_file send the slug that the agent gives (env: PADMARK_MCP_UPLOAD_ALLOW_SLUG)"
		}),
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
		Level:     level,
		AddSource: false, ReplaceAttr: nil,
	}))

	uploadRoot, err := resolveUploadRoot(cmd.String(FlagUploadRoot))
	if err != nil {
		return fmt.Errorf("check --%s: %w", FlagUploadRoot, err)
	}

	clients, err := newPadmarkClients(cmd)
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	mgr := NewManager(clients.api, clients.files, log, managerOptions(cmd, uploadRoot, clients)...)

	return errorcontext.Wrap(mgr.Run(ctx), "run server")
}

// managerOptions converts the upload flags and the token state of clients into Manager options.
func managerOptions(cmd *urcli.Command, uploadRoot string, clients *padmarkClients) []Option {
	opts := []Option{WithUploadRoot(uploadRoot)}

	if cmd.Bool(FlagUploadAllowHidden) {
		opts = append(opts, WithUploadHidden())
	}

	if cmd.Bool(FlagUploadAllowSlug) {
		opts = append(opts, WithUploadSlug())
	}

	if clients.hasToken {
		opts = append(opts, WithAccountToken())
	}

	return opts
}
