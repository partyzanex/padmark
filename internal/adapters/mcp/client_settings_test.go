package mcp

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/clientauth"
	"github.com/partyzanex/padmark/internal/domain"
)

// newTestCmd runs a full urcli parse of the given args, returning the resolved command so
// tests exercise the same flag/env precedence the binary sees.
func newTestCmd(t *testing.T, args ...string) *urcli.Command {
	t.Helper()

	var parsed *urcli.Command

	app := NewApp()
	app.Action = func(_ context.Context, cmd *urcli.Command) error {
		parsed = cmd

		return nil
	}

	require.NoError(t, app.Run(t.Context(), append([]string{"padmark-mcp"}, args...)))
	require.NotNil(t, parsed)

	return parsed
}

func resolvedServerURL(t *testing.T, args ...string) string {
	t.Helper()

	server, err := clientauth.ResolveServer(clientSettings(newTestCmd(t, args...)))
	require.NoError(t, err)

	return server.URL
}

func TestClientSettings_ExplicitFlagBeatsEnvelope(t *testing.T) {
	isolateEnv(t)

	envelope, err := domain.EncodeAPITokenEnvelope("http://embedded:4000", "k")
	require.NoError(t, err)

	assert.Equal(t, "http://explicit:9999",
		resolvedServerURL(t, "--base-url", "http://explicit:9999", "--token", envelope),
		"an explicit --base-url overrides the URL embedded in the envelope")
	assert.Equal(t, "http://embedded:4000", resolvedServerURL(t, "--token", envelope),
		"without --base-url the envelope selects the server")
}

func TestClientSettings_DefaultWithoutFlagAndEnvelope(t *testing.T) {
	isolateEnv(t)

	assert.Equal(t, DefaultBaseURL, resolvedServerURL(t))
}

func TestClientSettings_EnvBaseURL(t *testing.T) {
	isolateEnv(t)
	t.Setenv(EnvBaseURL, "http://from-env:7000")

	settings := clientSettings(newTestCmd(t))

	assert.Equal(t, "http://from-env:7000", settings.ServerURL, "PADMARK_URL is honoured")
	assert.True(t, settings.ServerURLSet, "PADMARK_URL is an explicit choice, as --base-url is")
	assert.Equal(t, "padmark-mcp", settings.Program)
	assert.Equal(t, FlagBaseURL, settings.ServerFlag)
}

func TestDefaultTimeout_IsBounded(t *testing.T) {
	assert.NotZero(t, DefaultTimeout, "a zero default would make tool calls hang on a dead server")
}

// TestNewServerFromFlags_RefusesInsecureRemote verifies the guard is wired into client creation
// and that the flag opts out.
func TestNewServerFromFlags_RefusesInsecureRemote(t *testing.T) {
	isolateEnv(t)

	log := slog.New(slog.DiscardHandler)

	_, err := newServerFromFlags(newTestCmd(t, "--base-url", "http://example.com", "--token", "secret"), log)
	require.ErrorIs(t, err, clientauth.ErrInsecureTransport)

	_, err = newServerFromFlags(newTestCmd(t,
		"--base-url", "http://example.com", "--token", "secret", "--allow-insecure-http"), log)
	require.NoError(t, err)
}

type ClientSettingsSuite struct {
	suite.Suite
}

func TestClientSettingsSuite(t *testing.T) {
	suite.Run(t, new(ClientSettingsSuite))
}

// TestNewServerFromFlags_TokenSelectsOwnerPrivacy checks that the token state goes into the
// server options: with a token, upload_file uses "owner" as its default privacy.
func (s *ClientSettingsSuite) TestNewServerFromFlags_TokenSelectsOwnerPrivacy() {
	isolateEnv(s.T())

	log := slog.New(slog.DiscardHandler)
	root := s.T().TempDir()

	owner, err := newServerFromFlags(newTestCmd(s.T(),
		"--base-url", "https://example.com", "--token", "secret", "--upload-root", root), log)
	s.Require().NoError(err)
	s.NotEmpty(owner.uploadRoot)
	s.True(owner.hasToken)

	anonymous, err := newServerFromFlags(newTestCmd(s.T(), "--base-url", "https://example.com"), log)
	s.Require().NoError(err)
	s.False(anonymous.hasToken, "no token gives no account option")
}

// TestWithCallTimeout_SetsLimit checks the default call limit and the option that changes it.
func TestWithCallTimeout_SetsLimit(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.DiscardHandler)

	assert.Equal(t, DefaultTimeout, NewServer(nil, log).callTimeout)
	assert.Equal(t, time.Second, NewServer(nil, log, WithCallTimeout(time.Second)).callTimeout)
	assert.Zero(t, NewServer(nil, log, WithCallTimeout(0)).callTimeout)
}
