package mcp

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

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
		return nil, fmt.Errorf("validate trusted server: %w", trustErr)
	}

	insecureErr := checkInsecureToken(stderrWriter(cmd), serverURL, token, allowInsecureHTTP(cmd))
	if insecureErr != nil {
		return nil, fmt.Errorf("validate token transport: %w", insecureErr)
	}

	transport := &bearerTransport{base: http.DefaultTransport, token: token}
	httpClient := &http.Client{
		Transport: transport, Timeout: cmd.Duration(FlagTimeout),
		CheckRedirect: nil, Jar: nil,
	}

	cl, err := padmark.NewClient(serverURL, padmark.WithClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	return cl, nil
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

// insecureHTTPSetting is the value of --allow-insecure-http. Its underlying type is bool, and the
// named type keeps the setting out of bare bool parameters.
type insecureHTTPSetting bool

// insecureHTTPDenied refuses cleartext transport to a non-loopback host.
const insecureHTTPDenied insecureHTTPSetting = false

// allowInsecureHTTP reads the --allow-insecure-http setting of the command.
func allowInsecureHTTP(cmd *urcli.Command) insecureHTTPSetting {
	return insecureHTTPSetting(cmd.Bool(FlagAllowInsecureHTTP))
}

// checkInsecureToken guards a bearer token against cleartext transport. HTTPS or no token → nil.
// Plain HTTP to a loopback host only warns on stderr, keeping local development working. Plain
// HTTP to any other host is an error unless allowInsecure (--allow-insecure-http) is set, in
// which case it warns and proceeds.
func checkInsecureToken(errOut io.Writer, serverURL, token string, allowInsecure insecureHTTPSetting) error {
	if token == "" {
		return nil
	}

	parsed, err := url.Parse(serverURL)
	if err == nil && parsed.Scheme == "https" {
		return nil
	}

	if allowInsecure == insecureHTTPDenied && !isLoopbackHost(serverURL) {
		return fmt.Errorf("%w: bearer token to %s; use an https:// URL or pass --%s",
			errInsecureTransport, serverURL, FlagAllowInsecureHTTP)
	}

	fmt.Fprintf(errOut, //nolint:errcheck // advisory write; a failed warning must not abort anything
		"warning: bearer token will be sent in cleartext over a non-HTTPS URL (%s); use an https:// server URL\n",
		serverURL)

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
	// RoundTrip must not modify the caller's request, so the headers go on a clone.
	clone := req.Clone(req.Context())

	if tr.token != "" {
		clone.Header.Set("Authorization", "Bearer "+tr.token)
	}

	if clone.Header.Get("Accept") == "" {
		clone.Header.Set("Accept", "application/json")
	}

	resp, err := tr.base.RoundTrip(clone)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}

	return resp, nil
}
