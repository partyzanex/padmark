package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/tokenfile"
)

// envelopeToken builds a pmk_ envelope token pinning baseURL.
func envelopeToken(t *testing.T, baseURL string) string {
	t.Helper()

	token, err := domain.EncodeAPITokenEnvelope(baseURL, "secret-key")
	require.NoError(t, err)

	return token
}

// runClientOnly replaces the root action with its guarded first step (newPadmarkClient) so
// tests exercise the trust gate without entering the stdio protocol loop: the loop reads the
// real process stdin and is not hermetic. newPadmarkClient performs no network I/O, so a nil
// error here means the gate let the pinned (or explicit) URL through.
func runClientOnly(ctx context.Context, args ...string) error {
	app := NewApp()
	app.Action = func(_ context.Context, cmd *urcli.Command) error {
		_, err := newPadmarkClient(cmd)

		return err
	}

	return app.Run(ctx, append([]string{"padmark-mcp"}, args...))
}

// TestTrustCommand_RecordsEnvelopeOrigin verifies `padmark-mcp trust` records the origin
// pinned by the envelope — the MCP-side escape hatch, since stdio serving cannot prompt.
func TestTrustCommand_RecordsEnvelopeOrigin(t *testing.T) {
	isolateEnv(t)

	token := envelopeToken(t, "https://example.com")

	require.NoError(t, NewApp().Run(t.Context(), []string{"padmark-mcp", "trust", "--token", token}))

	assert.True(t, tokenfile.IsKnownOrigin("https://example.com"))
}

// TestTrustCommand_RequiresSomethingToTrust verifies a bare legacy token with no --base-url
// is rejected: there is no pinned URL to record.
func TestTrustCommand_RequiresSomethingToTrust(t *testing.T) {
	isolateEnv(t)

	err := NewApp().Run(t.Context(), []string{"padmark-mcp", "trust", "--token", "legacy-bare-key"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing to trust")
}

// TestTrustCommand_ExplicitBaseURLWins verifies an explicit --base-url is trusted instead of
// the envelope's pinned URL when both are present — the same precedence request routing uses.
func TestTrustCommand_ExplicitBaseURLWins(t *testing.T) {
	isolateEnv(t)

	token := envelopeToken(t, "https://evil.example")

	require.NoError(t, NewApp().Run(t.Context(),
		[]string{"padmark-mcp", "trust", "--token", token, "--base-url", "https://good.example"}))

	assert.True(t, tokenfile.IsKnownOrigin("https://good.example"))
	assert.False(t, tokenfile.IsKnownOrigin("https://evil.example"))
}

// TestTrustedServerGate_BlocksUnknownPinnedOrigin is the core R-08 guard for the MCP surface:
// an envelope token pinning an untrusted server must fail during client construction — before
// the stdio protocol session starts, so no credentials can leave for the pinned host.
func TestTrustedServerGate_BlocksUnknownPinnedOrigin(t *testing.T) {
	isolateEnv(t)

	token := envelopeToken(t, "https://untrusted.example")

	err := runClientOnly(t.Context(), "--token", token)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not trusted yet")
	assert.Contains(t, err.Error(), "padmark-mcp trust", "error must name the escape hatch")
}

// TestTrustedServerGate_ExplicitBaseURLOverridesEnvelope verifies an explicit --base-url is
// its own confirmation: the envelope no longer decides, so the gate passes and no trust
// record is created for the pinned URL.
func TestTrustedServerGate_ExplicitBaseURLOverridesEnvelope(t *testing.T) {
	isolateEnv(t)

	token := envelopeToken(t, "https://untrusted.example")

	require.NoError(t, runClientOnly(t.Context(), "--token", token, "--base-url", "https://explicit.example"))
	assert.False(t, tokenfile.IsKnownOrigin("https://untrusted.example"))
}

// TestTrustedServerGate_TrustedOriginProceeds verifies the full trust-on-first-use cycle:
// after `padmark-mcp trust`, the same envelope builds a client without further questions.
func TestTrustedServerGate_TrustedOriginProceeds(t *testing.T) {
	isolateEnv(t)

	token := envelopeToken(t, "https://example.com")

	require.NoError(t, NewApp().Run(t.Context(), []string{"padmark-mcp", "trust", "--token", token}))
	require.NoError(t, runClientOnly(t.Context(), "--token", token))
}
