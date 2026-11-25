//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	clicmd "github.com/partyzanex/padmark/internal/adapters/cli"
	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/tests/internal/testenv"
)

// captureStdout redirects os.Stdout for the duration of fn and returns what was printed —
// for asserting the one-time enrollment/envelope output of the CLI's account commands.
func captureStdout(t *testing.T, run func() error) string {
	t.Helper()

	original := os.Stdout

	r, w, err := os.Pipe()
	require.NoError(t, err)

	os.Stdout = w

	defer func() { os.Stdout = original }()

	runErr := run()

	closeErr := w.Close()
	raw, readErr := io.ReadAll(r)
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	require.NoError(t, runErr)

	return string(raw)
}

// bearerPost posts an empty-body JSON request with a Bearer credential to the test server.
func bearerPost(t *testing.T, baseURL, path, bearer string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, baseURL+path, nil)
	require.NoError(t, err)

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}

// bearerPostOrigin is bearerPost plus an explicit Origin header — for the origin-gate checks;
// an empty origin omits the header the way real machine clients do.
func bearerPostOrigin(t *testing.T, baseURL, path, bearer, origin string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, baseURL+path, nil)
	require.NoError(t, err)

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)

	if origin != "" {
		req.Header.Set("Origin", origin)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}

// setupPost posts the JSON setup body (the headless bootstrap surface).
func setupPost(t *testing.T, baseURL, body string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, baseURL+"/setup", strings.NewReader(body))
	require.NoError(t, err)

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}

// accountsFlowBootstrapToken is the first-admin bootstrap token of the server of
// AccountsFlowSuite.
const accountsFlowBootstrapToken = "accounts-flow-bootstrap-token-0123456789"

// AccountsFlowSuite is the headless-accounts end-to-end: CLI setup → create-token →
// create-invite → member enrollment → the 403/409/410/429 edges. Each phase is one method; the
// phases run in sequence in TestAccountsFlow, because each phase uses the result of the one
// before it.
type AccountsFlowSuite struct {
	suite.Suite

	authMgr *auth.Manager
	users   *sqliterepo.UserRepository
	srvURL  string
}

func TestAccountsFlowSuite(t *testing.T) {
	suite.Run(t, new(AccountsFlowSuite))
}

func (s *AccountsFlowSuite) SetupTest() {
	t := s.T()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// The operator's bootstrap token: the server is configured with it, and padmark-cli setup
	// forwards it from the same environment variable.
	t.Setenv("PADMARK_BOOTSTRAP_TOKEN", accountsFlowBootstrapToken)

	// ── storage + wiring: the stack of testenv, as the server assembles it ──.
	stack := testenv.NewStack(t, "accountsflow", testenv.WithAccounts(),
		testenv.WithConfig(func(cfg *web.HandlerConfig, _ *testenv.Stack) {
			cfg.BootstrapToken = accountsFlowBootstrapToken
		}))
	s.authMgr, s.users, s.srvURL = stack.Auth, stack.Users, stack.Server.URL
}

func (s *AccountsFlowSuite) TestAccountsFlow() {
	totpURI := s.bootstrapFirstAdmin()
	admin, bearer, totpCodeAt := s.selfIssueToken(totpURI)
	inviteToken := s.issueInvite(bearer, totpCodeAt)
	memberURI := s.enrollMember(inviteToken)
	memberKey := s.checkMemberRights(memberURI)
	s.checkOriginGate(memberKey)
	s.checkTokenCap(admin)
}

// bootstrapFirstAdmin runs the headless bootstrap: CLI setup creates the first admin and prints
// the URI once. It returns that otpauth URI.
func (s *AccountsFlowSuite) bootstrapFirstAdmin() string {
	t := s.T()
	ctx := t.Context()

	out := captureStdout(t, func() error {
		return clicmd.NewApp().Run(ctx, []string{
			"padmark-cli", "--url", s.srvURL,
			"setup", "--username", "admin", "--password", apiFlowPassword,
		})
	})

	totpURI := strings.TrimSpace(out)
	require.True(t, strings.HasPrefix(totpURI, "otpauth://totp/"),
		"setup must print the otpauth URI on stdout, got: %q", totpURI)

	// Setup is closed now: a second bootstrap attempt answers 403 with the invite hint.
	resp := setupPost(t, s.srvURL, `{"username":"admin2","password":"`+apiFlowPassword+`"}`)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	return totpURI
}

