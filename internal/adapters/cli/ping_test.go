package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/suite"
)

type PingSuite struct {
	suite.Suite
}

func TestPingSuite(t *testing.T) {
	suite.Run(t, new(PingSuite))
}

// TestPing_LivenessFails_ReadinessOK_ReturnsError verifies the exit code reflects BOTH probes:
// a failing /healthz must surface as a non-nil error (non-zero exit) even when /readyz passes.
// Before the fix, pingAction returned only readyErr, masking the liveness failure as exit 0.
func (s *PingSuite) TestPing_LivenessFails_ReadinessOK_ReturnsError() {
	isolateEnv(s.T())

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	err := runCLI(s.T().Context(), "--url", srv.URL, cmdPing)

	s.Require().Error(err, "liveness failure must produce a non-zero exit even if readiness is OK")
}

// TestPing_AllProbesOK_ReturnsNil confirms the happy path: both probes pass → nil → exit 0.
func (s *PingSuite) TestPing_AllProbesOK_ReturnsNil() {
	isolateEnv(s.T())

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	err := runCLI(s.T().Context(), "--url", srv.URL, cmdPing)

	s.Require().NoError(err)
}
