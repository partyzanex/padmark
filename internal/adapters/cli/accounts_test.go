package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/suite"
)

type CreateTokenSuite struct {
	suite.Suite
}

func TestCreateTokenSuite(t *testing.T) {
	suite.Run(t, new(CreateTokenSuite))
}

// TestCreateToken_RequiresTOTPCodeBeforeAnyRequest: the CLI always authenticates with a bearer
// key, which may issue only with a step-up, so a missing --totp-code fails locally.
func (s *CreateTokenSuite) TestCreateToken_RequiresTOTPCodeBeforeAnyRequest() {
	for _, command := range []string{"create-token", "create-invite"} {
		s.Run(command, func() {
			isolateEnv(s.T())

			var hits atomic.Int32

			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
			defer srv.Close()

			err := runCLI(s.T().Context(), "--url", srv.URL, "--token", "bare-key", command, "--password", "pw")
			s.Require().ErrorIs(err, errStepUpNoTOTPCode)
			s.Zero(hits.Load(), "no request may be sent without a step-up")
		})
	}
}

// TestCreateToken_SendsStepUpAndLifetime: the step-up and the lifetime reach the server body.
func (s *CreateTokenSuite) TestCreateToken_SendsStepUpAndLifetime() {
	isolateEnv(s.T())

	var got struct {
		TTLDays  int    `json:"ttl_days"`
		Password string `json:"password"`
		TOTPCode string `json:"totp_code"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.NoError(json.NewDecoder(r.Body).Decode(&got))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"pmk_x"}`))
	}))
	defer srv.Close()

	s.Require().NoError(runCLI(s.T().Context(), "--url", srv.URL, "--token", "bare-key", "create-token",
		"--password", "pw", "--totp-code", "123456", "--ttl-days", "90"))

	s.Equal(90, got.TTLDays)
	s.Equal("pw", got.Password)
	s.Equal("123456", got.TOTPCode)
}

// TestCreateToken_StepUpRefusalIsReported: a 403 from the step-up rule is surfaced, not swallowed.
func (s *CreateTokenSuite) TestCreateToken_StepUpRefusalIsReported() {
	isolateEnv(s.T())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"re-authentication failed","code":"step_up_failed"}`))
	}))
	defer srv.Close()

	err := runCLI(s.T().Context(), "--url", srv.URL, "--token", "bare-key", "create-token",
		"--password", "pw", "--totp-code", "000000")
	s.Require().ErrorContains(err, "step-up refused: re-authentication failed")
}
