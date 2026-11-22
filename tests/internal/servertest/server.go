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

	return StartWithEnv(t, binary, directory, nil, args...)
}

// StartWithEnv is Start with more environment variables for the server process, in the
// KEY=value form. Use it for settings that the server reads only from the environment, for
// example the S3 keys of a test container. The PADMARK_* variables of the developer stay out.
func StartWithEnv(t *testing.T, binary, directory string, env []string, args ...string) (baseURL string, stop func()) {
	t.Helper()

	address, commandArgs := listenArgs(t, args)

	var output bytes.Buffer

	_, stop, done := startProcess(t, binary, directory, commandArgs, env, &output)
	baseURL = "http://" + address

	waitReady(t, baseURL, done, &output, stop)

	return baseURL, stop
}

// listenArgs selects a free loopback port. It returns the address and args together with the
// flags that put the main listener there and turn the metrics listener off.
func listenArgs(t *testing.T, args []string) (address string, commandArgs []string) {
	t.Helper()

	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	address = listener.Addr().String()
	require.NoError(t, listener.Close())

	return address, slices.Concat(args, []string{"--addr", address, "--metrics-addr", ""})
}

// Process is a server process for a test of the stop sequence: the test sends the signal and
// examines the exit code and the output.
type Process struct {
	command *exec.Cmd
	exited  <-chan struct{}
	output  *bytes.Buffer
	// BaseURL is the address of the main listener.
	BaseURL string
}

// Launch is Start for a test that stops the server itself. The test cleanup also stops and
// reaps the process.
func Launch(t *testing.T, binary, directory string, args ...string) *Process {
	t.Helper()

	address, commandArgs := listenArgs(t, args)
	output := new(bytes.Buffer)

	command, stop, done := startProcess(t, binary, directory, commandArgs, nil, output)
	process := &Process{command: command, exited: done, output: output, BaseURL: "http://" + address}

	waitReady(t, process.BaseURL, done, output, stop)

	return process
}

// Terminate sends SIGTERM to the server and does not wait for the exit.
func (p *Process) Terminate(t *testing.T) {
	t.Helper()

	require.NoError(t, p.command.Process.Signal(syscall.SIGTERM))
}

// WaitExit waits for the exit of the server for at most limit and fails the test after that
// time. It returns the exit code and all output of the server.
func (p *Process) WaitExit(t *testing.T, limit time.Duration) (exitCode int, output string) {
	t.Helper()

	select {
	case <-p.exited:
	case <-time.After(limit):
		t.Fatalf("the server did not exit in %s", limit)
	}

	// The process exited, so nothing writes to the output buffer now.
	return p.command.ProcessState.ExitCode(), p.output.String()
}

// startProcess starts the server binary and writes its output to output. It returns the
// command, a stop function, which is also the test cleanup, and a channel that closes when the
// process exits.
func startProcess(
	t *testing.T, binary, directory string, commandArgs, env []string, output *bytes.Buffer,
) (process *exec.Cmd, stop func(), done <-chan struct{}) {
	t.Helper()

	//nolint:gosec // controlled test binary/arguments
	command := exec.CommandContext(t.Context(), binary, commandArgs...)
	command.Dir = directory

	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "PADMARK_") {
			command.Env = append(command.Env, value)
		}
	}

	command.Env = append(command.Env, env...)

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

	return command, stop, exited
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
