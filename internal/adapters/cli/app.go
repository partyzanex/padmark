package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clifactory"
	"github.com/partyzanex/padmark/internal/errorcontext"
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
			deleteLinkCommand(),
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
			flag.Usage = "Per-request HTTP timeout; 0 disables it (env: PADMARK_TIMEOUT)"
		}),
		clifactory.BoolFlag(func(flag *urcli.BoolFlag) {
			flag.Name = FlagAllowInsecureHTTP
			flag.Sources = urcli.EnvVars(EnvAllowInsecureHTTP)
			flag.Usage = "Allow sending a bearer token or setup password over plain HTTP to a non-loopback host " +
				"(loopback hosts only warn); default refuses (env: PADMARK_ALLOW_INSECURE_HTTP)"
		}),
	}
}

func newPadmarkClient(cmd *urcli.Command) (*padmark.Client, error) {
	// An envelope token carries its own server URL; an explicit --url/PADMARK_URL still wins.
	token, serverURL, trustErr := resolveTrustedServer(cmd)
	if trustErr != nil {
		return nil, fmt.Errorf("resolve trusted server: %w", trustErr)
	}

	insecureErr := checkInsecureToken(commandErrWriter(cmd), serverURL, token, cmd.Bool(FlagAllowInsecureHTTP))
	if insecureErr != nil {
		return nil, fmt.Errorf("validate token transport: %w", insecureErr)
	}

	transport := &bearerTransport{base: http.DefaultTransport, token: token}
	httpClient := &http.Client{Transport: transport, Timeout: cmd.Duration(FlagTimeout),
		CheckRedirect: nil, Jar: nil,
	}

	cl, err := padmark.NewClient(serverURL, padmark.WithClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	return cl, nil
}

// commandErrWriter resolves the command's error output, falling back to os.Stderr when unset.
func commandErrWriter(cmd *urcli.Command) io.Writer {
	if out := cmd.Root().ErrWriter; out != nil {
		return out
	}

	return os.Stderr
}

// errInsecureTransport is returned when a credential would travel in cleartext to a non-loopback
// host and the operator did not opt in with --allow-insecure-http.
var errInsecureTransport = errors.New("refusing to send credentials over non-HTTPS to a non-loopback host")

// isLoopbackHost reports whether the URL's host is localhost or a loopback IP (127.0.0.0/8, ::1).
// An unparsable URL or empty host is not loopback, so the check fails closed.
func isLoopbackHost(serverURL string) bool {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return false
	}

	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}

	addr, err := netip.ParseAddr(host)

	return err == nil && addr.IsLoopback()
}

// checkInsecureToken guards a bearer token against cleartext transport. HTTPS or no token → nil.
// Plain HTTP to a loopback host only warns on stderr, keeping local development working. Plain
// HTTP to any other host is an error unless allowInsecure (--allow-insecure-http) is set, in
// which case it warns and proceeds.
func checkInsecureToken(errOut io.Writer, serverURL, token string, allowInsecure bool) error {
	if token == "" {
		return nil
	}

	return errorcontext.Wrap(
		checkInsecureTransport(errOut, serverURL, "bearer token", allowInsecure), "validate token transport",
	)
}

// checkInsecureTransport implements the shared policy for checkInsecureToken and
// checkInsecureSetup; what names the credential in the message.
func checkInsecureTransport(errOut io.Writer, serverURL, what string, allowInsecure bool) error {
	parsed, err := url.Parse(serverURL)
	if err == nil && parsed.Scheme == "https" {
		return nil
	}

	if !allowInsecure && !isLoopbackHost(serverURL) {
		return fmt.Errorf("%w: %s to %s; use an https:// URL or pass --%s",
			errInsecureTransport, what, serverURL, FlagAllowInsecureHTTP)
	}

	// Best-effort advisory write (errWriter swallows the error); a failed warning must not
	// abort the command.
	ew := &errWriter{w: errOut, err: nil}
	ew.printf(
		"warning: %s will be sent in cleartext over a non-HTTPS URL (%s); use an https:// server URL\n",
		what, serverURL)

	return nil
}

// bearerTransport injects an Authorization: Bearer header when a token is configured, and
// Accept: application/json when the request names none: the generated client sends no Accept,
// and the server refuses a burn-after-reading read that does not state its format explicitly.
// base is listed first to group pointer-sized fields contiguously (govet fieldalignment).
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (tr *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())

	if tr.token != "" {
		req.Header.Set("Authorization", "Bearer "+tr.token)
	}

	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}

	resp, err := tr.base.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}

	return resp, nil
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

// noteIDArg returns the note ID from the first positional argument or an error.
func noteIDArg(cmd *urcli.Command) (string, error) {
	id := cmd.Args().Get(0)
	if id == "" {
		return "", errNoteIDRequired
	}

	return id, nil
}

// linkIDArg returns the link ID from the first positional argument or an error.
func linkIDArg(cmd *urcli.Command) (string, error) {
	id := cmd.Args().Get(0)
	if id == "" {
		return "", errLinkIDRequired
	}

	return id, nil
}

// runDeleteCommand implements the "resolve ID → delete → print confirmation" flow shared by
// `delete` (notes) and `delete-link` (links). Only this skeleton is shared: ogen generates
// distinct params/response-union/client-method types per operation, so the resource-specific
// request-building and response-mapping stay in doDelete, supplied by each command's own action.
func runDeleteCommand(
	ctx context.Context, cmd *urcli.Command, idArg func(*urcli.Command) (string, error),
	doDelete func(ctx context.Context, cl *padmark.Client, id, editCode string) error,
) error {
	id, err := idArg(cmd)
	if err != nil {
		return fmt.Errorf("read resource ID: %w", err)
	}

	cl, err := newPadmarkClient(cmd)
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	err = doDelete(ctx, cl, id, cmd.String(FlagEditCode))
	if err != nil {
		return fmt.Errorf("delete resource: %w", err)
	}

	ew := &errWriter{w: os.Stdout, err: nil}
	ew.printf("deleted %s\n", id)

	if ew.err != nil {
		return fmt.Errorf("write output: %w", ew.err)
	}

	return nil
}
