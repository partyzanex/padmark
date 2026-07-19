package domain

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// APIToken is a long-lived bearer key issued through the browser-based CLI login flow.
// The plain key is shown to the user exactly once; only its SHA-256 hash is persisted.
// TokenHash is both the primary key and the public identifier used in admin URLs, since the
// CLI flow does not issue a separate opaque ID. It is looked up on every authenticated API request.
type APIToken struct {
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	TokenHash  string
	UserID     uuid.UUID
}

// apiTokenEnvelopePrefix marks a value produced by EncodeAPITokenEnvelope so the CLI can tell a
// self-contained envelope apart from a legacy bare bearer key.
const apiTokenEnvelopePrefix = "pmk_"

// apiTokenEnvelope is the JSON payload carried inside an envelope token: the server base URL and
// the raw bearer key. It is base64url-encoded and prefixed to form the copy-paste-friendly token.
type apiTokenEnvelope struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// EncodeAPITokenEnvelope packs a server base URL together with the raw bearer key into a single
// opaque, copy-paste-friendly token: the prefix followed by a base64url-encoded JSON object with
// "url" and "token" fields. The user configures one string and the CLI learns both the endpoint
// and the key from it. The envelope is transport convenience only: the server still authenticates
// on the raw key alone, so encoding is a presentation concern, not a security one.
func EncodeAPITokenEnvelope(baseURL, rawKey string) (string, error) {
	payload, err := json.Marshal(apiTokenEnvelope{URL: baseURL, Token: rawKey})
	if err != nil {
		return "", fmt.Errorf("marshal api token envelope: %w", err)
	}

	return apiTokenEnvelopePrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

// DecodeAPITokenEnvelope reverses EncodeAPITokenEnvelope. ok is false when s is not an envelope
// (for example a legacy bare key or malformed input) or carries no token, and the caller then
// treats s as the raw bearer key. baseURL may be empty if the envelope carried none.
func DecodeAPITokenEnvelope(s string) (baseURL, rawKey string, ok bool) {
	rest, found := strings.CutPrefix(s, apiTokenEnvelopePrefix)
	if !found {
		return "", "", false
	}

	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		return "", "", false
	}

	var env apiTokenEnvelope

	err = json.Unmarshal(raw, &env)
	if err != nil {
		return "", "", false
	}

	if env.Token == "" {
		return "", "", false
	}

	return env.URL, env.Token, true
}
