//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	clicmd "github.com/partyzanex/padmark/internal/adapters/cli"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/tests/internal/testenv"
)

const apiFlowPassword = "FlowP@ss123!"

// APITokenFlowSuite is the end-to-end admin→CLI→note flow:
// an admin issues an API key, and the CLI — authenticating with nothing but that Bearer key —
// creates a note through the real HTTP stack (auth middleware → ResolveAPIToken → ogen handler →
// storage). It also guards the composition-root wiring: if the api-token store is not injected
// into auth.Manager, CreateAPIToken returns ErrFeatureNotSupported and the suite fails.
type APITokenFlowSuite struct {
	suite.Suite

	stack *testenv.Stack
}

func TestAPITokenFlowSuite(t *testing.T) {
	suite.Run(t, new(APITokenFlowSuite))
}

// SetupTest isolates the CLI configuration and builds the stack: a fresh shared-cache in-memory
// DB (pragmas, incl. FK enforcement, via Open) and the stack of testenv, which mirrors
// internal/infra/server assembly.
func (s *APITokenFlowSuite) SetupTest() {
	s.T().Setenv("XDG_CONFIG_HOME", s.T().TempDir())
	s.stack = testenv.NewStack(s.T(), "apitokenflow", testenv.WithAccounts())
}

func (s *APITokenFlowSuite) TestAdminIssuesKey_CLICreatesNote() {
	envelope := s.issueEnvelope()
	s.checkCLICreatesNote(envelope)
	s.checkInvalidKeyRefused()
	s.checkPrivateRead(envelope)
}

// issueEnvelope makes the first admin issue an API key and returns the envelope token of it.
func (s *APITokenFlowSuite) issueEnvelope() string {
	t := s.T()
	ctx := t.Context()

	// ── admin issues an API key ──.
	_, err := s.stack.Auth.AcceptFirstAdmin(ctx, "admin", apiFlowPassword)
	require.NoError(t, err)

	admin, err := s.stack.Users.GetByUsername(ctx, "admin")
	require.NoError(t, err)
	require.True(t, admin.IsAdmin)

	apiKey, err := s.stack.Auth.CreateAPIToken(ctx, admin.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 0)
	require.NoError(t, err, "admin must be able to issue an API key — the store must be wired into auth.Manager")
	require.NotEmpty(t, apiKey)

	// The admin panel hands the user an envelope token: the raw key packed together with the
	// server URL. Exercising that exact string proves the whole feature end-to-end.
	envelope, err := domain.EncodeAPITokenEnvelope(s.stack.Server.URL, apiKey)
	require.NoError(t, err)

	require.NoError(t, clicmd.NewApp().Run(ctx, []string{"padmark-cli", "--token", envelope, "trust"}))

	return envelope
}

// checkCLICreatesNote makes the CLI create a note with only the envelope token.
func (s *APITokenFlowSuite) checkCLICreatesNote(envelope string) {
	t := s.T()
	ctx := t.Context()

	// ── CLI creates a note with only the envelope token — no --url, the URL comes from the token ──.
	runErr := clicmd.NewApp().Run(ctx, []string{
		"padmark-cli", "--token", envelope,
		"create", "--title", "hello", "--content", "world from cli",
	})
	require.NoError(t, runErr, "CLI must create a note using the URL and key packed into the envelope token")

	count, err := s.stack.DB.NewSelect().TableExpr("notes").Count(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), count, "exactly one note created through the API-token-authenticated path")
}

// checkInvalidKeyRefused checks that an invalid API key cannot change a privacy-gated note.
func (s *APITokenFlowSuite) checkInvalidKeyRefused() {
	t := s.T()
	ctx := t.Context()

	// ── negative: a bogus key is rejected when the operation requires a credential ──
	// POST /notes is anonymous-allowed by design (an invalid key does not block creating a
	// public note), so the negative case targets a mutation on a privacy=authenticated note —
	// the mutation privacy gate answers 401 before the note is touched.
	gatedPrv := domain.PrivacyAuthenticated
	gated, err := s.stack.Notes.Create(ctx, &domain.Note{
		Title:   "gated",
		Content: "requires login",
		Privacy: &gatedPrv,

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		OwnerID: nil, ID: "", ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false, ShowContents: nil,
	})
	require.NoError(t, err)

	badErr := clicmd.NewApp().Run(ctx, []string{
		"padmark-cli", "--url", s.stack.Server.URL, "--token", "not-a-real-key",
		"delete", gated.ID,
	})
	require.Error(t, badErr, "CLI must fail when the API key is invalid")

	count, err := s.stack.DB.NewSelect().TableExpr("notes").Count(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), count, "an invalid API key must not mutate the privacy-gated note")
}

// checkPrivateRead checks the reads of the CLI with the envelope token: a private note and a
// missing note.
func (s *APITokenFlowSuite) checkPrivateRead(envelope string) {
	t := s.T()
	ctx := t.Context()

	// ── regression: a PRIVATE (authenticated-level) note must be readable via the CLI's API
	// token ── GET /notes/{id} is a "public" route (auth not required), so the auth middleware
	// must still resolve the API-token Bearer key there — otherwise a private-note read returns 401.
	priv := domain.PrivacyAuthenticated
	created, err := s.stack.Notes.Create(ctx, &domain.Note{
		Title:   "secret",
		Content: "private body",
		Privacy: &priv,

		CreatedAt: time.Time{}, UpdatedAt: time.Time{}, ExpiresAt: nil,
		OwnerID: nil, ID: "", ContentType: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false, ShowContents: nil,
	})
	require.NoError(t, err)

	getErr := clicmd.NewApp().Run(ctx, []string{
		"padmark-cli", "--token", envelope, "get", created.ID,
	})
	require.NoError(t, getErr,
		"CLI must read a PRIVATE note with its API token (regression: 401 on the public GET route)")

	// ── regression: a missing note must surface as a clean typed "not found" error, not a
	// content-type decode failure — the API error body must be JSON per the OpenAPI spec ──.
	missingErr := clicmd.NewApp().Run(ctx, []string{
		"padmark-cli", "--token", envelope, "get", "no-such-note",
	})
	require.Error(t, missingErr)
	require.Contains(t, missingErr.Error(), "not found",
		"the ogen client must decode the JSON 404 into a typed not-found error")
	require.NotContains(t, missingErr.Error(), "Content-Type",
		"a missing note must not surface as an undecodable-response error")
}
