package middleware

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// LiveServerSuite runs the middleware behind a real HTTP server (httptest.Server). Only a real
// connection shows the connection deadlines and an aborted response.
type LiveServerSuite struct {
	suite.Suite
}

func TestLiveServerSuite(t *testing.T) {
	suite.Run(t, new(LiveServerSuite))
}

// startServer starts a server with handler. The test cleanup closes it.
func (s *LiveServerSuite) startServer(handler http.Handler) *httptest.Server {
	server := httptest.NewServer(handler)
	s.T().Cleanup(server.Close)

	return server
}

// TestStatusRecorder_PassesDeadlines makes sure that a handler behind the logging middleware
// can extend its connection deadlines. File uploads and downloads need this to run longer
// than the server read and write timeouts.
func (s *LiveServerSuite) TestStatusRecorder_PassesDeadlines() {
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

	server := s.startServer(WithLogging(slog.New(slog.DiscardHandler), handler))

	req, err := http.NewRequestWithContext(s.T().Context(), http.MethodGet, server.URL, http.NoBody)
	s.Require().NoError(err)

	resp, err := server.Client().Do(req)
	s.Require().NoError(err)
	s.Require().NoError(resp.Body.Close())
	s.Require().NoError(<-result)
}
