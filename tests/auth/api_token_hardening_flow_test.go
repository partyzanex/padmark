//go:build integration

package integration

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/crypto"
)

// Finding M-6: a bearer API token can mint more API tokens (and invites, and use the HTML admin
// panel); tokens never expire and survive a password change. With one leaked admin pmk_… key an
// attacker mints up to 20 siblings that outlive every remedy the victim has. Issuance by a bearer
// must re-authenticate (password + TOTP), the admin panel needs a browser session, tokens carry
// a caller-chosen lifetime, revoking a token revokes what it issued, and changing the password
// revokes all of the user's tokens. These tests describe the DESIRED behaviour.

// hardeningOwner is the signed-in admin every M-6 scenario starts from.
type hardeningOwner struct {
	totpSecret string
	session    string
	bearer     string
	userID     uuid.UUID
}

// totpCodeAt returns the owner's TOTP code for the window offset steps from now; each step-up
// needs a later window than the previous one because accepted counters must strictly increase.
func (o *hardeningOwner) totpCodeAt(t *testing.T, step int) string {
	t.Helper()

	code, err := totp.GenerateCode(o.totpSecret, time.Now().Add(time.Duration(step)*30*time.Second))
	require.NoError(t, err)

	return code
}

// newHardeningOwner bootstraps the first admin, signs in (TOTP window 0) and issues one API
// token from the browser session over HTTP.
func newHardeningOwner(t *testing.T, env ownerFlowEnv) *hardeningOwner {
	t.Helper()

	ctx := t.Context()

	_, err := env.authMgr.AcceptFirstAdmin(ctx, "owner", ownerFlowPassword)
	require.NoError(t, err)

	usr, err := env.users.GetByUsername(ctx, "owner")
	require.NoError(t, err)

	secret, _, err := crypto.NewSealer(testArgon2Params).Open(usr.TOTPSecret, []byte(ownerFlowPassword), usr.KDFSalt)
	require.NoError(t, err)

	owner := &hardeningOwner{totpSecret: secret, userID: usr.ID}

	owner.session, err = env.authMgr.Login(ctx, "owner", ownerFlowPassword, owner.totpCodeAt(t, 0),
		"Mozilla/5.0", "127.0.0.1")
	require.NoError(t, err)

	status, body := hardeningDo(t, env.baseURL, http.MethodPost, "/api/tokens", "", owner.session, `{}`)
	require.Equal(t, http.StatusCreated, status, body)

	owner.bearer = hardeningRawKey(t, body)

	return owner
}

// hardeningDo sends a JSON request with an optional bearer key or session cookie.
func hardeningDo(t *testing.T, baseURL, method, path, bearer, session, body string) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, baseURL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Accept", "application/json")

	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	if session != "" {
		req.Header.Set("Cookie", "padmark_session="+session)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, err := client.Do(req)
	require.NoError(t, err)

	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(raw)
}

// hardeningRawKey extracts the raw bearer key from a POST /api/tokens response.
func hardeningRawKey(t *testing.T, body string) string {
	t.Helper()

	var out struct {
		Token string `json:"token"`
	}

	require.NoError(t, json.Unmarshal([]byte(body), &out))

	_, raw, ok := domain.DecodeAPITokenEnvelope(out.Token)
	require.True(t, ok, "token response must carry a pmk_ envelope")

	return raw
}

