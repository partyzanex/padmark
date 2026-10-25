package ui_test

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
)

// supervisorEnv marks a re-executed test binary that runs one Padmark server as its child.
// The supervisor kills the server when its stdin reaches EOF. The test process holds the
// write end of that pipe, so the pipe closes when the test process exits for any reason,
// including a -timeout panic, and no server outlives the suite. The name has no PADMARK_
// prefix, so the server environment filter does not depend on it.
const supervisorEnv = "UI_TEST_SUPERVISOR"

// superviseServer runs os.Args[1:] as the server, forwards SIGTERM and SIGINT to it, kills it
// on stdin EOF and returns its exit code.
func superviseServer() int {
	server := exec.Command(os.Args[1], os.Args[2:]...) //nolint:gosec // arguments come from startServer
	server.Env = slices.DeleteFunc(os.Environ(), func(value string) bool {
		return strings.HasPrefix(value, supervisorEnv+"=")
	})
	server.Stdout, server.Stderr = os.Stdout, os.Stderr

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)

	err := server.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start UI server:", err)

		return 1
	}

	go func() {
		for received := range signals {
			_ = server.Process.Signal(received)
		}
	}()

	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = server.Process.Kill()
	}()

	err = server.Wait()

	exitErr, ok := errors.AsType[*exec.ExitError](err)
	if ok && exitErr.ExitCode() >= 0 {
		return exitErr.ExitCode()
	}

	if err != nil {
		return 1
	}

	return 0
}
