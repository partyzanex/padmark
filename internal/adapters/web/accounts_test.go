package web_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	time "time"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

// ── POST /setup — JSON (headless) branch ──

func (s *AuthHandlerSuite) postSetup(accept, body, secFetchSite string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/setup?raw=0", bytes.NewBufferString(body))
	req.Header.Set("Accept", accept)
	req.Header.Set("Content-Type", "application/json")

	if secFetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", secFetchSite)
	}

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	return rec
}

func (s *AuthHandlerSuite) enrollment(enr *auth.Enrollment) *auth.Enrollment {
	if enr.TOTPURI == "" {
		enr.TOTPURI = "otpauth://totp/padmark:test?secret=ABC234567ABC234567ABC234&issuer=padmark"
		enr.TOTPURI += "&algorithm=SHA1&digits=6&period=30"
	}

	if enr.QRCode == "" {
		enr.QRCode = "data:image/png;base64,abc"
	}

	return enr
}

// TestSetupJSON_NoAccept_IsTheJSONBranch pins the negotiate precedence: a caller with no
// Accept header (curl, the CLI) reaches the machine-readable branch without any CSRF material.
func (s *AuthHandlerSuite) TestSetupJSON_NoAccept_IsTheJSONBranch() {
	s.auth.EXPECT().IsEmpty(gomock.Any()).Return(true, nil)
	s.auth.EXPECT().AcceptFirstAdmin(gomock.Any(), "admin", testPw).
		Return(s.enrollment(&auth.Enrollment{
			TOTPURI: "", QRCode: "",
		}), nil)

	rec := s.postSetup("", `{"username":"admin","password":"`+testPw+`","bootstrap_token":"`+testBootstrapToken+`"}`, "")

	s.Require().Equal(http.StatusCreated, rec.Code)
	s.Equal("application/json", rec.Header().Get("Content-Type"))

	var out struct {
		OTPAuthURI string `json:"otp_auth_uri"`
		Username   string `json:"username"`
		IsAdmin    bool   `json:"is_admin"`
	}
	s.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &out))
	s.Contains(out.OTPAuthURI, "otpauth://totp/")
	s.Equal("admin", out.Username)
	s.True(out.IsAdmin)
}

// TestSetupJSON_ExplicitJSONAccepted_CreatesWithInvite — an explicit Accept: application/json
// with an invite enrolls a non-admin user (is_admin false in the response).
func (s *AuthHandlerSuite) TestSetupJSON_ExplicitJSONAccepted_CreatesWithInvite() {
	s.auth.EXPECT().AcceptInvite(gomock.Any(), "tok-1", "member", testPw).
		Return(s.enrollment(&auth.Enrollment{
			TOTPURI: "", QRCode: "",
		}), nil)

	rec := s.postSetup("application/json",
		`{"username":"member","password":"`+testPw+`","invite":"tok-1"}`, "")

	s.Require().Equal(http.StatusCreated, rec.Code)

	var out struct {
		IsAdmin bool `json:"is_admin"`
	}
	s.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &out))
	s.False(out.IsAdmin)
}

