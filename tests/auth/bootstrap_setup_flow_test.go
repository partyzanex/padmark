//go:build integration

package integration

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptershttp "github.com/partyzanex/padmark/internal/adapters/http"
)

// Finding M-2: while no account exists, /setup without an invite creates the single, permanent
// admin for whoever reaches it first — /setup is a public route, the server listens on all
// interfaces by default, and the partial unique index on users.is_admin plus admin-less invites
// make the outcome irreversible without editing the database. A first-admin bootstrap must
// demand proof of server ownership (PADMARK_BOOTSTRAP_TOKEN); with none configured it stays
// closed. These tests describe the DESIRED behaviour and fail until M-2 is fixed.

var setupCSRFPattern = regexp.MustCompile(`name="csrf_token" value="([^"]+)"`)

// TestBootstrapSetup_AnonymousCannotClaimFirstAdmin is an internet scanner racing the operator to
// a freshly started --enable-accounts instance on which no bootstrap token was configured.
func TestBootstrapSetup_AnonymousCannotClaimFirstAdmin(t *testing.T) {
	const scannerPassword = "Sc4nner!Passw0rd"

	t.Run("headless JSON setup", func(t *testing.T) {
		env := newOwnerFlowEnv(t, "bootstrap-setup-json")

		for _, body := range []string{
			`{"username":"scanner","password":"` + scannerPassword + `"}`,
			`{"username":"scanner","password":"` + scannerPassword + `","bootstrap_token":"guess"}`,
		} {
			resp := setupPost(t, env.baseURL, body)
			raw, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())

			assert.Equal(t, http.StatusForbidden, resp.StatusCode,
				"an unproven first-admin setup must be refused: %s", raw)
		}

		empty, err := env.authMgr.IsEmpty(t.Context())
		require.NoError(t, err)
		assert.True(t, empty, "the scanner must not have become the first admin")
	})

	t.Run("browser form setup", func(t *testing.T) {
		env := newOwnerFlowEnv(t, "bootstrap-setup-form")

		jar, err := cookiejar.New(nil)
		require.NoError(t, err)

		client := &http.Client{Jar: jar,
			Transport: nil, CheckRedirect: nil, Timeout: 0,
		}

		// A real browser flow: GET the form (CSRF cookie + token), then submit it.
		page, err := client.Get(env.baseURL + "/setup")
		require.NoError(t, err)

		raw, err := io.ReadAll(page.Body)
		require.NoError(t, err)
		require.NoError(t, page.Body.Close())

		if match := setupCSRFPattern.FindSubmatch(raw); match != nil {
			form := url.Values{
				"csrf_token":       {string(match[1])},
				"username":         {"scanner"},
				"password":         {scannerPassword},
				"password_confirm": {scannerPassword},
				"bootstrap_token":  {"guess"},
			}

			req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodPost, env.baseURL+"/setup",
				strings.NewReader(form.Encode()))
			require.NoError(t, reqErr)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "text/html")
			req.Header.Set("Origin", env.baseURL)

			resp, doErr := client.Do(req)
			require.NoError(t, doErr)
			require.NoError(t, resp.Body.Close())
		}

		empty, err := env.authMgr.IsEmpty(t.Context())
		require.NoError(t, err)
		assert.True(t, empty, "the scanner must not have become the first admin via the HTML form")
	})
}

// bootstrapFlowToken is the operator's bootstrap token in the configured-token scenarios.
const bootstrapFlowToken = "operator-bootstrap-token-9f8e7d6c5b4a3210"

