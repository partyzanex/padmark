package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPing_LivenessFails_ReadinessOK_ReturnsError verifies the exit code reflects BOTH probes:
// a failing /healthz must surface as a non-nil error (non-zero exit) even when /readyz passes.
// Before the fix, pingAction returned only readyErr, masking the liveness failure as exit 0.
func TestPing_LivenessFails_ReadinessOK_ReturnsError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	err := runCLI(context.Background(), "--url", srv.URL, cmdPing)

	require.Error(t, err, "liveness failure must produce a non-zero exit even if readiness is OK")
}

// TestPing_AllProbesOK_ReturnsNil confirms the happy path: both probes pass → nil → exit 0.
func TestPing_AllProbesOK_ReturnsNil(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	err := runCLI(context.Background(), "--url", srv.URL, cmdPing)

	require.NoError(t, err)
}
