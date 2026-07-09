package domain

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPITokenEnvelope_RoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		rawKey  string
	}{
		{name: "url and key", baseURL: "https://notes.example.com", rawKey: "abc-DEF_123"},
		{name: "url with path and port", baseURL: "http://localhost:8080/api", rawKey: "k"},
		{name: "empty url", baseURL: "", rawKey: "only-key"},
	}

	for _, sample := range cases {
		t.Run(sample.name, func(t *testing.T) {
			envelope, err := EncodeAPITokenEnvelope(sample.baseURL, sample.rawKey)
			require.NoError(t, err)
			require.NotEqual(t, sample.rawKey, envelope, "envelope must not equal the bare key")

			gotURL, gotKey, ok := DecodeAPITokenEnvelope(envelope)
			require.True(t, ok)
			assert.Equal(t, sample.baseURL, gotURL)
			assert.Equal(t, sample.rawKey, gotKey)
		})
	}
}

func TestDecodeAPITokenEnvelope_NotAnEnvelope(t *testing.T) {
	// A legacy bare key (no prefix) is reported as not-an-envelope so the caller uses it verbatim.
	_, _, ok := DecodeAPITokenEnvelope("plain-legacy-key")
	assert.False(t, ok)
}

func TestDecodeAPITokenEnvelope_MalformedBase64(t *testing.T) {
	_, _, ok := DecodeAPITokenEnvelope(apiTokenEnvelopePrefix + "not!valid!base64")
	assert.False(t, ok)
}

func TestDecodeAPITokenEnvelope_MalformedJSON(t *testing.T) {
	// Correct prefix and valid base64, but the payload is not a JSON object.
	payload := base64.RawURLEncoding.EncodeToString([]byte("not json at all"))
	_, _, ok := DecodeAPITokenEnvelope(apiTokenEnvelopePrefix + payload)
	assert.False(t, ok)
}

func TestDecodeAPITokenEnvelope_EmptyToken(t *testing.T) {
	// Valid JSON envelope but without a token — cannot authenticate, so not a usable envelope.
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"url":"http://x","token":""}`))
	_, _, ok := DecodeAPITokenEnvelope(apiTokenEnvelopePrefix + payload)
	assert.False(t, ok)
}

func TestEncodeAPITokenEnvelope_PayloadIsJSONObject(t *testing.T) {
	envelope, err := EncodeAPITokenEnvelope("http://localhost:4000", "the-key")
	require.NoError(t, err)

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(envelope, apiTokenEnvelopePrefix))
	require.NoError(t, err)

	var obj map[string]any
	require.NoError(t, json.Unmarshal(raw, &obj), "payload must be a JSON object")
	assert.Equal(t, "http://localhost:4000", obj["url"])
	assert.Equal(t, "the-key", obj["token"])
	assert.Len(t, obj, 2, "exactly two fields: url and token")
}
