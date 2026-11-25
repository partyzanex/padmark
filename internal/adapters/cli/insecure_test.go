package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/suite"
)

type InsecureSuite struct {
	suite.Suite
}

func TestInsecureSuite(t *testing.T) {
	suite.Run(t, new(InsecureSuite))
}

// TestCheckInsecureToken_WiredIntoClient verifies the warning is actually emitted to the
// command's error writer when a real command builds its HTTP client with a token over http://.
func (s *InsecureSuite) TestCheckInsecureToken_WiredIntoClient() {
	isolateEnv(s.T())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var errBuf bytes.Buffer

	app := NewApp()
	app.ErrWriter = &errBuf

	err := app.Run(s.T().Context(), []string{testBin, "--url", srv.URL, "--token", "secret", cmdPing})

	s.Require().NoError(err)
	s.Contains(errBuf.String(), "cleartext", "insecure-token warning must reach stderr")
}

// TestCheckInsecureSetup_WiredIntoAction verifies the warning is emitted to the command's error
// writer when a real setup run posts the bootstrap credentials over http://.
func (s *InsecureSuite) TestCheckInsecureSetup_WiredIntoAction() {
	isolateEnv(s.T())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"otp_auth_uri":"otpauth://totp/padmark:root"}`))
	}))
	defer srv.Close()

	var errBuf bytes.Buffer

	app := NewApp()
	app.ErrWriter = &errBuf

	err := app.Run(s.T().Context(), []string{
		testBin, "--url", srv.URL, cmdSetup, "--username", "root", "--password", "FlowP@ss123!",
	})

	s.Require().NoError(err)
	s.Contains(errBuf.String(), "cleartext", "insecure-setup warning must reach stderr")
}
