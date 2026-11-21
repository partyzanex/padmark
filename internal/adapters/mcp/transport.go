package mcp

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/partyzanex/padmark/internal/infra/tokenfile"
)

// maxRedirects is the redirect limit of the net/http default client.
const maxRedirects = 10

var (
	errCrossOriginRedirect = errors.New("refusing a redirect to a different server origin")
	errTooManyRedirects    = errors.New("stopped after too many redirects")
	errDefaultTransport    = errors.New("http.DefaultTransport is not an *http.Transport")
)

// newHTTPClient returns the one HTTP client of the proxy. It sends the bearer token only to the
// origin (scheme, host and port) of serverURL and refuses each redirect to a different origin,
// so a redirect cannot carry the token, edit codes or passwords to a server that was not checked.
//
// The client has no total timeout: http.Client.Timeout also limits the body transfer and would
// stop a large upload on a slow link. headerTimeout limits the wait for the response headers.
// net/http starts that wait after it writes the full request body, so a slow upload is not
// stopped. The tools other than upload_file get a deadline for the full call (limitCall). The
// dial and TLS handshake limits of http.DefaultTransport stay. A zero headerTimeout turns the
// header limit off, as --timeout 0 does.
func newHTTPClient(serverURL, token string, headerTimeout time.Duration) (*http.Client, error) {
	defaults, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errDefaultTransport
	}

	origin, err := tokenfile.Origin(serverURL)
	if err != nil {
		return nil, fmt.Errorf("server origin: %w", err)
	}

	base := defaults.Clone()
	base.ResponseHeaderTimeout = headerTimeout

	return &http.Client{
		Transport:     &bearerTransport{base: base, origin: origin, token: token},
		CheckRedirect: sameOriginRedirects(origin),
		Jar:           nil,
		Timeout:       0,
	}, nil
}

// sameOriginRedirects permits only redirects that stay on origin.
func sameOriginRedirects(origin string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errTooManyRedirects
		}

		target, err := tokenfile.Origin(req.URL.String())
		if err != nil {
			return fmt.Errorf("%w: %w", errCrossOriginRedirect, err)
		}

		if target != origin {
			return fmt.Errorf("%w: %s", errCrossOriginRedirect, target)
		}

		return nil
	}
}

// bearerTransport injects an Authorization: Bearer header when a token is configured, and
// Accept: application/json when the request names none: the generated client sends no Accept,
// and the server refuses a burn-after-reading read that does not state its format explicitly.
// origin is the canonical origin (tokenfile.Origin) that may receive the token.
// base is listed first to group pointer-sized fields contiguously (govet fieldalignment).
type bearerTransport struct {
	base   http.RoundTripper
	origin string
	token  string
}

func (tr *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// RoundTrip must not modify the caller's request, so the headers go on a clone.
	clone := req.Clone(req.Context())

	if tr.token != "" && tr.sameOrigin(clone.URL.String()) {
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

// sameOrigin reports whether rawURL has the origin that may receive the token.
func (tr *bearerTransport) sameOrigin(rawURL string) bool {
	origin, err := tokenfile.Origin(rawURL)

	return err == nil && origin == tr.origin
}
