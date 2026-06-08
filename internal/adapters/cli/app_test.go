package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRequestTimeout_SlowServer_FailsFast verifies the --timeout flag is wired into the HTTP
// client: against a server that never responds in time, the command aborts after the timeout
// instead of hanging. The slow server sleeps far longer than the timeout, so a fast failure
// (well under the sleep) proves the bound is enforced rather than ignored.
func TestRequestTimeout_SlowServer_FailsFast(t *testing.T) {
	t.Parallel()

	// serverDelay >> timeout so the client's deadline fires first. elapsed is measured before
	// srv.Close() (which blocks until the in-flight handler's sleep ends), so the assertion sees
	// only the client-side fail-fast time, not the cleanup wait.
	const (
		serverDelay = 800 * time.Millisecond
		timeout     = "50ms"
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(serverDelay)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	start := time.Now()
	err := runCLI(context.Background(), "--url", srv.URL, "--timeout", timeout, cmdGet, "some-id")
	elapsed := time.Since(start)

	require.Error(t, err, "request must fail when the server is slower than the timeout")
	assert.Less(t, elapsed, 400*time.Millisecond,
		"command must fail fast on timeout, not wait for the slow server (got %s)", elapsed)
}
