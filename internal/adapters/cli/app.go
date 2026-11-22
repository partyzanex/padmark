package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clientauth"
	"github.com/partyzanex/padmark/internal/clifactory"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

// NewApp returns the root CLI command for padmark-cli.
func NewApp() *urcli.Command {
	return clifactory.Command(func(command *urcli.Command) {
		command.Name = "padmark-cli"
		command.Usage = "Command-line client for the Padmark notes service"
		command.EnableShellCompletion = true
		command.Flags = globalFlags()
		command.Commands = []*urcli.Command{
			createCommand(),
			getCommand(),
			editCommand(),
			deleteCommand(),
			shortenCommand(),
			expandCommand(),
			updateLinkCommand(),
			deleteLinkCommand(),
			uploadCommand(),
			downloadCommand(),
			fileInfoCommand(),
			updateFileCommand(),
			deleteFileCommand(),
			pingCommand(),
			setupCommand(),
			createTokenCommand(),
			createInviteCommand(),
			trustCommand(),
		}
	})
}

func globalFlags() []urcli.Flag {
	return []urcli.Flag{
		clifactory.StringFlag(func(flag *urcli.StringFlag) {
			flag.Name = FlagURL
			flag.Aliases = []string{"u"}
			flag.Sources = urcli.EnvVars(EnvURL)
			flag.Value = DefaultURL
			flag.Usage = "Padmark server URL"
		}),
		clifactory.StringFlag(func(flag *urcli.StringFlag) {
			flag.Name = FlagToken
			flag.Sources = urcli.EnvVars(EnvToken)
			flag.Usage = "Bearer token (env: PADMARK_TOKEN; falls back to ~/.config/padmark/token). " +
				"Flag values are visible in process listings (ps); prefer the env var or token file"
		}),
		clifactory.DurationFlag(func(flag *urcli.DurationFlag) {
			flag.Name = FlagTimeout
			flag.Sources = urcli.EnvVars(EnvTimeout)
			flag.Value = DefaultTimeout
			flag.Usage = "Time limit of the API calls of a command; upload and download use it only for " +
				"the wait for the response headers; 0 disables it (env: PADMARK_TIMEOUT)"
		}),
		clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
			flag.Name = FlagAllowInsecureHTTP
			flag.Sources = urcli.EnvVars(EnvAllowInsecureHTTP)
			flag.Usage = "Allow sending a bearer token or setup password over plain HTTP to a non-loopback host " +
				"(loopback hosts only warn); default refuses (env: PADMARK_ALLOW_INSECURE_HTTP)"
		}),
	}
}

// programName is the binary name in the messages of the client rules.
const programName = "padmark-cli"

// clientSettings reads the settings of the one API client of the CLI from the global flags.
// Each command passes them to clientauth.NewClient, which resolves the server and the token and
// checks trust and the transport. Its HTTP client uses --timeout only for the response headers,
// so `upload` and `download` can transfer a large file. The other commands call requestContext
// to limit their API calls.
func clientSettings(cmd *urcli.Command) *clientauth.Settings {
	return &clientauth.Settings{
		ErrOut:        commandErrWriter(cmd),
		Token:         cmd.String(FlagToken),
		ServerURL:     cmd.String(FlagURL),
		Program:       programName,
		ServerFlag:    FlagURL,
		HeaderTimeout: cmd.Duration(FlagTimeout),
		ServerURLSet:  cmd.IsSet(FlagURL),
		InsecureHTTP:  clientauth.InsecureHTTP(cmd.Bool(FlagAllowInsecureHTTP)),
	}
}

// resolveServerURL returns the base server URL that the requests use, without the trust check.
// A command uses it before it builds the client, to compare a URL argument with the server.
func resolveServerURL(cmd *urcli.Command) (string, error) {
	server, err := clientauth.ResolveServer(clientSettings(cmd))
	if err != nil {
		return "", fmt.Errorf("resolve server: %w", err)
	}

	return server.URL, nil
}

// requestContext adds the --timeout deadline to ctx for the API calls of an ordinary command.
// The deadline limits the full call: the request, the response body and its decode. Call it
// after the command reads its local input, so a slow stdin does not use the time limit.
// `upload` and `download` do not call it: a large file needs more time than --timeout, so only
// the response header timeout of the HTTP client limits them. A zero --timeout adds no
// deadline. The caller must call the returned cancel function.
func requestContext(ctx context.Context, cmd *urcli.Command) (context.Context, context.CancelFunc) {
	timeout := cmd.Duration(FlagTimeout)
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}

	return context.WithTimeout(ctx, timeout)
}

// commandWriter resolves the command's standard output, falling back to os.Stdout when unset.
func commandWriter(cmd *urcli.Command) io.Writer {
	if out := cmd.Root().Writer; out != nil {
		return out
	}

	return os.Stdout
}

// commandErrWriter resolves the command's error output, falling back to os.Stderr when unset.
func commandErrWriter(cmd *urcli.Command) io.Writer {
	if out := cmd.Root().ErrWriter; out != nil {
		return out
	}

	return os.Stderr
}

// readContent reads note content from --content flag, --file flag, or stdin (in that order).
func readContent(cmd *urcli.Command) (string, error) {
	if content := cmd.String(FlagContent); content != "" {
		return content, nil
	}

	if path := cmd.String(FlagFile); path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // path from user CLI input, intentional file inclusion
		if err != nil {
			return "", fmt.Errorf("read file: %w", err)
		}

		return string(data), nil
	}

	// When stdin is an interactive terminal (not a pipe/redirect) there is nothing to read and
	// io.ReadAll would block forever, looking like a hang. Fail fast with a clear message.
	stat, statErr := os.Stdin.Stat()
	if statErr == nil && isCharDevice(stat.Mode()) {
		return "", errContentRequired
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}

	return strings.TrimRight(string(data), "\n"), nil
}

// isCharDevice reports whether a file mode denotes a character device (an interactive
// terminal), as opposed to a pipe or regular file used to feed content via stdin.
func isCharDevice(mode os.FileMode) bool {
	return mode&os.ModeCharDevice != 0
}

// optEditCode returns the X-Edit-Code header value. An empty code omits the header, so the
// server can identify the owner by --token.
func optEditCode(editCode string) padmark.OptString {
	if editCode == "" {
		return padmark.OptString{Value: "", Set: false}
	}

	return padmark.NewOptString(editCode)
}

// runDeleteCommand implements the "resolve ID → delete → print confirmation" flow shared by
// `delete` (notes) and `delete-link` (links). Only this skeleton is shared: ogen generates
// distinct params/response-union/client-method types per operation, so the resource-specific
// request-building and response-mapping stay in doDelete, supplied by each command's own action.
func runDeleteCommand(
	ctx context.Context, cmd *urcli.Command, kind *recordKind,
	doDelete func(ctx context.Context, cl *padmark.Client, id, editCode string) error,
) error {
	id, err := recordIDArg(cmd, kind)
	if err != nil {
		return fmt.Errorf("read resource ID: %w", err)
	}

	apiClient, _, err := clientauth.NewClient(clientSettings(cmd))
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	callCtx, cancel := requestContext(ctx, cmd)
	defer cancel()

	err = doDelete(callCtx, apiClient, id, cmd.String(FlagEditCode))
	if err != nil {
		return fmt.Errorf("delete resource: %w", err)
	}

	ew := &errWriter{w: commandWriter(cmd), err: nil}
	ew.printf("deleted %s\n", id)

	if ew.err != nil {
		return fmt.Errorf("write output: %w", ew.err)
	}

	return nil
}