// selfIssueToken makes the admin get a credential and self-serve a token over the generated
// route. It returns the admin, the bearer key of the self-issued envelope and the TOTP code of
// the admin for a 30-second window.
func (s *AccountsFlowSuite) selfIssueToken(totpURI string) (*domain.User, string, func(step int) string) {
	t := s.T()
	ctx := t.Context()

	admin, err := s.users.GetByUsername(ctx, "admin")
	require.NoError(t, err)
	require.True(t, admin.IsAdmin)

	apiKey, err := s.authMgr.CreateAPIToken(ctx, admin.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 0)
	require.NoError(t, err)

	adminEnvelope, err := domain.EncodeAPITokenEnvelope(s.srvURL, apiKey)
	require.NoError(t, err)

	require.NoError(t, clicmd.NewApp().Run(ctx, []string{"padmark-cli", "--token", adminEnvelope, "trust"}))

	// Issuing with a bearer key needs a step-up: the password (here a flag) and a current TOTP
	// code from the secret the setup printed once. Each code is accepted once, so every step-up
	// below uses the next 30-second window.
	enrollURI, err := url.Parse(totpURI)
	require.NoError(t, err)

	totpCodeAt := func(step int) string {
		code, codeErr := totp.GenerateCode(enrollURI.Query().Get("secret"),
			time.Now().Add(time.Duration(step)*30*time.Second))
		require.NoError(t, codeErr)

		return code
	}

	out := captureStdout(t, func() error {
		return clicmd.NewApp().Run(ctx, []string{
			"padmark-cli", "--token", adminEnvelope, "create-token",
			"--password", apiFlowPassword, "--totp-code", totpCodeAt(0), "--ttl-days", "30",
		})
	})

	selfIssued := strings.TrimSpace(out)
	base, bearer, first := domain.DecodeAPITokenEnvelope(selfIssued)
	require.True(t, first, "create-token must print a pmk_ envelope, got: %q", selfIssued)
	require.Equal(t, s.srvURL, base)

	// The issued envelope is immediately usable, but a key alone cannot multiply itself.
	status, body := hardeningDo(t, s.srvURL, http.MethodPost, "/api/tokens", bearer, "", "")
	require.Equal(t, http.StatusForbidden, status, body)
	require.Contains(t, body, `"code":"step_up_required"`)

	return admin, bearer, totpCodeAt
}

// issueInvite makes the admin issue an invite with a step-up and returns the invite token.
func (s *AccountsFlowSuite) issueInvite(bearer string, totpCodeAt func(step int) string) string {
	t := s.T()

	// ── invites: admin issues (with step-up), member enrolls via CLI setup, replay is 410 ──.
	status, body := hardeningDo(t, s.srvURL, http.MethodPost, "/api/invites", bearer, "",
		`{"password":"`+apiFlowPassword+`","totp_code":"`+totpCodeAt(1)+`"}`)
	require.Equal(t, http.StatusCreated, status, body)

	var inviteOut struct {
		InviteURL string `json:"invite_url"`
	}

	err := json.Unmarshal([]byte(body), &inviteOut)
	require.NoError(t, err)

	inviteURL, parseErr := url.Parse(inviteOut.InviteURL)
	require.NoError(t, parseErr)

	inviteToken := inviteURL.Query().Get("invite")
	require.NotEmpty(t, inviteToken)

	return inviteToken
}

// enrollMember enrolls the member through the CLI (headless path) with a valid invite and
// returns the otpauth URI of the member. A replay of the spent invite is 410.
func (s *AccountsFlowSuite) enrollMember(inviteToken string) *url.URL {
	t := s.T()
	ctx := t.Context()

	out := captureStdout(t, func() error {
		return clicmd.NewApp().Run(ctx, []string{
			"padmark-cli", "--url", s.srvURL,
			"setup", "--invite", inviteToken,
			"--username", "member", "--password", apiFlowPassword,
		})
	})
	require.True(t, strings.HasPrefix(strings.TrimSpace(out), "otpauth://totp/"))

	memberURI, err := url.Parse(strings.TrimSpace(out))
	require.NoError(t, err)

	// Replay of the spent invite is 410 — the token is single-use.
	resp := setupPost(t, s.srvURL,
		`{"username":"other","password":"`+apiFlowPassword+`","invite":"`+inviteToken+`"}`)
	require.Equal(t, http.StatusGone, resp.StatusCode)
	_ = resp.Body.Close()

	return memberURI
}