// newBootstrapEnv is newOwnerFlowEnv's stack with the operator's bootstrap token configured.
func newBootstrapEnv(t *testing.T, dbName string) ownerFlowEnv {
	t.Helper()

	env := newOwnerFlowEnv(t, dbName)
	log := slog.New(slog.DiscardHandler)
	notesMgr, linksMgr := newOwnerFlowManagers(t, env.db, log)

	handler := adaptershttp.NewHandler(notesMgr, linksMgr, log).WithAuthManager(env.authMgr)
	ogen := adaptershttp.NewOgenHandler(notesMgr, linksMgr, adaptershttp.NoPinger{}, log)
	router, err := adaptershttp.NewRouter(handler, ogen, &adaptershttp.RouterOptions{
		MaxBodyBytes:   256 * 1024,
		CSRFSecret:     []byte("padmark-test-csrf-secret-32bytes"),
		BootstrapToken: bootstrapFlowToken,

		ForcedScheme: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0, RateBurst: 0,
		SessionTTL: 0, DisableAPI: false, DirectRedirect: false,
	})
	require.NoError(t, err)

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	env.baseURL = srv.URL

	return env
}

// postSetupForm submits the browser /setup form (CSRF cookie + token from a GET) and returns the
// response status.
func postSetupForm(t *testing.T, baseURL string, fields url.Values) int {
	t.Helper()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)

	client := &http.Client{Jar: jar,
		Transport: nil, CheckRedirect: nil, Timeout: 0,
	}

	page, err := client.Get(baseURL + "/setup")
	require.NoError(t, err)

	raw, err := io.ReadAll(page.Body)
	require.NoError(t, err)
	require.NoError(t, page.Body.Close())
	require.Equal(t, http.StatusOK, page.StatusCode, "the first-admin form must be offered")

	match := setupCSRFPattern.FindSubmatch(raw)
	require.NotNil(t, match, "setup form must carry a CSRF token")
	require.Contains(t, string(raw), `name="bootstrap_token"`, "setup form must ask for the bootstrap token")

	fields.Set("csrf_token", string(match[1]))

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/setup",
		strings.NewReader(fields.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Origin", baseURL)

	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	return resp.StatusCode
}

// TestBootstrapSetup_ConfiguredToken: with the operator's token configured, only a setup that
// presents it creates the first admin — over JSON and over the browser form alike.
func TestBootstrapSetup_ConfiguredToken(t *testing.T) {
	const password = "0perator!Passw0rd"

	t.Run("JSON: wrong token refused, right token creates the admin", func(t *testing.T) {
		env := newBootstrapEnv(t, "bootstrap-token-json")

		for _, body := range []string{
			`{"username":"scanner","password":"` + password + `"}`,
			`{"username":"scanner","password":"` + password + `","bootstrap_token":"guess"}`,
		} {
			resp := setupPost(t, env.baseURL, body)
			require.NoError(t, resp.Body.Close())
			assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		}

		empty, err := env.authMgr.IsEmpty(t.Context())
		require.NoError(t, err)
		require.True(t, empty)

		resp := setupPost(t, env.baseURL,
			`{"username":"operator","password":"`+password+`","bootstrap_token":"`+bootstrapFlowToken+`"}`)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		admin, err := env.users.GetByUsername(t.Context(), "operator")
		require.NoError(t, err)
		assert.True(t, admin.IsAdmin)
	})

	t.Run("form: wrong token refused, right token creates the admin", func(t *testing.T) {
		env := newBootstrapEnv(t, "bootstrap-token-form")
		fields := func(token string) url.Values {
			return url.Values{
				"username": {"operator"}, "password": {password}, "password_confirm": {password},
				"bootstrap_token": {token},
			}
		}

		assert.Equal(t, http.StatusForbidden, postSetupForm(t, env.baseURL, fields("guess")))

		empty, err := env.authMgr.IsEmpty(t.Context())
		require.NoError(t, err)
		require.True(t, empty)

		assert.Equal(t, http.StatusOK, postSetupForm(t, env.baseURL, fields(bootstrapFlowToken)))

		admin, err := env.users.GetByUsername(t.Context(), "operator")
		require.NoError(t, err)
		assert.True(t, admin.IsAdmin)
	})
}