// hardeningTokenID is a key's public identifier: the base64url SHA-256 the admin panel uses.
func hardeningTokenID(raw string) string {
	sum := sha256.Sum256([]byte(raw))

	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// hardeningTokenCount counts the live API tokens of the owner.
func hardeningTokenCount(t *testing.T, env ownerFlowEnv, owner *hardeningOwner) int {
	t.Helper()

	tokens, err := env.authMgr.ListAPITokens(t.Context(), owner.userID)
	require.NoError(t, err)

	return len(tokens)
}

// hardeningKeyWorks reports whether raw still authenticates: an owner-only note it created is
// readable with it.
func hardeningKeyWorks(t *testing.T, env ownerFlowEnv, raw, noteID string) bool {
	t.Helper()

	status, _ := hardeningDo(t, env.baseURL, http.MethodGet, "/notes/"+noteID, raw, "", "")

	return status == http.StatusOK
}

// hardeningOwnerNote creates an owner-only note with the bearer key and returns its id.
func hardeningOwnerNote(t *testing.T, env ownerFlowEnv, raw string) string {
	t.Helper()

	status, body := hardeningDo(t, env.baseURL, http.MethodPost, "/notes", raw, "",
		`{"content":"m6","privacy":"owner"}`)
	require.Equal(t, http.StatusCreated, status, body)

	var created struct {
		ID string `json:"id"`
	}

	require.NoError(t, json.Unmarshal([]byte(body), &created))

	return created.ID
}

// TestAPITokenHardening_BearerIssuanceRequiresStepUp: a leaked key alone mints neither API tokens
// nor invites — the bearer must re-authenticate with the account password and a fresh TOTP code.
func TestAPITokenHardening_BearerIssuanceRequiresStepUp(t *testing.T) {
	env := newOwnerFlowEnv(t, "m6-step-up")
	owner := newHardeningOwner(t, env)

	for _, body := range []string{
		``,
		`{}`,
		`{"password":"wrong-password","totp_code":"` + owner.totpCodeAt(t, 1) + `"}`,
		`{"password":"` + ownerFlowPassword + `","totp_code":"000000"}`,
	} {
		status, resp := hardeningDo(t, env.baseURL, http.MethodPost, "/api/tokens", owner.bearer, "", body)
		assert.Equal(t, http.StatusForbidden, status, "bearer token issuance without step-up (%q): %s", body, resp)

		status, resp = hardeningDo(t, env.baseURL, http.MethodPost, "/api/invites", owner.bearer, "", body)
		assert.Equal(t, http.StatusForbidden, status, "bearer invite issuance without step-up (%q): %s", body, resp)
	}

	assert.Equal(t, 1, hardeningTokenCount(t, env, owner), "no token may have been minted")

	status, resp := hardeningDo(t, env.baseURL, http.MethodPost, "/api/tokens", owner.bearer, "",
		`{"password":"`+ownerFlowPassword+`","totp_code":"`+owner.totpCodeAt(t, 1)+`"}`)
	require.Equal(t, http.StatusCreated, status, "a bearer with a valid step-up may mint: %s", resp)
	assert.Equal(t, 2, hardeningTokenCount(t, env, owner))
}

// TestAPITokenHardening_AdminPanelNeedsSession: the HTML admin panel (and its key-minting form)
// is a browser UI — a bearer key must not reach it.
func TestAPITokenHardening_AdminPanelNeedsSession(t *testing.T) {
	env := newOwnerFlowEnv(t, "m6-admin-panel")
	owner := newHardeningOwner(t, env)

	status, _ := hardeningDo(t, env.baseURL, http.MethodGet, "/admin", owner.bearer, "", "")
	assert.NotEqual(t, http.StatusOK, status, "GET /admin must not serve a bearer caller")

	// A CSRF pair is available to anyone from a public page; the bearer then posts the form.
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	client := &http.Client{Jar: jar}

	page, err := client.Get(env.baseURL + "/login")
	require.NoError(t, err)

	raw, err := io.ReadAll(page.Body)
	require.NoError(t, err)
	require.NoError(t, page.Body.Close())

	match := setupCSRFPattern.FindSubmatch(raw)
	require.NotNil(t, match)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, env.baseURL+"/admin/api-keys",
		strings.NewReader("csrf_token="+string(match[1])+"&user_id="+owner.userID.String()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+owner.bearer)
	req.Header.Set("Origin", env.baseURL)

	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, 1, hardeningTokenCount(t, env, owner), "the admin form must not mint a key for a bearer")
}

// TestAPITokenHardening_PasswordChangeRevokesTokens: "change the password" is the remedy a victim
// reaches for — it must also kill every API token of the account.
func TestAPITokenHardening_PasswordChangeRevokesTokens(t *testing.T) {
	env := newOwnerFlowEnv(t, "m6-password-change")
	owner := newHardeningOwner(t, env)
	noteID := hardeningOwnerNote(t, env, owner.bearer)

	require.True(t, hardeningKeyWorks(t, env, owner.bearer, noteID))

	_, err := env.authMgr.ChangePassword(t.Context(), owner.session, ownerFlowPassword,
		"N3w!OwnerPassw0rd", owner.totpCodeAt(t, 1))
	require.NoError(t, err)

	assert.False(t, hardeningKeyWorks(t, env, owner.bearer, noteID), "the API token must not survive a password change")
	assert.Zero(t, hardeningTokenCount(t, env, owner))
}

// TestAPITokenHardening_LifetimeAndCascade: the issuer chooses a lifetime in days (0 = never), and
// revoking a token also revokes every token issued with it.
func TestAPITokenHardening_LifetimeAndCascade(t *testing.T) {
	env := newOwnerFlowEnv(t, "m6-ttl-cascade")
	owner := newHardeningOwner(t, env)
	noteID := hardeningOwnerNote(t, env, owner.bearer)

	status, body := hardeningDo(t, env.baseURL, http.MethodPost, "/api/tokens", "", owner.session, `{"ttl_days":7}`)
	require.Equal(t, http.StatusCreated, status, body)

	weekly := hardeningTokenID(hardeningRawKey(t, body))

	status, body = hardeningDo(t, env.baseURL, http.MethodPost, "/api/tokens", owner.bearer, "",
		`{"password":"`+ownerFlowPassword+`","totp_code":"`+owner.totpCodeAt(t, 1)+`"}`)
	require.Equal(t, http.StatusCreated, status, body)

	child := hardeningRawKey(t, body)

	tokens, err := env.authMgr.ListAPITokens(t.Context(), owner.userID)
	require.NoError(t, err)

	for _, token := range tokens {
		switch token.ID {
		case weekly:
			if assert.NotNil(t, token.ExpiresAt, "ttl_days:7 must set an expiry") {
				assert.WithinDuration(t, time.Now().Add(7*24*time.Hour), *token.ExpiresAt, time.Minute)
			}
		case hardeningTokenID(child), hardeningTokenID(owner.bearer):
			assert.Nil(t, token.ExpiresAt, "omitted ttl_days means no expiry")
		}
	}

	require.True(t, hardeningKeyWorks(t, env, child, noteID))
	require.NoError(t, env.authMgr.RevokeAPIToken(t.Context(), owner.userID, hardeningTokenID(owner.bearer)))

	assert.False(t, hardeningKeyWorks(t, env, child, noteID), "revoking a token must revoke the tokens it issued")
}
