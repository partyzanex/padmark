package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// TestServerTimeoutPolicy pins the timeout configuration of all three listeners (main,
// redirect, metrics). IdleTimeout and ReadHeaderTimeout must stay the shared constants: a
// literal that silently drops IdleTimeout compiles fine and only shows up at runtime as
// keep-alive connections that linger forever on the auxiliary listeners.
func TestServerTimeoutPolicy(t *testing.T) {
	api := newMainServer("127.0.0.1:0", http.NewServeMux(), 30, 60, 1<<20)
	redirect := newRedirectServer("127.0.0.1:0", nil)
	metrics := newMetricsServer("127.0.0.1:0", prometheus.NewRegistry())

	for name, srv := range map[string]*http.Server{
		"main":     api,
		"redirect": redirect,
		"metrics":  metrics,
	} {
		require.Equal(t, idleTimeout, srv.IdleTimeout, name+": IdleTimeout must be the shared constant")
		require.Equal(t, readHeaderTimeout, srv.ReadHeaderTimeout, name+": ReadHeaderTimeout must be the shared constant")
	}

	// Main-only: operator-configured request limits pass through unchanged.
	require.Equal(t, 30*time.Second, api.ReadTimeout)
	require.Equal(t, 60*time.Second, api.WriteTimeout)
	require.Equal(t, 1<<20, api.MaxHeaderBytes)

	// Auxiliary listeners never read a body and write one small response, so Read/Write
	// timeouts are intentionally unset there.
	require.Zero(t, redirect.ReadTimeout)
	require.Zero(t, redirect.WriteTimeout)
	require.Zero(t, metrics.ReadTimeout)
	require.Zero(t, metrics.WriteTimeout)
}
