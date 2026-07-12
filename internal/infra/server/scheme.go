package server

import "fmt"

// parsePublicScheme validates the --public-scheme override. Empty input means "no override" —
// the HTTP layer falls back to auto-detecting the scheme from TLS/X-Forwarded-Proto
// (see requestScheme in internal/adapters/http).
func parsePublicScheme(raw string) (string, error) {
	switch raw {
	case "", "http", "https":
		return raw, nil
	default:
		return "", fmt.Errorf("invalid public scheme %q: must be %q or %q", raw, "http", "https")
	}
}
