package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	urcli "github.com/urfave/cli/v3"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// NewApp returns the root CLI command for padmark-cli.
func NewApp() *urcli.Command {
	return &urcli.Command{
		Name:                  "padmark-cli",
		Usage:                 "Command-line client for the Padmark notes service",
		EnableShellCompletion: true,
		Flags:                 globalFlags(),
		Commands: []*urcli.Command{
			createCommand(),
			getCommand(),
			editCommand(),
			deleteCommand(),
			pingCommand(),
		},
	}
}

func globalFlags() []urcli.Flag {
	return []urcli.Flag{
		&urcli.StringFlag{
			Name:    FlagURL,
			Aliases: []string{"u"},
			Sources: urcli.EnvVars(EnvURL),
			Value:   DefaultURL,
			Usage:   "Padmark server URL",
		},
		&urcli.StringFlag{
			Name:    FlagToken,
			Sources: urcli.EnvVars(EnvToken),
			Usage:   "Bearer token for authentication (env: PADMARK_TOKEN; falls back to ~/.config/padmark/token)",
		},
		&urcli.DurationFlag{
			Name:    FlagTimeout,
			Sources: urcli.EnvVars(EnvTimeout),
			Value:   DefaultTimeout,
			Usage:   "Per-request HTTP timeout; 0 disables it (env: PADMARK_TIMEOUT)",
		},
	}
}

func newPadmarkClient(cmd *urcli.Command) (*padmark.Client, error) {
	token, embeddedURL := splitToken(resolveToken(cmd))

	// An envelope token carries its own server URL; an explicit --url/PADMARK_URL still wins.
	serverURL := pickServerURL(cmd, embeddedURL)

	warnInsecureToken(commandErrWriter(cmd), serverURL, token)

	transport := &bearerTransport{base: http.DefaultTransport, token: token}
	httpClient := &http.Client{Transport: transport, Timeout: cmd.Duration(FlagTimeout)}

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

// warnInsecureToken prints a stderr warning when a bearer token would be sent over a non-HTTPS
// connection, where it travels in cleartext and can be intercepted. It is advisory only and
// does not block the request. No token is set, or HTTPS in use → no warning.
func warnInsecureToken(errOut io.Writer, serverURL, token string) {
	if token == "" {
		return
	}

	parsed, err := url.Parse(serverURL)
	if err == nil && parsed.Scheme == "https" {
		return
	}

	// Best-effort advisory write (errWriter swallows the error); a failed warning must not
	// abort the command.
	ew := &errWriter{w: errOut}
	ew.printf(
		"warning: bearer token will be sent in cleartext over a non-HTTPS URL (%s); use an https:// server URL\n",
		serverURL)
}

// bearerTransport injects an Authorization: Bearer header when a token is configured.
// base is listed first to group pointer-sized fields contiguously (govet fieldalignment).
type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (tr *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if tr.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+tr.token)
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
		return "", errors.New(
			"no content provided: pass --content, --file, or pipe content via stdin")
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
		return "", errors.New("note ID is required as the first argument")
	}

	return id, nil
}
