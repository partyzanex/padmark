package mcp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clientauth"
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
				flag.Usage = "Time limit of each tool call; upload_file uses it only for the wait for " +
					"the response headers; 0 disables it (env: PADMARK_TIMEOUT)"
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

// uploadFlags returns the flag that limits upload_file.
func uploadFlags() []urcli.Flag {
	return []urcli.Flag{
		clifactory.StringFlag(func(flag *urcli.StringFlag) {
			flag.Name = FlagUploadRoot
			flag.Sources = urcli.EnvVars(EnvUploadRoot)
			flag.Value = DefaultUploadRoot
			flag.Usage = "Directory from which upload_file may read files; empty turns uploads off. " +
				"Do not put secrets in it (env: PADMARK_MCP_UPLOAD_ROOT)"
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

// runAction builds the one padmark client from the resolved token and server URL and serves
// MCP over stdio until the client disconnects. It is the composition root of the binary.
func runAction(ctx context.Context, cmd *urcli.Command) error {
	level, err := logging.ParseLevel(cmd.String(FlagLogLevel))
	if err != nil {
		return fmt.Errorf("parse log level: %w", err)
	}

	log := slog.New(slog.NewTextHandler(stderrWriter(cmd), &slog.HandlerOptions{
		Level:     level,
		AddSource: false, ReplaceAttr: nil,
	}))

	proxy, err := newServerFromFlags(cmd, log)
	if err != nil {
		return err
	}

	return errorcontext.Wrap(proxy.Run(ctx), "run server")
}

// newServerFromFlags builds the Server with the one padmark client of the proxy. The token
// state selects the upload default: with a token, upload_file uses "owner".
func newServerFromFlags(cmd *urcli.Command, log *slog.Logger) (*Server, error) {
	uploadRoot, err := resolveUploadRoot(cmd.String(FlagUploadRoot))
	if err != nil {
		return nil, fmt.Errorf("check --%s: %w", FlagUploadRoot, err)
	}

	client, server, err := clientauth.NewClient(clientSettings(cmd))
	if err != nil {
		return nil, fmt.Errorf("create API client: %w", err)
	}

	opts := []Option{WithUploadRoot(uploadRoot), WithCallTimeout(cmd.Duration(FlagTimeout))}
	if server.HasToken() {
		opts = append(opts, WithAccountToken())
	}

	return NewServer(client, log, opts...), nil
}

// programName is the binary name in the messages of the client rules.
const programName = "padmark-mcp"

// clientSettings reads the settings of the one API client of the proxy from the flags. The
// shared client rules (clientauth.NewClient) resolve the server and the token and check trust
// and the transport. The HTTP client uses --timeout only for the response headers; limitCall
// adds the deadline of each tool call other than upload_file. No connection is opened here.
func clientSettings(cmd *urcli.Command) *clientauth.Settings {
	return &clientauth.Settings{
		ErrOut:        stderrWriter(cmd),
		Token:         cmd.String(FlagToken),
		ServerURL:     cmd.String(FlagBaseURL),
		Program:       programName,
		ServerFlag:    FlagBaseURL,
		HeaderTimeout: cmd.Duration(FlagTimeout),
		ServerURLSet:  cmd.IsSet(FlagBaseURL),
		InsecureHTTP:  clientauth.InsecureHTTP(cmd.Bool(FlagAllowInsecureHTTP)),
	}
}