// TestSetupJSON_BrowserStayOnGuardedHTMLPath — a text/html-first Accept (browsers) never
// crosses into the unguarded JSON path: without CSRF material the request is rejected there.
func (s *AuthHandlerSuite) TestSetupJSON_BrowserStayOnGuardedHTMLPath() {
	rec := s.postSetup("text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		`{"username":"admin","password":"`+testPw+`","bootstrap_token":"`+testBootstrapToken+`"}`, "")

	s.Equal(http.StatusForbidden, rec.Code)
}

// TestSetupJSON_CrossSiteFetch_Rejected closes the CSRF side door: a browser page can force
// Accept: application/json in a cross-site fetch, and fetch-metadata flags it as not same-origin.
func (s *AuthHandlerSuite) TestSetupJSON_CrossSiteFetch_Rejected() {
	rec := s.postSetup("application/json",
		`{"username":"admin","password":"`+testPw+`","bootstrap_token":"`+testBootstrapToken+`"}`, "cross-site")

	s.Equal(http.StatusForbidden, rec.Code)
	s.Contains(rec.Body.String(), "same-origin")
}

// TestSetupJSON_RawQueryNeverJSON — ?raw=1 resolves to formatPlain, so it never reaches the
// unguarded JSON branch even with an Accept a plain machine would send.
func (s *AuthHandlerSuite) TestSetupJSON_RawQueryNeverJSON() {
	req := httptest.NewRequest(http.MethodPost, "/setup?raw=1", bytes.NewBufferString(
		`{"username":"admin","password":"pw"}`))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

// TestSetupJSON_SetupClosed_403OnceAccountsExist — the bootstrap gate answered as JSON.
func (s *AuthHandlerSuite) TestSetupJSON_SetupClosed_403OnceAccountsExist() {
	s.auth.EXPECT().IsEmpty(gomock.Any()).Return(false, nil)

	rec := s.postSetup("application/json",
		`{"username":"admin","password":"`+testPw+`","bootstrap_token":"`+testBootstrapToken+`"}`, "")

	s.Require().Equal(http.StatusForbidden, rec.Code)
	s.Contains(rec.Body.String(), "invite")
}

// TestSetupJSON_BootstrapToken_Refusals: the headless first-admin bootstrap is refused with a
// stable code when no token is configured, or the presented one is missing or wrong (M-2).
func (s *AuthHandlerSuite) TestSetupJSON_BootstrapToken_Refusals() {
	cases := []struct {
		name, configured, body, code string
	}{
		{"none configured", "", `{"username":"admin","password":"` + testPw +
			`","bootstrap_token":"` + testBootstrapToken + `"}`, "bootstrap_disabled"},
		{
			"missing", testBootstrapToken, `{"username":"admin","password":"` + testPw + `"}`,
			"bootstrap_token_invalid",
		},
		{"wrong", testBootstrapToken, `{"username":"admin","password":"` + testPw +
			`","bootstrap_token":"guess"}`, "bootstrap_token_invalid"},
	}

	for _, testCase := range cases {
		s.Run(testCase.name, func() {
			s.auth.EXPECT().IsEmpty(gomock.Any()).Return(true, nil)
			// AcceptFirstAdmin must NOT be called.

			req := httptest.NewRequest(http.MethodPost, "/setup", bytes.NewBufferString(testCase.body))
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Content-Type", "application/json")

			rec := httptest.NewRecorder()

			s.newRouterWithBootstrapToken(testCase.configured).ServeHTTP(rec, req)

			s.Equal(http.StatusForbidden, rec.Code)
			s.Contains(rec.Body.String(), `"code":"`+testCase.code+`"`)
		})
	}
}

// TestSetupJSON_InviteNeedsNoBootstrapToken: the bootstrap token gates only the invite-less
// first-admin path — an invite is its own credential, even with no token configured.
func (s *AuthHandlerSuite) TestSetupJSON_InviteNeedsNoBootstrapToken() {
	s.auth.EXPECT().AcceptInvite(gomock.Any(), "tok", "bob", testPw).
		Return(s.enrollment(&auth.Enrollment{
			TOTPURI: "", QRCode: "",
		}), nil)

	req := httptest.NewRequest(http.MethodPost, "/setup",
		bytes.NewBufferString(`{"username":"bob","password":"`+testPw+`","invite":"tok"}`))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()

	s.newRouterWithBootstrapToken("").ServeHTTP(rec, req)

	s.Equal(http.StatusCreated, rec.Code)
}

// TestSetupJSON_WeakPassword_422 — the weak-password sentinel maps to a machine status with
// the rule spelled out in the message (ValidatePassword itself is enforced in the usecase).
func (s *AuthHandlerSuite) TestSetupJSON_WeakPassword_422() {
	s.auth.EXPECT().IsEmpty(gomock.Any()).Return(true, nil)
	s.auth.EXPECT().AcceptFirstAdmin(gomock.Any(), "admin", "short").
		Return(nil, domain.ErrWeakPassword)

	rec := s.postSetup("application/json",
		`{"username":"admin","password":"short","bootstrap_token":"`+testBootstrapToken+`"}`, "")

	s.Require().Equal(http.StatusUnprocessableEntity, rec.Code)
	s.Contains(rec.Body.String(), "12 characters")
}

// TestSetupJSON_DuplicateUsername_409.
func (s *AuthHandlerSuite) TestSetupJSON_DuplicateUsername_409() {
	s.auth.EXPECT().AcceptInvite(gomock.Any(), "tok", "taken", testPw).
		Return(nil, domain.ErrUserExists)

	rec := s.postSetup("application/json",
		`{"username":"taken","password":"`+testPw+`","invite":"tok"}`, "")

	s.Require().Equal(http.StatusConflict, rec.Code)
	s.Contains(rec.Body.String(), "already taken")
}

// TestSetupJSON_SpentInvite_410.
func (s *AuthHandlerSuite) TestSetupJSON_SpentInvite_410() {
	s.auth.EXPECT().AcceptInvite(gomock.Any(), "used", "member", testPw).
		Return(nil, domain.ErrInviteUsed)

	rec := s.postSetup("application/json",
		`{"username":"member","password":"`+testPw+`","invite":"used"}`, "")

	s.Require().Equal(http.StatusGone, rec.Code)
}

// TestSetupJSON_InvalidUsername_422 — a non-empty username that fails domain.ValidateUsername
// is a validation failure (422), like a weak password; the message spells out the rule.
func (s *AuthHandlerSuite) TestSetupJSON_InvalidUsername_422() {
	s.auth.EXPECT().AcceptInvite(gomock.Any(), "tok", "bad/name", testPw).
		Return(nil, fmt.Errorf("accept invite: %w", domain.ErrInvalidUsername))

	rec := s.postSetup("application/json",
		`{"username":"bad/name","password":"`+testPw+`","invite":"tok"}`, "")

	s.Require().Equal(http.StatusUnprocessableEntity, rec.Code)
	s.Contains(rec.Body.String(), "username must be")
}

// TestSetupJSON_EmptyUsername_400 pins the required-field pre-check: an empty or blank username
// is a malformed request (400) answered before the bootstrap gate or any AuthManager call.
func (s *AuthHandlerSuite) TestSetupJSON_EmptyUsername_400() {
	for _, username := range []string{"", "   "} {
		rec := s.postSetup("application/json",
			`{"username":"`+username+`","password":"`+testPw+`","bootstrap_token":"`+testBootstrapToken+`"}`, "")

		s.Equal(http.StatusBadRequest, rec.Code, "username %q", username)
		s.Contains(rec.Body.String(), "username is required")
	}
}

// ── POST /api/tokens, POST /api/invites ──

func (s *AuthHandlerSuite) postOgen(path, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	return rec
}

// TestCreateAPIToken_ValidBearer_ReturnsEnvelope proves self-service issuance end-to-end over
// the generated route: the envelope decodes to the request's own base URL and the plain key.
func (s *AuthHandlerSuite) TestCreateAPIToken_ValidBearer_ReturnsEnvelope() {
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "good-api-key").Return(usr, "token-hash", nil)
	s.auth.EXPECT().CreateAPIToken(gomock.Any(), testUserID1, gomock.Any(), gomock.Any()).Return("plain-key-1", nil)

	rec := s.postOgen("/api/tokens", "good-api-key")

	s.Require().Equal(http.StatusCreated, rec.Code)

	var out struct {
		Token string `json:"token"`
	}
	s.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &out))

	base, key, decoded := domain.DecodeAPITokenEnvelope(out.Token)
	s.True(decoded)
	s.Equal("plain-key-1", key)
	// httptest request host; scheme detected from the non-TLS request.
	s.Equal("http://example.com", base)
}

