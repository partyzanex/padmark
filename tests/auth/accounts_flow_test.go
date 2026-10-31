//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	clicmd "github.com/partyzanex/padmark/internal/adapters/cli"
	adaptershttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
	"github.com/partyzanex/padmark/internal/infra/lock"
	"github.com/partyzanex/padmark/internal/infra/render"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/testutil"
	"github.com/partyzanex/padmark/internal/usecases/auth"
	"github.com/partyzanex/padmark/internal/usecases/links"
	"github.com/partyzanex/padmark/internal/usecases/notes"
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
func setupPost(t *testing.T, baseURL string, body string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, baseURL+"/setup", strings.NewReader(body))
	require.NoError(t, err)

	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}

// TestAccountsFlow is the headless-accounts end-to-end: CLI setup → create-token →
// create-invite → member enrollment → the 403/409/410/429 edges.
// accountsFlowBootstrapToken is the first-admin bootstrap token TestAccountsFlow's server runs with.
const accountsFlowBootstrapToken = "accounts-flow-bootstrap-token-0123456789"

func TestAccountsFlow(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// The operator's bootstrap token: the server is configured with it, and padmark-cli setup
	// forwards it from the same environment variable.
	t.Setenv("PADMARK_BOOTSTRAP_TOKEN", accountsFlowBootstrapToken)

	ctx := context.Background()

	// ── storage + wiring (see testAPITokenFlow for the same pattern) ──
	db, err := sqliterepo.Open("file:accountsflow?mode=memory&cache=shared", slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = sqliterepo.Migrate(ctx, db)
	require.NoError(t, err)

	log := slog.New(slog.DiscardHandler)
	users := sqliterepo.NewUserRepository(db)

	authMgr, err := auth.NewManager(
		users,
		sqliterepo.NewInviteRepository(db, slog.Default()),
		sqliterepo.NewSessionRepository(db, slog.Default()),
		sqliterepo.NewAPITokenRepository(db),
		crypto.NewPasswordHasher(testArgon2Params),
		crypto.NewSealer(testArgon2Params),
		crypto.NewTOTP(),
		log,
		"padmark-test",
		time.Hour,
	)
	require.NoError(t, err)

	slugLock := lock.NewLocker()

	notesMgr := notes.NewManager(
		sqliterepo.NewNoteRepository(db),
		sqliterepo.NewLinkRepository(db, slog.Default()),
		slugLock,
		render.NewRenderer(),
		testutil.ContentEncryptor(t, "note"),
		crypto.NewEditCodeHasher(crypto.DefaultArgon2Params()),
		log,
		false,
	)
	linksMgr := links.NewManager(
		sqliterepo.NewLinkRepository(db, slog.Default()),
		sqliterepo.NewNoteRepository(db),
		slugLock,
		testutil.ContentEncryptor(t, "link"),
		crypto.NewEditCodeHasher(crypto.DefaultArgon2Params()),
		log,
		false,
	)

	handler := adaptershttp.NewHandler(notesMgr, linksMgr, log).WithAuthManager(authMgr)
	ogen := adaptershttp.NewOgenHandler(notesMgr, linksMgr, adaptershttp.NoPinger{}, log)
	router, err := adaptershttp.NewRouter(handler, ogen, &adaptershttp.RouterOptions{
		MaxBodyBytes:   256 * 1024,
		CSRFSecret:     []byte("padmark-test-csrf-secret-32bytes"),
		BootstrapToken: accountsFlowBootstrapToken,

		ForcedScheme: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0, RateBurst: 0,
		SessionTTL: 0, DisableAPI: false, DirectRedirect: false,
	})
	require.NoError(t, err)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	// ── headless bootstrap: CLI setup creates the first admin and prints the URI once ──
	out := captureStdout(t, func() error {
		return clicmd.NewApp().Run(ctx, []string{
			"padmark-cli", "--url", srv.URL,
			"setup", "--username", "admin", "--password", apiFlowPassword,
		})
	})

	totpURI := strings.TrimSpace(out)
	require.True(t, strings.HasPrefix(totpURI, "otpauth://totp/"),
		"setup must print the otpauth URI on stdout, got: %q", totpURI)

	// Setup is closed now: a second bootstrap attempt answers 403 with the invite hint.
	resp := setupPost(t, srv.URL, `{"username":"admin2","password":"`+apiFlowPassword+`"}`)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// ── admin gets a credential and self-serves a token over the generated route ──
	admin, err := users.GetByUsername(ctx, "admin")
	require.NoError(t, err)
	require.True(t, admin.IsAdmin)

	apiKey, err := authMgr.CreateAPIToken(ctx, admin.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 0)
	require.NoError(t, err)

	adminEnvelope, err := domain.EncodeAPITokenEnvelope(srv.URL, apiKey)
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

	out = captureStdout(t, func() error {
		return clicmd.NewApp().Run(ctx, []string{
			"padmark-cli", "--token", adminEnvelope, "create-token",
			"--password", apiFlowPassword, "--totp-code", totpCodeAt(0), "--ttl-days", "30",
		})
	})

	selfIssued := strings.TrimSpace(out)
	base, bearer, first := domain.DecodeAPITokenEnvelope(selfIssued)
	require.True(t, first, "create-token must print a pmk_ envelope, got: %q", selfIssued)
	require.Equal(t, srv.URL, base)

	// The issued envelope is immediately usable, but a key alone cannot multiply itself.
	status, body := hardeningDo(t, srv.URL, http.MethodPost, "/api/tokens", bearer, "", "")
	require.Equal(t, http.StatusForbidden, status, body)
	require.Contains(t, body, `"code":"step_up_required"`)

	// ── invites: admin issues (with step-up), member enrolls via CLI setup, replay is 410 ──
	status, body = hardeningDo(t, srv.URL, http.MethodPost, "/api/invites", bearer, "",
		`{"password":"`+apiFlowPassword+`","totp_code":"`+totpCodeAt(1)+`"}`)
	require.Equal(t, http.StatusCreated, status, body)

	var inviteOut struct {
		InviteURL string `json:"invite_url"`
	}

	err = json.Unmarshal([]byte(body), &inviteOut)
	require.NoError(t, err)

	inviteURL, parseErr := url.Parse(inviteOut.InviteURL)
	require.NoError(t, parseErr)

	inviteToken := inviteURL.Query().Get("invite")
	require.NotEmpty(t, inviteToken)

	// Member enrolls through the CLI (headless path) with a valid invite.
	out = captureStdout(t, func() error {
		return clicmd.NewApp().Run(ctx, []string{
			"padmark-cli", "--url", srv.URL,
			"setup", "--invite", inviteToken,
			"--username", "member", "--password", apiFlowPassword,
		})
	})
	require.True(t, strings.HasPrefix(strings.TrimSpace(out), "otpauth://totp/"))

	memberURI, err := url.Parse(strings.TrimSpace(out))
	require.NoError(t, err)

	// Replay of the spent invite is 410 — the token is single-use.
	resp = setupPost(t, srv.URL,
		`{"username":"other","password":"`+apiFlowPassword+`","invite":"`+inviteToken+`"}`)
	require.Equal(t, http.StatusGone, resp.StatusCode)
	_ = resp.Body.Close()

	// ── non-admin cannot mint invites ──
	member, err := users.GetByUsername(ctx, "member")
	require.NoError(t, err)
	require.False(t, member.IsAdmin)

	memberKey, err := authMgr.CreateAPIToken(ctx, member.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 0)
	require.NoError(t, err)

	resp = bearerPost(t, srv.URL, "/api/invites", memberKey)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	_ = resp.Body.Close()

	// Member's self-service token issuance works — any authenticated caller, self only — once
	// the bearer re-authenticates with the member's own password and TOTP code.
	memberCode, err := totp.GenerateCode(memberURI.Query().Get("secret"), time.Now())
	require.NoError(t, err)

	status, body = hardeningDo(t, srv.URL, http.MethodPost, "/api/tokens", memberKey, "",
		`{"password":"`+apiFlowPassword+`","totp_code":"`+memberCode+`"}`)
	require.Equal(t, http.StatusCreated, status, body)

	// ── the origin gate: cross-site mints are refused, same-origin passes (P1/P2 fixes) ──
	// A page on a sibling subdomain rides the session cookie (SameSite alone admits it);
	// the Origin header it must declare cannot match, so the mint is refused before it runs.
	resp = bearerPostOrigin(t, srv.URL, "/api/tokens", memberKey, "https://evil.sibling.example")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	_ = resp.Body.Close()

	resp = bearerPostOrigin(t, srv.URL, "/api/invites", memberKey, "https://evil.sibling.example")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	_ = resp.Body.Close()

	// The same-origin call passes the origin gate (the browser sends its own origin) and reaches
	// the issuing rule, which wants the bearer's step-up — not the cross-origin refusal.
	resp = bearerPostOrigin(t, srv.URL, "/api/tokens", memberKey, srv.URL)

	sameOrigin, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	_ = resp.Body.Close()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Contains(t, string(sameOrigin), `"code":"step_up_required"`)

	// JSON setup: a CORS-safelisted text/plain Content-Type (a preflight-free cross-site fetch,
	// no Fetch Metadata required) is refused — the headless branch exists only for JSON bodies.
	plainReq, plainNewErr := http.NewRequest(http.MethodPost, srv.URL+"/setup",
		strings.NewReader(`{"username":"admin3","password":"`+apiFlowPassword+`"}`))
	require.NoError(t, plainNewErr)
	plainReq.Header.Set("Accept", "application/json")
	plainReq.Header.Set("Content-Type", "text/plain")

	plainResp, plainErr := http.DefaultClient.Do(plainReq)
	require.NoError(t, plainErr)
	require.Equal(t, http.StatusUnsupportedMediaType, plainResp.StatusCode,
		"a text/plain JSON smuggle must not reach the setup usecase")
	_ = plainResp.Body.Close()

	// ── the per-user cap: 20 live tokens max, the 21st is refused ──
	// admin holds 2 issued above (manager, create-token); fill to 20. Over HTTP the refusal is
	// 429 (unit-tested); here it is checked at the usecase, since every further bearer issue
	// would need another TOTP window.
	for range 18 {
		_, err = authMgr.CreateAPIToken(ctx, admin.ID, auth.Issuer{
			APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
		}, 0)
		require.NoError(t, err)
	}

	_, err = authMgr.CreateAPIToken(ctx, admin.ID, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 0)
	require.ErrorIs(t, err, domain.ErrAPITokenLimit)
}
