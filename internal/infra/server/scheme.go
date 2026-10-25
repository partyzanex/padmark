package server

import (
	"fmt"
	"net"
	"net/url"
)

// parsePublicScheme validates the --public-scheme override. Empty input means "no override" —
// the HTTP layer falls back to auto-detecting the scheme from TLS/X-Forwarded-Proto
// (see requestScheme in internal/adapters/http).
func parsePublicScheme(raw string) (string, error) {
	switch raw {
	case "", "http", "https":
		return raw, nil
	default:
		return "", fmt.Errorf("%w %q: must be %q or %q", errInvalidPublicScheme, raw, "http", "https")
	}
}

// listenerURL formats a browser URL for the main listener. Wildcard addresses use
// localhost; the scheme reflects TLS on this listener, independently of a reverse proxy.
func listenerURL(addr string, tlsEnabled bool) string {
	var endpoint url.URL

	endpoint.Scheme = "http"
	if tlsEnabled {
		endpoint.Scheme = "https"
	}

	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		addressIP := net.ParseIP(host)
		if host == "" || (addressIP != nil && addressIP.IsUnspecified()) {
			host = "localhost"
		}

		addr = net.JoinHostPort(host, port)
	}

	endpoint.Host = addr

	return endpoint.String()
}