// TestCreateAPIToken_TokenCap_429 — the 20-token cap maps to the documented quota status.
func (s *AuthHandlerSuite) TestCreateAPIToken_TokenCap_429() {
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "good-api-key").Return(usr, "token-hash", nil)
	s.auth.EXPECT().CreateAPIToken(gomock.Any(), testUserID1, gomock.Any(), gomock.Any()).
		Return("", domain.ErrAPITokenLimit)

	rec := s.postOgen("/api/tokens", "good-api-key")

	s.Require().Equal(http.StatusTooManyRequests, rec.Code)
	s.Contains(rec.Body.String(), "api token limit reached")
}

// TestCreateInvite_AdminBearer_ReturnsSetupLink — admin issuance over the ogen route.
func (s *AuthHandlerSuite) TestCreateInvite_AdminBearer_ReturnsSetupLink() {
	admin := s.adminUser()
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "admin-key").Return(admin, "token-hash", nil)
	s.auth.EXPECT().GenerateInvite(gomock.Any(), testAdminID, gomock.Any()).Return("inv-token", nil)

	rec := s.postOgen("/api/invites", "admin-key")

	s.Require().Equal(http.StatusCreated, rec.Code)

	var out struct {
		InviteURL string `json:"invite_url"`
	}
	s.Require().NoError(json.Unmarshal(rec.Body.Bytes(), &out))
	s.Contains(out.InviteURL, "/setup?invite=inv-token")
}