// checkMemberRights checks that a non-admin cannot mint invites and can issue a token for
// itself with a step-up. It returns the API key of the member.
func (s *AccountsFlowSuite) checkMemberRights(memberURI *url.URL) string {
	t := s.T()
	ctx := t.Context()

	// ── non-admin cannot mint invites ──.
	member, err := s.users.GetByUsername(ctx, "member")
	require.NoError(t, err)
	require.False(t, member.IsAdmin)

	memberKey, err := s.authMgr.CreateAPIToken(ctx, member.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 0)
	require.NoError(t, err)

	resp := bearerPost(t, s.srvURL, "/api/invites", memberKey)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	_ = resp.Body.Close()

	// Member's self-service token issuance works — any authenticated caller, self only — once
	// the bearer re-authenticates with the member's own password and TOTP code.
	memberCode, err := totp.GenerateCode(memberURI.Query().Get("secret"), time.Now())
	require.NoError(t, err)

	status, body := hardeningDo(t, s.srvURL, http.MethodPost, "/api/tokens", memberKey, "",
		`{"password":"`+apiFlowPassword+`","totp_code":"`+memberCode+`"}`)
	require.Equal(t, http.StatusCreated, status, body)

	return memberKey
}

// checkOriginGate checks that cross-site mints are refused, same-origin passes (P1/P2 fixes),
// and that a text/plain JSON setup is refused.
func (s *AccountsFlowSuite) checkOriginGate(memberKey string) {
	t := s.T()

	// A page on a sibling subdomain rides the session cookie (SameSite alone admits it);
	// the Origin header it must declare cannot match, so the mint is refused before it runs.
	resp := bearerPostOrigin(t, s.srvURL, "/api/tokens", memberKey, "https://evil.sibling.example")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	_ = resp.Body.Close()

	resp = bearerPostOrigin(t, s.srvURL, "/api/invites", memberKey, "https://evil.sibling.example")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	_ = resp.Body.Close()

	// The same-origin call passes the origin gate (the browser sends its own origin) and reaches
	// the issuing rule, which wants the bearer's step-up — not the cross-origin refusal.
	resp = bearerPostOrigin(t, s.srvURL, "/api/tokens", memberKey, s.srvURL)

	sameOrigin, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	_ = resp.Body.Close()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Contains(t, string(sameOrigin), `"code":"step_up_required"`)

	// JSON setup: a CORS-safelisted text/plain Content-Type (a preflight-free cross-site fetch,
	// no Fetch Metadata required) is refused — the headless branch exists only for JSON bodies.
	plainReq, plainNewErr := http.NewRequestWithContext(t.Context(), http.MethodPost, s.srvURL+"/setup",
		strings.NewReader(`{"username":"admin3","password":"`+apiFlowPassword+`"}`))
	require.NoError(t, plainNewErr)
	plainReq.Header.Set("Accept", "application/json")
	plainReq.Header.Set("Content-Type", "text/plain")

	plainResp, plainErr := http.DefaultClient.Do(plainReq)
	require.NoError(t, plainErr)
	require.Equal(t, http.StatusUnsupportedMediaType, plainResp.StatusCode,
		"a text/plain JSON smuggle must not reach the setup usecase")
	_ = plainResp.Body.Close()
}

// checkTokenCap checks the per-user cap: 20 live tokens max, the 21st is refused. admin holds 2
// issued above (manager, create-token); fill to 20. Over HTTP the refusal is 429 (unit-tested);
// here it is checked at the usecase, since every further bearer issue would need another TOTP
// window.
func (s *AccountsFlowSuite) checkTokenCap(admin *domain.User) {
	t := s.T()
	ctx := t.Context()

	for range 18 {
		_, err := s.authMgr.CreateAPIToken(ctx, admin.ID, auth.Issuer{
			APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
		}, 0)
		require.NoError(t, err)
	}

	_, err := s.authMgr.CreateAPIToken(ctx, admin.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 0)
	require.ErrorIs(t, err, domain.ErrAPITokenLimit)
}
