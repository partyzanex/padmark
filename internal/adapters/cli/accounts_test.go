package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateToken_RequiresTOTPCodeBeforeAnyRequest: the CLI always authenticates with a bearer
// key, which may issue only with a step-up, so a missing --totp-code fails locally.
func TestCreateToken_RequiresTOTPCodeBeforeAnyRequest(t *testing.T) {
	for _, command := range []string{"create-token", "create-invite"} {
		t.Run(command, func(t *testing.T) {
			isolateEnv(t)

			var hits atomic.Int32

			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
			defer srv.Close()

			err := runCLI(t.Context(), "--url", srv.URL, "--token", "bare-key", command, "--password", "pw")
			require.ErrorIs(t, err, errStepUpNoTOTPCode)
			assert.Zero(t, hits.Load(), "no request may be sent without a step-up")
		})
	}
}

// TestCreateToken_SendsStepUpAndLifetime: the step-up and the lifetime reach the server body.
func TestCreateToken_SendsStepUpAndLifetime(t *testing.T) {
	isolateEnv(t)

	var got struct {
		TTLDays  int    `json:"ttl_days"`
		Password string `json:"password"`
		TOTPCode string `json:"totp_code"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"pmk_x"}`))
	}))
	defer srv.Close()

	require.NoError(t, runCLI(t.Context(), "--url", srv.URL, "--token", "bare-key", "create-token",
		"--password", "pw", "--totp-code", "123456", "--ttl-days", "90"))

	assert.Equal(t, 90, got.TTLDays)
	assert.Equal(t, "pw", got.Password)
	assert.Equal(t, "123456", got.TOTPCode)
}

// TestCreateToken_StepUpRefusalIsReported: a 403 from the step-up rule is surfaced, not swallowed.
func TestCreateToken_StepUpRefusalIsReported(t *testing.T) {
	isolateEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"re-authentication failed","code":"step_up_failed"}`))
	}))
	defer srv.Close()

	err := runCLI(t.Context(), "--url", srv.URL, "--token", "bare-key", "create-token",
		"--password", "pw", "--totp-code", "000000")
	require.ErrorContains(t, err, "step-up refused: re-authentication failed")
}