// TestCreateInvite_ForcedScheme_Honoured — behind a TLS-terminating proxy with --public-scheme,
// the invite link must carry the public scheme, not the internal http.
func (s *AuthHandlerSuite) TestCreateInvite_ForcedScheme_Honoured() {
	router := s.newRouterWithAuthAndScheme("https")

	admin := s.adminUser()
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "admin-key").Return(admin, "token-hash", nil)
	s.auth.EXPECT().GenerateInvite(gomock.Any(), testAdminID, gomock.Any()).Return("inv-token", nil)

	req := httptest.NewRequest(http.MethodPost, "/api/invites", nil)
	req.Header.Set("Authorization", "Bearer admin-key")

	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	s.Require().Equal(http.StatusCreated, rec.Code)
	s.Contains(rec.Body.String(), "https://example.com/setup?invite=")
}

// TestCreateInvite_NonAdmin_403 — a non-admin bearer gets the admin gate answered as JSON.
func (s *AuthHandlerSuite) TestCreateInvite_NonAdmin_403() {
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "good-api-key").Return(usr, "token-hash", nil)

	rec := s.postOgen("/api/invites", "good-api-key")

	s.Require().Equal(http.StatusForbidden, rec.Code)
}

// TestAccountEndpoints_RequireCredentials — neither /api/tokens nor /api/invites is a public
// path: without a credential the middleware answers 401 before any handler runs.
func (s *AuthHandlerSuite) TestAccountEndpoints_RequireCredentials() {
	s.Equal(http.StatusUnauthorized, s.postOgen("/api/tokens", "").Code)
	s.Equal(http.StatusUnauthorized, s.postOgen("/api/invites", "").Code)
}

