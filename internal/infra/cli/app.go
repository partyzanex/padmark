package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
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
			Usage:   "Bearer token for authentication (env: PADMARK_TOKEN)",
		},
	}
}

func newPadmarkClient(cmd *urcli.Command) (*padmark.Client, error) {
	serverURL := cmd.String(FlagURL)
	token := cmd.String(FlagToken)

	transport := &bearerTransport{base: http.DefaultTransport, token: token}
	httpClient := &http.Client{Transport: transport}

	cl, err := padmark.NewClient(serverURL, padmark.WithClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	return cl, nil
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

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}

	return strings.TrimRight(string(data), "\n"), nil
}

// noteIDArg returns the note ID from the first positional argument or an error.
func noteIDArg(cmd *urcli.Command) (string, error) {
	id := cmd.Args().Get(0)
	if id == "" {
		return "", errors.New("note ID is required as the first argument")
	}

	return id, nil
}
