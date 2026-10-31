package mcp

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/domain"
)

// newTestCmd runs a full urcli parse of the given args, returning the resolved command so
// tests exercise the same flag/env precedence the binary sees.
func newTestCmd(t *testing.T, args ...string) *urcli.Command {
	t.Helper()

	var parsed *urcli.Command

	app := NewApp()
	app.Action = func(_ context.Context, cmd *urcli.Command) error {
		parsed = cmd

		return nil
	}

	require.NoError(t, app.Run(t.Context(), append([]string{"padmark-mcp"}, args...)))
	require.NotNil(t, parsed)

	return parsed
}

func TestSplitToken_EnvelopeUnpacksURLAndKey(t *testing.T) {
	isolateEnv(t)

	envelope, err := domain.EncodeAPITokenEnvelope("https://notes.example.com", "the-key")
	require.NoError(t, err)

	bearer, baseURL := splitToken(envelope)
	assert.Equal(t, "the-key", bearer)
	assert.Equal(t, "https://notes.example.com", baseURL)
}

func TestSplitToken_BareKeyHasNoURL(t *testing.T) {
	bearer, baseURL := splitToken("legacy-plain-key")
	assert.Equal(t, "legacy-plain-key", bearer, "a bare key is used verbatim as the bearer")
	assert.Empty(t, baseURL)
}

func TestPickServerURL_ExplicitFlagBeatsEnvelope(t *testing.T) {
	isolateEnv(t)

	envelope, err := domain.EncodeAPITokenEnvelope("http://embedded:4000", "k")
	require.NoError(t, err)

	cmd := newTestCmd(t, "--base-url", "http://explicit:9999", "--token", envelope)

	assert.Equal(t, "http://explicit:9999", pickServerURL(cmd, "http://embedded:4000"),
		"an explicit --base-url overrides the URL embedded in the envelope")
}

func TestPickServerURL_DefaultWithoutFlagAndEnvelope(t *testing.T) {
	isolateEnv(t)

	cmd := newTestCmd(t)

	assert.Equal(t, DefaultBaseURL, pickServerURL(cmd, ""))
}

func TestPickServerURL_EnvBaseURL(t *testing.T) {
	isolateEnv(t)
	t.Setenv(EnvBaseURL, "http://from-env:7000")

	cmd := newTestCmd(t)

	assert.Equal(t, "http://from-env:7000", cmd.String(FlagBaseURL), "PADMARK_URL is honoured")
}

func TestDefaultTimeout_IsBounded(t *testing.T) {
	assert.NotZero(t, DefaultTimeout, "a zero default would make tool calls hang on a dead server")
}

// TestCheckInsecureToken covers the cleartext-transport policy: loopback http only warns,
// non-loopback http is refused unless --allow-insecure-http is set, https is silent.
func TestCheckInsecureToken(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		serverURL string
		token     string
		allow     bool
		wantWarn  bool
		wantErr   bool
	}{
		{"remote http is refused", "http://example.com", "secret", false, false, true},
		{"remote http with opt-in warns", "http://example.com", "secret", true, true, false},
		{"localhost warns", "http://localhost:8080", "secret", false, true, false},
		{"127.0.0.1 warns", "http://127.0.0.1:8080", "secret", false, true, false},
		{"ipv6 loopback warns", "http://[::1]:8080", "secret", false, true, false},
		{"lookalike host is refused", "http://localhost.evil.com", "secret", false, false, true},
		{"https is silent", "https://example.com", "secret", false, false, false},
		{"no token is silent", "http://example.com", "", false, false, false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			err := checkInsecureToken(&buf, testCase.serverURL, testCase.token, insecureHTTPSetting(testCase.allow))

			if testCase.wantErr {
				require.ErrorIs(t, err, errInsecureTransport)
				assert.Contains(t, err.Error(), FlagAllowInsecureHTTP)

				return
			}

			require.NoError(t, err)

			if testCase.wantWarn {
				assert.Contains(t, buf.String(), "cleartext")
			} else {
				assert.Empty(t, buf.String())
			}
		})
	}
}

// TestNewPadmarkClient_RefusesInsecureRemote verifies the guard is wired into client creation
// and that the flag opts out.
func TestNewPadmarkClient_RefusesInsecureRemote(t *testing.T) {
	isolateEnv(t)

	_, err := newPadmarkClient(newTestCmd(t, "--base-url", "http://example.com", "--token", "secret"))
	require.ErrorIs(t, err, errInsecureTransport)

	_, err = newPadmarkClient(newTestCmd(t,
		"--base-url", "http://example.com", "--token", "secret", "--allow-insecure-http"))
	require.NoError(t, err)
}
