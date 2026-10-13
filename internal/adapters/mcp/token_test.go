package mcp

import (
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
	t.Setenv(EnvToken, "")
	t.Setenv(EnvBaseURL, "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

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
	t.Setenv(EnvToken, "")
	t.Setenv(EnvBaseURL, "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	envelope, err := domain.EncodeAPITokenEnvelope("http://embedded:4000", "k")
	require.NoError(t, err)

	cmd := newTestCmd(t, "--base-url", "http://explicit:9999", "--token", envelope)

	assert.Equal(t, "http://explicit:9999", pickServerURL(cmd, "http://embedded:4000"),
		"an explicit --base-url overrides the URL embedded in the envelope")
}

func TestPickServerURL_DefaultWithoutFlagAndEnvelope(t *testing.T) {
	t.Setenv(EnvToken, "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cmd := newTestCmd(t)

	assert.Equal(t, DefaultBaseURL, pickServerURL(cmd, ""))
}

func TestPickServerURL_EnvBaseURL(t *testing.T) {
	t.Setenv(EnvToken, "")
	t.Setenv(EnvBaseURL, "http://from-env:7000")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cmd := newTestCmd(t)

	assert.Equal(t, "http://from-env:7000", cmd.String(FlagBaseURL), "PADMARK_URL is honoured")
}

func TestDefaultTimeout_IsBounded(t *testing.T) {
	assert.NotZero(t, DefaultTimeout, "a zero default would make tool calls hang on a dead server")
}
