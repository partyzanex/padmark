package cli

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
)

// newBearerClient returns an HTTP client for serverURL. It sends the bearer token only to the
// origin (scheme, host and port) of serverURL and refuses each redirect to a different origin,
// so a redirect cannot carry the token, edit codes or passwords to a server that was not checked.
func newBearerClient(serverURL, token string, timeout time.Duration) (*http.Client, error) {
	origin, err := tokenfile.Origin(serverURL)
	if err != nil {
		return nil, fmt.Errorf("server origin: %w", err)
	}

	return &http.Client{
		Transport:     &bearerTransport{base: http.DefaultTransport, origin: origin, token: token},
		CheckRedirect: sameOriginRedirects(origin),
		Jar:           nil,
		Timeout:       timeout,
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
