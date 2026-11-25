package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/internal/clientauth"
)

// setupArgs returns the credential flags of a setup run that would otherwise succeed: only
// the server choice under test decides whether it may proceed.
func setupArgs() []string {
	return []string{cmdSetup, "--username", "root", "--password", "FlowP@ss123!"}
}

// newSetupSrv starts a /setup endpoint answering 201 with an otpauth URI and counts the hits;
// it also records whether any request carried an Authorization header (setup must not).
func newSetupSrv(hits *atomic.Int32, sawAuth *atomic.Bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)

		if r.Header.Get("Authorization") != "" {
			sawAuth.Store(true)
		}

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"otp_auth_uri":"otpauth://totp/padmark:root"}`))
	}))
}

type SetupCommandSuite struct {
	suite.Suite
}

func TestSetupCommandSuite(t *testing.T) {
	suite.Run(t, new(SetupCommandSuite))
}

// TestSetup_PasswordPipedToStdin verifies the no-dependency password path: without --password,
// the setup password comes from the first line of piped stdin — visible-echo TTY prompts are
// deliberately rejected as a footgun instead of allowed.
func (s *SetupCommandSuite) TestSetup_PasswordPipedToStdin() {
	isolateEnv(s.T())

	// Replace stdin with a pipe carrying the password; no --password flag.
	r, w, err := os.Pipe()
	s.Require().NoError(err)

	_, writeErr := w.WriteString("P1pedP@ss12!\n")

	s.Require().NoError(writeErr)

	s.Require().NoError(w.Close())

	saved := os.Stdin
	os.Stdin = r

	s.T().Cleanup(func() { os.Stdin = saved })

	// The server answers 400 for whatever we send — the test only pins how the password was
	// resolved, so the command must fail on the HTTP response, not on password resolution.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)

		_, _ = w.Write([]byte(`{"message":"The first admin already exists."}`))
	}))
	defer srv.Close()

	err = runCLI(s.T().Context(), "--url", srv.URL, "setup", "--username", "admin")
	s.Require().Error(err)
	s.Require().Contains(err.Error(), "The first admin already exists",
		"piped password must be accepted and reach the server")
}

// TestSetup_TTYWithoutPassword_FailsFast — an interactive stdin with no --password is an
// immediate error, not a visible-echo prompt.
func (s *SetupCommandSuite) TestSetup_TTYWithoutPassword_FailsFast() {
	isolateEnv(s.T())

	// os.Stdin in `go test` is not a char device, but the error path is the same: nothing piped
	// and no flag → the command must refuse rather than block or prompt.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		s.T().Error("server must not be reached without a password")
	}))
	defer srv.Close()

	err := runCLI(s.T().Context(), "--url", srv.URL, "setup", "--username", "admin")
	s.Require().Error(err)
	s.Require().ErrorIs(err, errSetupNoPassword)
}

// TestSetup_UntrustedEnvelope_BlocksBeforeAnyRequest is the R-08 regression for setup: a pmk_
// envelope pinning an untrusted server, from any token source and with no --url, must fail
// before the insecure warning and before a single byte reaches the pinned server — otherwise
// the bootstrap password and the returned TOTP secret go wherever a third-party token points.
func (s *SetupCommandSuite) TestSetup_UntrustedEnvelope_BlocksBeforeAnyRequest() {
	cases := []struct {
		name string
		// provide runs in the subtest: s.T() and s.Require() belong to it.
		provide func(configDir, token string) []string
	}{
		{"token flag", func(_, token string) []string {
			return []string{"--token", token}
		}},
		{"PADMARK_TOKEN env", func(_, token string) []string {
			s.T().Setenv(EnvToken, token)

			return nil
		}},
		{"token file", func(configDir, token string) []string {
			s.Require().NoError(os.MkdirAll(filepath.Join(configDir, "padmark"), 0o700))
			s.Require().NoError(os.WriteFile(filepath.Join(configDir, "padmark", "token"), []byte(token), 0o600))

			return nil
		}},
	}

	for _, testCase := range cases {
		s.Run(testCase.name, func() {
			configDir := isolateEnv(s.T())

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				s.T().Error("setup must not reach a server pinned by an untrusted envelope")
				w.WriteHeader(http.StatusCreated)
			}))
			defer srv.Close()

			args := testCase.provide(configDir, envelopeToken(s.T(), srv.URL))

			var errBuf bytes.Buffer

			app := NewApp()
			app.ErrWriter = &errBuf

			err := app.Run(s.T().Context(), append(append([]string{testBin}, args...), setupArgs()...))

			s.Require().Error(err)
			s.Contains(err.Error(), "not trusted yet")
			s.NotContains(errBuf.String(), "cleartext", "the gate must run before the insecure warning")
		})
	}
}

// TestSetup_ExplicitURLOverridesUntrustedEnvelope verifies an explicit --url is its own
// confirmation for setup too: the envelope no longer decides, so setup proceeds to --url.
func TestSetup_ExplicitURLOverridesUntrustedEnvelope(t *testing.T) {
	isolateEnv(t)

	var (
		hits    atomic.Int32
		sawAuth atomic.Bool
	)

	srv := newSetupSrv(&hits, &sawAuth)
	defer srv.Close()

	token := envelopeToken(t, "https://untrusted.example")

	require.NoError(t, runCLI(t.Context(), append([]string{"--token", token, "--url", srv.URL}, setupArgs()...)...))
	assert.Equal(t, int32(1), hits.Load(), "setup must reach the explicit --url server")
	assert.False(t, sawAuth.Load(), "setup is unauthenticated and must not send the bearer key")
}

// TestSetup_TrustedEnvelopeProceeds verifies the trust-on-first-use cycle for setup: once the
// pinned origin is recorded with `trust`, setup follows the envelope URL without --url.
func TestSetup_TrustedEnvelopeProceeds(t *testing.T) {
	isolateEnv(t)

	var (
		hits    atomic.Int32
		sawAuth atomic.Bool
	)

	srv := newSetupSrv(&hits, &sawAuth)
	defer srv.Close()

	token := envelopeToken(t, srv.URL)

	require.NoError(t, runCLI(t.Context(), "--token", token, cmdTrust))
	require.NoError(t, runCLI(t.Context(), append([]string{"--token", token}, setupArgs()...)...))
	assert.Equal(t, int32(1), hits.Load(), "setup must reach the trusted pinned server")
	assert.False(t, sawAuth.Load(), "setup is unauthenticated and must not send the bearer key")
}

// TestSetup_SendsBootstrapTokenFromEnv: bootstrapping the first admin forwards the server's
// bootstrap token from the environment (never a flag); an invite enrollment does not send it.
func (s *SetupCommandSuite) TestSetup_SendsBootstrapTokenFromEnv() {
	const bootstrap = "operator-bootstrap-token-0123456789abcdef"

	for _, testCase := range []struct {
		name  string
		extra []string
		want  string
	}{
		{"first admin", nil, bootstrap},
		{"invite enrollment", []string{"--invite", "inv-123"}, ""},
	} {
		s.Run(testCase.name, func() {
			isolateEnv(s.T())
			s.T().Setenv(EnvBootstrapToken, bootstrap)

			var got struct {
				BootstrapToken *string `json:"bootstrap_token"`
			}

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				s.NoError(json.NewDecoder(r.Body).Decode(&got))

				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"otp_auth_uri":"otpauth://totp/padmark:root"}`))
			}))
			defer srv.Close()

			args := append(append([]string{"--url", srv.URL}, setupArgs()...), testCase.extra...)
			s.Require().NoError(runCLI(s.T().Context(), args...))

			if testCase.want == "" {
				s.Nil(got.BootstrapToken, "an invite enrollment must not send the bootstrap token")
			} else {
				s.Require().NotNil(got.BootstrapToken)
				s.Equal(testCase.want, *got.BootstrapToken)
			}
		})
	}
}

// TestSetup_CrossOriginRedirectKeepsSecrets proves that a 307 or 308 answer of /setup does not
// carry the password and the bootstrap token to a different origin. net/http sends the request
// body again on these redirects, so only the redirect guard of the shared client stops it.
func (s *SetupCommandSuite) TestSetup_CrossOriginRedirectKeepsSecrets() {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		s.Run(http.StatusText(status), func() {
			isolateEnv(s.T())
			s.T().Setenv(EnvBootstrapToken, "bootstrap-secret")

			var otherHits atomic.Int32

			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				otherHits.Add(1)
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"otp_auth_uri":"otpauth://totp/padmark:root"}`))
			}))
			defer other.Close()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, other.URL+"/setup", status)
			}))
			defer srv.Close()

			err := runCLI(s.T().Context(), append([]string{"--url", srv.URL}, setupArgs()...)...)
			s.Require().ErrorIs(err, clientauth.ErrCrossOriginRedirect, "a refused redirect must fail the command")
			s.Zero(otherHits.Load(), "the other origin must not receive the setup request")
		})
	}
}