// TestAccountEndpoints_DisabledAPI_503 — --disable-api short-circuits the account endpoints
// like the rest of the machine surface: the middleware runs outside auth, so a valid bearer
// key must not leak provisioning calls on a disabled instance.
func (s *AuthHandlerSuite) TestAccountEndpoints_DisabledAPI_503() {
	handler := web.NewHandler(s.manager, s.linkManager, discardLog())
	ogen := web.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog())

	opts := web.RouterOptions{
		MaxBodyBytes: 256 * 1024,
		CSRFSecret:   authTestCSRFSecret,
		DisableAPI:   true,

		ForcedScheme: "", BootstrapToken: "", TrustedProxies: nil, AllowedHosts: nil, RateLimit: 0,
		RateBurst: 0, SessionTTL: 0, DirectRedirect: false,
	}

	router, err := web.NewRouter(handler, ogen, &opts)
	s.Require().NoError(err)

	req := httptest.NewRequest(http.MethodPost, "/api/tokens", nil)
	req.Header.Set("Authorization", "Bearer good-api-key")

	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	s.Equal(http.StatusServiceUnavailable, rec.Code)

	req2 := httptest.NewRequest(http.MethodPost, "/api/invites", nil)
	req2.Header.Set("Authorization", "Bearer good-api-key")

	rec2 := httptest.NewRecorder()

	router.ServeHTTP(rec2, req2)
	s.Equal(http.StatusServiceUnavailable, rec2.Code)
}

// TestAccountEndpoints_FeatureNotEnabled_503 — with accounts enabled but the API-token store
// not wired (auth.NewManager with a nil store), issuance fails the documented 503.
func (s *AuthHandlerSuite) TestAccountEndpoints_FeatureNotEnabled_503() {
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "good-api-key").Return(usr, "token-hash", nil)
	s.auth.EXPECT().CreateAPIToken(gomock.Any(), testUserID1, gomock.Any(), gomock.Any()).
		Return("", domain.ErrFeatureNotSupported)

	rec := s.postOgen("/api/tokens", "good-api-key")

	s.Require().Equal(http.StatusServiceUnavailable, rec.Code)
	s.Contains(rec.Body.String(), "feature is not enabled")
}

// ── origin/CSRF hardening (P1: JSON setup, P2: account endpoints) ──

// TestSetupJSON_WrongContentType_Rejected415 pins the text/plain smuggle fix: a JSON body under
// a CORS-safelisted Content-Type made a cross-site fetch preflight-free; requiring the
// non-safelisted application/json forces every cross-origin browser fetch into an OPTIONS
// preflight it cannot complete — independent of Fetch Metadata support.
func (s *AuthHandlerSuite) TestSetupJSON_WrongContentType_Rejected415() {
	req := httptest.NewRequest(http.MethodPost, "/setup",
		bytes.NewBufferString(`{"username":"admin","password":"`+testPw+`","bootstrap_token":"`+testBootstrapToken+`"}`))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "text/plain") // safelisted ⇒ no browser preflight

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	// No manager expectations — the gate must answer before any business logic.
	s.Equal(http.StatusUnsupportedMediaType, rec.Code)
	s.Contains(rec.Body.String(), "application/json")
}

// TestSetupJSON_CrossOriginHeader_Rejected403 reproduces the reported P1 vector without
// relying on Fetch Metadata: a preflight-free-looking request whose Origin announces a foreign
// site must be rejected even though Sec-Fetch-Site is absent.
func (s *AuthHandlerSuite) TestSetupJSON_CrossOriginHeader_Rejected403() {
	req := httptest.NewRequest(http.MethodPost, "/setup",
		bytes.NewBufferString(`{"username":"admin","password":"`+testPw+`","bootstrap_token":"`+testBootstrapToken+`"}`))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example") // no Sec-Fetch-Site at all

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
	s.Contains(rec.Body.String(), "same-origin")
}

// TestSetupJSON_SameOriginHeader_OK — a browser fetch on the same host carries its own origin;
// that must keep working.
func (s *AuthHandlerSuite) TestSetupJSON_SameOriginHeader_OK() {
	s.auth.EXPECT().IsEmpty(gomock.Any()).Return(true, nil)
	s.auth.EXPECT().AcceptFirstAdmin(gomock.Any(), "admin", testPw).
		Return(s.enrollment(&auth.Enrollment{
			TOTPURI: "", QRCode: "",
		}), nil)

	req := httptest.NewRequest(http.MethodPost, "/setup",
		bytes.NewBufferString(`{"username":"admin","password":"`+testPw+`","bootstrap_token":"`+testBootstrapToken+`"}`))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com") // httptest.NewRequest default host

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Require().Equal(http.StatusCreated, rec.Code)
}

