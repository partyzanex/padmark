// Package servertest launches isolated server processes against disposable test databases.
package servertest

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	stopTimeout  = 5 * time.Second
	startTimeout = 20 * time.Second
	pollInterval = 25 * time.Millisecond
	probeTimeout = 200 * time.Millisecond
)

// Build compiles the current repository, without touching the operator's bin directory.
func Build(t *testing.T, buildFlags ...string) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "padmark-server")
	args := append([]string{"build", "-buildvcs=false", "-o", binary}, buildFlags...)
	args = append(args, "./cmd/padmark-server")
	//nolint:gosec // repository source and a test-owned output path
	command := exec.CommandContext(t.Context(), "go", args...)
	command.Dir = "../.."
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))

	return binary
}

// Start waits for readiness. Returned stop and test cleanup both stop and reap the process.
//
//nolint:funlen // one test-process lifecycle, including shutdown and readiness diagnostics
func Start(t *testing.T, binary, directory string, args ...string) (baseURL string, stop func()) {
	t.Helper()

	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	address := listener.Addr().String()
	require.NoError(t, listener.Close())

	commandArgs := slices.Concat(args, []string{"--addr", address, "--metrics-addr", ""})
	command := exec.CommandContext(t.Context(), binary, commandArgs...) //nolint:gosec // controlled test binary/arguments
	command.Dir = directory

	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "PADMARK_") {
			command.Env = append(command.Env, value)
		}
	}

	var output bytes.Buffer

	command.Stdout, command.Stderr = &output, &output
	require.NoError(t, command.Start())

	done := make(chan struct{})

	go func() {
		_ = command.Wait() //nolint:errcheck // a signal exit is expected; early exits fail readiness below

		close(done)
	}()

	stop = sync.OnceFunc(func() {
		_ = command.Process.Signal(syscall.SIGTERM) //nolint:errcheck // the process may have already exited

		select {
		case <-done:
		case <-time.After(stopTimeout):
			_ = command.Process.Kill() //nolint:errcheck // may exit between the timeout and Kill

			<-done
		}
	})
	t.Cleanup(stop)

	baseURL = "http://" + address

	client := &http.Client{Timeout: probeTimeout, Transport: nil, CheckRedirect: nil, Jar: nil}
	defer client.CloseIdleConnections()

	deadline := time.NewTimer(startTimeout)
	defer deadline.Stop()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/readyz", http.NoBody)
	require.NoError(t, err)

	for {
		response, requestErr := client.Do(request)
		if requestErr == nil {
			require.NoError(t, response.Body.Close())

			if response.StatusCode == http.StatusOK {
				return baseURL, stop
			}
		}

		select {
		case <-done:
			t.Fatalf("server exited before readiness: %s", output.String())
		case <-deadline.C:
			stop()
			t.Fatalf("server startup timed out: %s", output.String())
		case <-ticker.C:
		}
	}
}
