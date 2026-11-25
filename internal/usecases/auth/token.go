package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// bearerTokenBytes is the entropy of each bearer value that the auth managers make (256 bits):
// session IDs, API tokens, invite tokens and reveal tokens.
const bearerTokenBytes = 32

// newRandomToken returns bearerTokenBytes random bytes as base64url text without padding.
func newRandomToken() (string, error) {
	buf := make([]byte, bearerTokenBytes)

	_, err := rand.Read(buf)
	if err != nil {
		return "", fmt.Errorf("read rand: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}
