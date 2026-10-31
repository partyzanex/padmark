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
func Start(t *testing.T, binary, directory string, args ...string) (baseURL string, stop func()) {
	t.Helper()

	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	address := listener.Addr().String()
	require.NoError(t, listener.Close())

	commandArgs := slices.Concat(args, []string{"--addr", address, "--metrics-addr", ""})

	var output bytes.Buffer

	stop, done := startProcess(t, binary, directory, commandArgs, &output)
	baseURL = "http://" + address

	waitReady(t, baseURL, done, &output, stop)

	return baseURL, stop
}

// startProcess starts the server binary and writes its output to output. It returns a stop
// function, which is also the test cleanup, and a channel that closes when the process exits.
func startProcess(
	t *testing.T, binary, directory string, commandArgs []string, output *bytes.Buffer,
) (stop func(), done <-chan struct{}) {
	t.Helper()

	//nolint:gosec // controlled test binary/arguments
	command := exec.CommandContext(t.Context(), binary, commandArgs...)
	command.Dir = directory

	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "PADMARK_") {
			command.Env = append(command.Env, value)
		}
	}

	command.Stdout, command.Stderr = output, output
	require.NoError(t, command.Start())

	exited := make(chan struct{})

	go func() {
		_ = command.Wait() //nolint:errcheck // a signal exit is expected; early exits fail readiness below

		close(exited)
	}()

	stop = sync.OnceFunc(func() {
		_ = command.Process.Signal(syscall.SIGTERM) //nolint:errcheck // the process may have already exited

		select {
		case <-exited:
		case <-time.After(stopTimeout):
			_ = command.Process.Kill() //nolint:errcheck // may exit between the timeout and Kill

			<-exited
		}
	})
	t.Cleanup(stop)

	return stop, exited
}

// waitReady polls the readiness endpoint of baseURL until it returns 200 OK. It fails the test
// when the process exits first or the startup timeout passes. The timeout also calls stop.
func waitReady(t *testing.T, baseURL string, done <-chan struct{}, output *bytes.Buffer, stop func()) {
	t.Helper()

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
				return
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