// TestCreateAPIToken_CrossOriginHeader_Rejected is the P2 regression: a same-site
// sibling-subdomain page posts to /api/tokens riding the user's session cookie; the request
// always declares Origin, so a host mismatch answers 403 before ResolveAPIToken runs.
func (s *AuthHandlerSuite) TestCreateAPIToken_CrossOriginHeader_Rejected() {
	// The auth middleware resolves the Bearer credential before the handler; only the mint
	// itself must be blocked (the expectation for CreateAPIToken stays unset on purpose).
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "good-api-key").Return(usr, "token-hash", nil)

	req := s.postOgenReq("/api/tokens", "good-api-key")
	req.Header.Set("Origin", "https://evil.sibling.example")

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
	s.Equal("application/json", rec.Header().Get("Content-Type"))
	s.Contains(rec.Body.String(), "cross-origin")
}

// TestCreateAPIToken_SameOriginHeader_OK — the browser-side same-origin call keeps working.
func (s *AuthHandlerSuite) TestCreateAPIToken_SameOriginHeader_OK() {
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "good-api-key").Return(usr, "token-hash", nil)
	s.auth.EXPECT().CreateAPIToken(gomock.Any(), testUserID1, gomock.Any(), gomock.Any()).Return("plain-key-1", nil)

	req := s.postOgenReq("/api/tokens", "good-api-key")
	req.Header.Set("Origin", "http://example.com")

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Require().Equal(http.StatusCreated, rec.Code)
}

// TestCreateInvite_CrossOriginHeader_Rejected — the admin-only invite mint is equally
// cookie-forgeable, so the same origin gate applies.
func (s *AuthHandlerSuite) TestCreateInvite_CrossOriginHeader_Rejected() {
	admin := s.adminUser()
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "admin-key").Return(admin, "token-hash", nil)

	req := s.postOgenReq("/api/invites", "admin-key")
	req.Header.Set("Origin", "https://evil.sibling.example")

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	s.Equal(http.StatusForbidden, rec.Code)
}

// postOgenReq builds an ogen-route request with the Bearer header set; the caller may add
// additional headers before serving it.
func (s *AuthHandlerSuite) postOgenReq(path, bearer string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	return req
}

// ── step-up and lifetime on the issuing endpoints (M-6) ──

// postOgenJSON posts a JSON body to an ogen route with an optional bearer key or session cookie.
func (s *AuthHandlerSuite) postOgenJSON(path, bearer, session, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")

	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	if session != "" {
		req.AddCookie(s.sessionCookie(session))
	}

	rec := httptest.NewRecorder()

	s.router.ServeHTTP(rec, req)

	return rec
}

// TestCreateAPIToken_Bearer_PassesIssuerStepUpAndLifetime: a bearer caller is identified to the
// usecase by its key's ID, together with the body's step-up and lifetime.
func (s *AuthHandlerSuite) TestCreateAPIToken_Bearer_PassesIssuerStepUpAndLifetime() {
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "good-api-key").Return(usr, "parent-hash", nil)
	s.auth.EXPECT().CreateAPIToken(gomock.Any(), testUserID1, auth.Issuer{
		APITokenHash: "parent-hash",
		StepUp:       auth.StepUp{Password: "pw", TOTPCode: "123456"},
	}, 30).Return("plain-key-1", nil)

	rec := s.postOgenJSON("/api/tokens", "good-api-key", "",
		`{"ttl_days":30,"password":"pw","totp_code":"123456"}`)

	s.Equal(http.StatusCreated, rec.Code, rec.Body.String())
}

