package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSetup_PasswordPipedToStdin verifies the no-dependency password path: without --password,
// the setup password comes from the first line of piped stdin — visible-echo TTY prompts are
// deliberately rejected as a footgun instead of allowed.
func TestSetup_PasswordPipedToStdin(t *testing.T) {
	// Replace stdin with a pipe carrying the password; no --password flag.
	r, w, err := os.Pipe()
	require.NoError(t, err)

	_, writeErr := w.WriteString("P1pedP@ss12!\n")

	require.NoError(t, writeErr)

	require.NoError(t, w.Close())

	saved := os.Stdin
	os.Stdin = r

	t.Cleanup(func() { os.Stdin = saved })

	// The server answers 400 for whatever we send — the test only pins how the password was
	// resolved, so the command must fail on the HTTP response, not on password resolution.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)

		_, _ = w.Write([]byte(`{"message":"The first admin already exists."}`))
	}))
	defer srv.Close()

	err = runCLI(context.Background(), "--url", srv.URL, "setup", "--username", "admin")
	require.Error(t, err)
	require.Contains(t, err.Error(), "The first admin already exists",
		"piped password must be accepted and reach the server")
}

// TestSetup_TTYWithoutPassword_FailsFast — an interactive stdin with no --password is an
// immediate error, not a visible-echo prompt.
func TestSetup_TTYWithoutPassword_FailsFast(t *testing.T) {
	// os.Stdin in `go test` is not a char device, but the error path is the same: nothing piped
	// and no flag → the command must refuse rather than block or prompt.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server must not be reached without a password")
	}))
	defer srv.Close()

	err := runCLI(context.Background(), "--url", srv.URL, "setup", "--username", "admin")
	require.Error(t, err)
	require.ErrorIs(t, err, errSetupNoPassword)
}
