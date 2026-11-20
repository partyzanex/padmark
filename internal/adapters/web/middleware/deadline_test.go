package middleware

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStatusRecorder_PassesDeadlines makes sure that a handler behind the logging middleware
// can extend its connection deadlines. File uploads and downloads need this to run longer
// than the server read and write timeouts.
func TestStatusRecorder_PassesDeadlines(t *testing.T) {
	t.Parallel()

	result := make(chan error, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ctrl := http.NewResponseController(w)
		deadline := time.Now().Add(time.Hour)

		readErr := ctrl.SetReadDeadline(deadline)
		writeErr := ctrl.SetWriteDeadline(deadline)

		if readErr != nil {
			result <- readErr
		} else {
			result <- writeErr
		}

		w.WriteHeader(http.StatusNoContent)
	})

	server := httptest.NewServer(WithLogging(slog.New(slog.DiscardHandler), handler))
	t.Cleanup(server.Close)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
	require.NoError(t, err)

	resp, err := server.Client().Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, <-result)
}