// TestCreateAPIToken_Session_IssuesWithoutBearer: a browser session is not a bearer — the usecase
// sees no issuing key, so no step-up is demanded.
func (s *AuthHandlerSuite) TestCreateAPIToken_Session_IssuesWithoutBearer() {
	usr := &domain.User{
		ID: testUserID1, Username: "alice",
		CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
		KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
	}
	s.auth.EXPECT().GetSession(gomock.Any(), "sess").Return(usr, nil)
	s.auth.EXPECT().CreateAPIToken(gomock.Any(), testUserID1, auth.Issuer{
		APITokenHash: "", StepUp: auth.StepUp{Password: "", TOTPCode: ""},
	}, 7).Return("plain-key-1", nil)

	rec := s.postOgenJSON("/api/tokens", "", "sess", `{"ttl_days":7}`)

	s.Equal(http.StatusCreated, rec.Code, rec.Body.String())
}

// TestCreateAPIToken_Refusals: step-up refusals are 403 with a machine-readable code, an
// out-of-range lifetime is 422 (ogen's schema bounds catch it before the handler, too).
func (s *AuthHandlerSuite) TestCreateAPIToken_Refusals() {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrStepUpRequired, http.StatusForbidden, "step_up_required"},
		{domain.ErrStepUpFailed, http.StatusForbidden, "step_up_failed"},
		{domain.ErrInvalidAPITokenTTL, http.StatusUnprocessableEntity, ""},
	}

	for _, testCase := range cases {
		s.Run(testCase.err.Error(), func() {
			usr := &domain.User{
				ID: testUserID1, Username: "alice",
				CreatedAt: time.Time{}, LastLoginAt: nil, TOTPSecret: "", PasswordHash: "",
				KDFSalt: nil, LastTOTPCounter: 0, IsAdmin: false,
			}
			s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "good-api-key").Return(usr, "parent-hash", nil)
			s.auth.EXPECT().CreateAPIToken(gomock.Any(), testUserID1, gomock.Any(), gomock.Any()).
				Return("", fmt.Errorf("wrapped: %w", testCase.err))

			rec := s.postOgenJSON("/api/tokens", "good-api-key", "", `{}`)

			s.Equal(testCase.status, rec.Code)

			if testCase.code != "" {
				s.Contains(rec.Body.String(), `"code":"`+testCase.code+`"`)
			}
		})
	}

	rec := s.postOgenJSON("/api/tokens", "", "", `{"ttl_days":3651}`)
	s.NotEqual(http.StatusCreated, rec.Code, "the schema bounds ttl_days")
}

// TestCreateInvite_Bearer_StepUp: the invite endpoint forwards the bearer and step-up the same
// way and reports a missing step-up as 403 step_up_required.
func (s *AuthHandlerSuite) TestCreateInvite_Bearer_StepUp() {
	admin := s.adminUser()
	s.auth.EXPECT().ResolveAPIToken(gomock.Any(), "admin-key").Return(admin, "parent-hash", nil).Times(2)
	s.auth.EXPECT().GenerateInvite(gomock.Any(), testAdminID, auth.Issuer{
		APITokenHash: "parent-hash",
		StepUp:       auth.StepUp{Password: "", TOTPCode: ""},
	}).
		Return("", domain.ErrStepUpRequired)
	s.auth.EXPECT().GenerateInvite(gomock.Any(), testAdminID, auth.Issuer{
		APITokenHash: "parent-hash",
		StepUp:       auth.StepUp{Password: "pw", TOTPCode: "123456"},
	}).Return("inv-token", nil)

	rec := s.postOgenJSON("/api/invites", "admin-key", "", ``)
	s.Equal(http.StatusForbidden, rec.Code)
	s.Contains(rec.Body.String(), `"code":"step_up_required"`)

	rec = s.postOgenJSON("/api/invites", "admin-key", "", `{"password":"pw","totp_code":"123456"}`)
	s.Equal(http.StatusCreated, rec.Code, rec.Body.String())
}
