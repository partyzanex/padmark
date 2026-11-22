package clientauth_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/internal/clientauth"
	"github.com/partyzanex/padmark/internal/domain"
)

const (
	testDefaultURL = "http://localhost:8080"
	testProgram    = "padmark-test"
	testServerFlag = "base-url"
)

func TestSplitToken_Envelope(t *testing.T) {
	t.Parallel()

	envelope, err := domain.EncodeAPITokenEnvelope("https://notes.example.com", "the-key")
	require.NoError(t, err)

	bearer, serverURL := clientauth.SplitToken(envelope)
	assert.Equal(t, "the-key", bearer)
	assert.Equal(t, "https://notes.example.com", serverURL)
}

func TestSplitToken_BareKey(t *testing.T) {
	t.Parallel()

	bearer, serverURL := clientauth.SplitToken("legacy-plain-key")
	assert.Equal(t, "legacy-plain-key", bearer, "a bare key is used verbatim as the bearer")
	assert.Empty(t, serverURL, "a bare key carries no embedded URL")
}

// ClientSuite runs each test with an empty config dir, so no test reads or changes the real
// token file or known_hosts.
type ClientSuite struct {
	suite.Suite

	errOut    bytes.Buffer
	configDir string
}

func TestClientSuite(t *testing.T) {
	suite.Run(t, new(ClientSuite))
}

func (s *ClientSuite) SetupTest() {
	home := s.T().TempDir()
	s.configDir = filepath.Join(home, ".config")
	s.T().Setenv("HOME", home)
	s.T().Setenv("XDG_CONFIG_HOME", s.configDir)
	s.errOut.Reset()
}

// settings returns the settings of a run with token and, when serverURL is not empty, an
// explicit server flag.
func (s *ClientSuite) settings(token, serverURL string) *clientauth.Settings {
	settings := &clientauth.Settings{
		ErrOut:        &s.errOut,
		Token:         token,
		ServerURL:     testDefaultURL,
		Program:       testProgram,
		ServerFlag:    testServerFlag,
		HeaderTimeout: time.Minute,
		ServerURLSet:  false,
		InsecureHTTP:  clientauth.InsecureHTTPDenied,
	}

	if serverURL != "" {
		settings.ServerURL = serverURL
		settings.ServerURLSet = true
	}

	return settings
}

func (s *ClientSuite) resolvedURL(token, serverURL string) string {
	server, err := clientauth.ResolveServer(s.settings(token, serverURL))
	s.Require().NoError(err)

	return server.URL
}

func (s *ClientSuite) envelope(serverURL string) string {
	token, err := domain.EncodeAPITokenEnvelope(serverURL, "envelope-key")
	s.Require().NoError(err)

	return token
}

func (s *ClientSuite) TestResolveServer_Precedence() {
	envelope := s.envelope("https://pinned.example")

	s.Equal(testDefaultURL, s.resolvedURL("", ""), "default without token and flag")
	s.Equal("https://pinned.example", s.resolvedURL(envelope, ""), "the envelope URL replaces the default")
	s.Equal("https://explicit.example", s.resolvedURL(envelope, "https://explicit.example"),
		"an explicit flag wins over the envelope")
}

func (s *ClientSuite) TestResolveServer_ReadsTokenFile() {
	dir := filepath.Join(s.configDir, "padmark")
	s.Require().NoError(os.MkdirAll(dir, 0o700))
	s.Require().NoError(os.WriteFile(filepath.Join(dir, "token"), []byte(s.envelope("https://file.example")+"\n"), 0o600))

	server, err := clientauth.ResolveServer(s.settings("", ""))

	s.Require().NoError(err)
	s.Equal("https://file.example", server.URL)
	s.True(server.HasToken())
}

func (s *ClientSuite) TestTrustedServer_RefusesUnknownEnvelopeOrigin() {
	_, err := clientauth.TrustedServer(s.settings(s.envelope("https://untrusted.example"), ""))

	s.Require().ErrorIs(err, clientauth.ErrUntrustedServer)
	s.Contains(err.Error(), "validate trusted server: token pins server https://untrusted.example, "+
		"which is not trusted yet; run `padmark-test trust` to record it, or choose the server explicitly "+
		"with --base-url/PADMARK_URL")
}

func (s *ClientSuite) TestTrustedServer_ExplicitFlagNeedsNoRecord() {
	settings := s.settings(s.envelope("https://untrusted.example"), "https://explicit.example")

	server, err := clientauth.TrustedServer(settings)

	s.Require().NoError(err)
	s.Equal("https://explicit.example", server.URL)
	s.False(clientauth.IsKnownOrigin("https://untrusted.example"))
}

func (s *ClientSuite) TestTrustedServer_BareTokenNeedsNoRecord() {
	server, err := clientauth.TrustedServer(s.settings("legacy-bare-key", ""))

	s.Require().NoError(err)
	s.Equal(testDefaultURL, server.URL)
}

func (s *ClientSuite) TestTrust_RecordsEnvelopeOriginThenAllows() {
	envelope := s.envelope("https://pinned.example:443/base")

	origin, recordPath, err := clientauth.Trust(s.settings(envelope, ""))
	s.Require().NoError(err)
	s.Equal("https://pinned.example", origin)
	s.Equal(filepath.Join(s.configDir, "padmark", "known_hosts"), recordPath)

	server, err := clientauth.TrustedServer(s.settings(envelope, ""))
	s.Require().NoError(err)
	s.Equal("https://pinned.example:443/base", server.URL)
}

func (s *ClientSuite) TestTrust_ExplicitFlagWins() {
	origin, _, err := clientauth.Trust(s.settings(s.envelope("https://evil.example"), "https://good.example"))

	s.Require().NoError(err)
	s.Equal("https://good.example", origin)
	s.False(clientauth.IsKnownOrigin("https://evil.example"))
}

func (s *ClientSuite) TestTrust_NothingToTrust() {
	_, _, err := clientauth.Trust(s.settings("legacy-bare-key", ""))

	s.Require().ErrorIs(err, clientauth.ErrNoTrustTarget)
	s.Equal("nothing to trust: the token is not a pmk_ envelope and no --base-url was given", err.Error())
}

func (s *ClientSuite) TestNewClient_RefusesTokenOverRemoteHTTP() {
	_, _, err := clientauth.NewClient(s.settings("secret", "http://example.com"))

	s.Require().ErrorIs(err, clientauth.ErrInsecureTransport)
	s.Contains(err.Error(), "validate token transport: ")
}

func (s *ClientSuite) TestNewClient_NoTokenOverRemoteHTTP() {
	_, server, err := clientauth.NewClient(s.settings("", "http://example.com"))

	s.Require().NoError(err)
	s.False(server.HasToken())
	s.Empty(s.errOut.String(), "a run without a token sends no credential, so there is no warning")
}

func (s *ClientSuite) TestNewClient_SendsBearerToServer() {
	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")

		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	apiClient, server, err := clientauth.NewClient(s.settings("secret", srv.URL))
	s.Require().NoError(err)
	s.True(server.HasToken())
	s.Equal(srv.URL, server.URL)
	s.Contains(s.errOut.String(), "warning: bearer token will be sent in cleartext", "loopback HTTP warns")

	s.Require().NoError(apiClient.Healthz(s.T().Context()))
	s.Equal("Bearer secret", gotAuth)
}
