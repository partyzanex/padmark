package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// newPadmarkSrv starts a test server answering both probes so ping succeeds when routed to it.
func newPadmarkSrv() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	return httptest.NewServer(mux)
}

// TestTrustCommand_RecordsEnvelopeOrigin verifies `trust` resolves the pinned URL from the
// envelope and records its origin, printing what was trusted and where.
func TestTrustCommand_RecordsEnvelopeOrigin(t *testing.T) {
	isolateEnv(t)

	token := envelopeToken(t, "https://example.com")

	require.NoError(t, runCLI(t.Context(), "--token", token, cmdTrust))

	assert.True(t, tokenfile.IsKnownOrigin("https://example.com"))
}

// TestTrustCommand_RequiresSomethingToTrust verifies a bare legacy token with no --url is
// rejected: there is no pinned URL to record.
func TestTrustCommand_RequiresSomethingToTrust(t *testing.T) {
	isolateEnv(t)

	err := runCLI(t.Context(), "--token", "legacy-bare-key", cmdTrust)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "nothing to trust")
}

// TestTrustCommand_ExplicitURLWins verifies an explicit --url is trusted instead of the
// envelope's pinned URL when both are present — the same precedence request routing uses.
func TestTrustCommand_ExplicitURLWins(t *testing.T) {
	isolateEnv(t)

	token := envelopeToken(t, "https://evil.example")

	require.NoError(t, runCLI(t.Context(), "--token", token, "--url", "https://good.example", cmdTrust))

	assert.True(t, tokenfile.IsKnownOrigin("https://good.example"))
	assert.False(t, tokenfile.IsKnownOrigin("https://evil.example"))
}

// TestTrustedServerGate_BlocksUnknownPinnedOrigin is the core R-08 guard: a command run with
// an envelope token pinning an untrusted server must fail before any request is sent. The
// target server deliberately answers everything, so reaching it would mean the gate leaked.
func TestTrustedServerGate_BlocksUnknownPinnedOrigin(t *testing.T) {
	isolateEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	token := envelopeToken(t, srv.URL)

	err := runCLI(t.Context(), "--token", token, cmdPing)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not trusted yet")
}

// TestTrustedServerGate_ExplicitURLOverridesEnvelope verifies an explicit --url is its own
// confirmation: the envelope no longer decides, so no trust record is needed.
func TestTrustedServerGate_ExplicitURLOverridesEnvelope(t *testing.T) {
	isolateEnv(t)

	srv := newPadmarkSrv()
	defer srv.Close()

	token := envelopeToken(t, "https://evil.example")

	require.NoError(t, runCLI(t.Context(), "--token", token, "--url", srv.URL, cmdPing))
	assert.False(t, tokenfile.IsKnownOrigin("https://evil.example"))
}

// TestTrustedServerGate_TrustedOriginProceeds verifies the full trust-on-first-use cycle:
// after `trust`, the same envelope routes to the pinned server without further questions.
func TestTrustedServerGate_TrustedOriginProceeds(t *testing.T) {
	isolateEnv(t)

	srv := newPadmarkSrv()
	defer srv.Close()

	token := envelopeToken(t, srv.URL)

	require.NoError(t, runCLI(t.Context(), "--token", token, cmdTrust))
	require.NoError(t, runCLI(t.Context(), "--token", token, cmdPing))
}

// TestTrustedServerGate_BareTokenUnaffected verifies legacy bare tokens (no embedded URL)
// keep working with zero new questions — the gate only applies to envelope pins.
func TestTrustedServerGate_BareTokenUnaffected(t *testing.T) {
	isolateEnv(t)

	srv := newPadmarkSrv()
	defer srv.Close()

	require.NoError(t, runCLI(t.Context(), "--token", "legacy-bare-key", "--url", srv.URL, cmdPing))
}

// TestTrustedServerGate_ErrorMessageActionable pins the escape hatches named in the error so
// the fix instructions cannot drift from the actual command and flag names.
func TestTrustedServerGate_ErrorMessageActionable(t *testing.T) {
	isolateEnv(t)

	token := envelopeToken(t, "https://untrusted.example")

	err := runCLI(t.Context(), "--token", token, cmdPing)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "padmark-cli trust")
	assert.Contains(t, err.Error(), "https://untrusted.example",
		"error must name the pinned origin")
}
