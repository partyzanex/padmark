package cli_test

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	adaptercli "github.com/partyzanex/padmark/internal/adapters/cli"
	"github.com/partyzanex/padmark/internal/errorcontext"
)

func isolateCLI(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		adaptercli.EnvURL, adaptercli.EnvToken, adaptercli.EnvTimeout, adaptercli.EnvEditCode,
	} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
}

func runCLI(t *testing.T, serverURL string, args ...string) error {
	t.Helper()

	app := adaptercli.NewApp()
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	command := make([]string, 0, 5+len(args))
	command = append(command, "padmark-cli", "--url", serverURL, "--token", "sentinel-test-token")

	return errorcontext.Wrap(app.Run(t.Context(), append(command, args...)), "run CLI")
}

func TestCLIResponseErrorsPreserveIdentity(t *testing.T) {
	isolateCLI(t)

	for _, test := range []struct {
		name, body, context string
		status              int
		args                []string
		causes              []error
	}{
		{
			name: "unauthorized", status: http.StatusUnauthorized, body: `{"message":"sign in"}`,
			args: []string{"get", "example"}, context: "decode get note response",
			causes: []error{adaptercli.ErrUnauthorized},
		},
		{
			name: "missing note", status: http.StatusNotFound, body: `{"message":"missing"}`,
			args: []string{"get", "example"}, context: "decode get note response",
			causes: []error{adaptercli.ErrNotFound, adaptercli.ErrNoteNotFound},
		},
		{
			name: "missing link", status: http.StatusNotFound, body: `{"message":"missing"}`,
			args: []string{"expand", "example"}, context: "decode get link response",
			causes: []error{adaptercli.ErrNotFound, adaptercli.ErrLinkNotFound},
		},
		{
			name: "expired note", status: http.StatusGone, body: `{"message":"gone"}`,
			args: []string{"get", "example"}, context: "decode get note response",
			causes: []error{adaptercli.ErrNoteGone},
		},
		{
			name: "disabled API", status: http.StatusServiceUnavailable,
			body: `{"message":"translated text","code":"api_disabled"}`,
			args: []string{"get", "example"}, context: "decode get note response",
			causes: []error{adaptercli.ErrAPIDisabled},
		},
		{
			name: "temporary service failure", status: http.StatusServiceUnavailable,
			body: `{"message":"maintenance"}`, args: []string{"get", "example"},
			context: "maintenance", causes: []error{adaptercli.ErrServiceUnavailable},
		},
		{
			name: "edit forbidden", status: http.StatusForbidden, body: `{"message":"denied"}`,
			args:    []string{"edit", "example", "--content", "fixture", "--edit-code", "fixture-code"},
			context: "decode update note response", causes: []error{adaptercli.ErrForbidden},
		},
		{
			name: "invalid update", status: http.StatusBadRequest, body: `{"message":"invalid fixture title"}`,
			args:    []string{"edit", "example", "--content", "fixture", "--edit-code", "fixture-code"},
			context: "invalid fixture title", causes: []error{adaptercli.ErrBadRequest},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)

				_, err := io.WriteString(w, test.body)
				if err != nil {
					t.Errorf("write fixture response: %v", err)
				}
			}))
			defer server.Close()

			for range 2 {
				err := runCLI(t, server.URL, test.args...)
				require.ErrorContains(t, err, test.context)

				for _, cause := range test.causes {
					require.ErrorIs(t, err, cause)
				}
			}
		})
	}
}

func TestCLITransportErrorPreservesType(t *testing.T) {
	isolateCLI(t)

	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()

	err := runCLI(t, server.URL, "get", "example")
	require.ErrorContains(t, err, "get note")
	_, ok := errors.AsType[*net.OpError](err)
	require.True(t, ok, "network errors must remain available through errors.As")
}

func TestCLIMissingIDIsStableAndHasContext(t *testing.T) {
	isolateCLI(t)

	first := runCLI(t, "http://127.0.0.1:1", "expand")
	second := runCLI(t, "http://127.0.0.1:1", "expand")
	require.ErrorContains(t, first, "read link ID")
	require.ErrorContains(t, second, "read link ID")

	cause := first
	for unwrapped := errors.Unwrap(cause); unwrapped != nil; unwrapped = errors.Unwrap(cause) {
		cause = unwrapped
	}

	require.ErrorIs(t, second, cause)
}

func TestCLIOutputSuccessRemainsNil(t *testing.T) {
	isolateCLI(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		_, err := io.WriteString(w, `{"id":"fixture","title":"Fixture","content":"text",`+
			`"content_type":"text/plain","views":0,"burn_after_reading":false,`+
			`"created_at":"2026-10-25T12:00:00Z","updated_at":"2026-10-25T12:00:00Z"}`)
		if err != nil {
			t.Errorf("write fixture response: %v", err)
		}
	}))
	defer server.Close()

	require.NoError(t, runCLI(t, server.URL, "get", "example", "--json"))
}
