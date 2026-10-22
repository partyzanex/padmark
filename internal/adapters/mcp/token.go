package mcp

import (
	"fmt"
	"io"
	"net/http"
	"net/url"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/tokenfile"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// resolveToken returns the bearer token in precedence order: the --token flag or PADMARK_TOKEN
// env (both surfaced through cmd.String), then the token config file ~/.config/padmark/token,
// read through the same tokenfile rules as padmark-cli.
func resolveToken(cmd *urcli.Command) string {
	if token := cmd.String(FlagToken); token != "" {
		return token
	}

	return tokenfile.Read(stderrWriter(cmd))
}

// splitToken separates a configured token value into the bearer key sent to the server and any
// server base URL embedded in it. A pmk_ envelope token (domain.DecodeAPITokenEnvelope) yields
// its key and URL, a legacy bare key yields itself as the bearer and an empty URL.
func splitToken(raw string) (bearer, baseURL string) {
	if embeddedURL, key, ok := domain.DecodeAPITokenEnvelope(raw); ok {
		return key, embeddedURL
	}

	return raw, ""
}

// pickServerURL chooses the base server URL: an explicit --base-url/PADMARK_URL always wins,
// otherwise the URL embedded in an envelope token, otherwise the --base-url default — so a
// stale URL packed into the token can be overridden without editing the token.
func pickServerURL(cmd *urcli.Command, embeddedURL string) string {
	if embeddedURL != "" && !cmd.IsSet(FlagBaseURL) {
		return embeddedURL
	}

	return cmd.String(FlagBaseURL)
}

// newPadmarkClient builds the generated ogen client pointed at the resolved server URL, with
// the raw bearer key injected on every request (the client itself sends no Authorization —
// the API spec carries no per-operation security). No connection is opened here.
func newPadmarkClient(cmd *urcli.Command) (*padmark.Client, error) {
	token, embeddedURL := splitToken(resolveToken(cmd))

	serverURL := pickServerURL(cmd, embeddedURL)

	trustErr := ensureTrustedServer(cmd, embeddedURL, serverURL)
	if trustErr != nil {
		return nil, trustErr
	}

	warnInsecureToken(stderrWriter(cmd), serverURL, token)

	transport := &bearerTransport{base: http.DefaultTransport, token: token}
	httpClient := &http.Client{Transport: transport, Timeout: cmd.Duration(FlagTimeout)}

	cl, err := padmark.NewClient(serverURL, padmark.WithClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	return cl, nil
}

// warnInsecureToken prints a stderr warning when a bearer token would be sent over a non-HTTPS
// connection, where it travels in cleartext and can be intercepted. Advisory only — a failed
// write must not abort anything.
func warnInsecureToken(errOut io.Writer, serverURL, token string) {
	if token == "" {
		return
	}

	parsed, err := url.Parse(serverURL)
	if err == nil && parsed.Scheme == "https" {
		return
	}

	fmt.Fprintf(errOut, //nolint:errcheck // advisory write; a failed warning must not abort anything
		"warning: bearer token will be sent in cleartext over a non-HTTPS URL (%s); use an https:// server URL\n",
		serverURL)
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
