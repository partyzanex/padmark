package http

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	_ "embed"

	"github.com/bluele/gcache"
	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

//go:embed templates/change_password.html
var changePwTmplSrc string

// remoteIP extracts just the host part from r.RemoteAddr for session logging.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

const maxUALen = 512

func truncateUA(ua string) string {
	if len(ua) <= maxUALen {
		return ua
	}

	return ua[:maxUALen]
}

//go:embed templates/login.html
var loginTmplSrc string

//go:embed templates/setup.html
var setupTmplSrc string

const sessionCookieName = "padmark_session"

func extractSessionID(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}

	return cookie.Value
}

func userFromContext(r *http.Request) *domain.User {
	return userFromCtx(r.Context())
}

// userFromCtx is the context-only counterpart of userFromContext, for ogen handlers (OgenHandler
// methods receive only a context.Context, not *http.Request) — see NewRouter for why the
// auth-middleware-enriched context still reaches them: ogen dispatches using r.Context().
func userFromCtx(ctx context.Context) *domain.User {
	usr, _ := ctx.Value(keyUser).(*domain.User)

	return usr
}

// apiTokenHashFromCtx returns the public ID (hash) of the bearer API token the request
// authenticated with, or "" for a browser session or an anonymous caller. Credential-issuing
// operations use it to demand a step-up from a bearer and to record it as the new key's issuer.
func apiTokenHashFromCtx(ctx context.Context) string {
	hash, _ := ctx.Value(keyAPIToken).(string)

	return hash
}

// ownerIDFromCtx returns the authenticated caller's user ID for stamping domain.Note.OwnerID at
// creation, or nil for an anonymous caller (note stays unowned, exactly like before this field
// existed).
func ownerIDFromCtx(ctx context.Context) *uuid.UUID {
	usr := userFromCtx(ctx)
	if usr == nil {
		return nil
	}

	return &usr.ID
}

// callerIDFromCtx returns the authenticated caller's user ID for the owner-bypass check in
// notes.Manager.Update/Delete, or uuid.Nil for an anonymous caller (never matches an owner).
func callerIDFromCtx(ctx context.Context) uuid.UUID {
	usr := userFromCtx(ctx)
	if usr == nil {
		return uuid.Nil
	}

	return usr.ID
}

// authenticatedFromCtx reports whether the request resolved to a TOTP session or API-token user
// — the context-only counterpart of common.isAuthenticated's context check, for ogen handlers
// (see userFromCtx). Unlike common.isAuthenticated, it does NOT special-case "no auth configured
// at all" (context has no way to know that). Callers that need that must use
// common.isAuthenticated instead; this exists for handlers that only see a context.Context,
// e.g. OgenHandler.GetLink.
func authenticatedFromCtx(ctx context.Context) bool {
	return userFromCtx(ctx) != nil
}

// AuthHandler serves login, logout, account setup, and password-change endpoints.
type AuthHandler struct {
	*common

	loginTmpl    *template.Template
	setupTmpl    *template.Template
	changePwTmpl *template.Template
}

func newAuthHandler(c *common, loginTmpl, setupTmpl, changePwTmpl *template.Template) *AuthHandler {
	return &AuthHandler{common: c, loginTmpl: loginTmpl, setupTmpl: setupTmpl, changePwTmpl: changePwTmpl}
}

// RegisterRoutes registers login, logout, account setup, and password-change routes.
func (h *AuthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", h.LoginPage)
	// Per-IP limiter first (cheapest, and every attempt spends the caller's IP budget, including
	// ones the account lock then rejects), then the per-username lock — see withLoginLockout.
	loginLock := newLoginLockout(gcache.NewRealClock())
	mux.HandleFunc("POST /totp-login", h.guard(withTOTPRateLimit(h.trustedProxies, h.log,
		withLoginLockout(loginLock, h.log, h.TOTPLoginHandler))))
	mux.HandleFunc("POST /logout", h.guard(h.LogoutHandler))
	mux.HandleFunc("GET /setup", h.SetupPage)
	mux.HandleFunc("POST /setup", h.setupGuard(h.SetupHandler))
	mux.HandleFunc("GET /change-password", h.ChangePasswordPage)
	mux.HandleFunc("POST /change-password", h.guard(h.ChangePasswordHandler))
}

// ── login page (GET /login) ──

type loginViewData struct {
	Nonce     string
	Next      string
	CSRFToken string
	Error     bool
}

// LoginPage handles GET /login.
func (h *AuthHandler) LoginPage(w http.ResponseWriter, r *http.Request) {
	// Already signed in → don't show the form again; go where the user intended (or home).
	if h.hasValidSession(r) {
		dest := "/"
		if next := safeNextURL(r.URL.Query().Get("next")); next != "" {
			dest = next
		}

		http.Redirect(w, r, dest, http.StatusSeeOther) //nolint:gosec // G710: dest validated by safeNextURL

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.loginTmpl.Execute(w, loginViewData{
		Error:     r.URL.Query().Get("error") == "1",
		Nonce:     nonceFromContext(r.Context()),
		Next:      safeNextURL(r.URL.Query().Get("next")),
		CSRFToken: csrfFromContext(r.Context()),
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render login template", "err", err)
	}
}

// ── TOTP login (POST /totp-login) ──

// TOTPLoginHandler handles POST /totp-login — username + password + TOTP code → session cookie.
func (h *AuthHandler) TOTPLoginHandler(w http.ResponseWriter, r *http.Request) {
	if h.authMgr == nil {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	const maxBody = 4096

	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	username := r.FormValue("username")
	password := r.FormValue("password")
	code := r.FormValue("code")
	next := safeNextURL(r.FormValue("next"))

	sessID, err := h.authMgr.Login(r.Context(), username, password, code, truncateUA(r.UserAgent()), remoteIP(r))
	if err != nil {
		dest := "/login?error=1"
		if next != "" {
			dest += "&next=" + url.QueryEscape(next)
		}

		http.Redirect(w, r, dest, http.StatusSeeOther)

		return
	}

	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly/SameSite set; Secure follows isHTTPS
		Name:     sessionCookieName,
		Value:    sessID,
		Path:     "/",
		MaxAge:   int(h.sessionMaxAge().Seconds()),
		HttpOnly: true,
		Secure:   h.isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		// Unused optional fields zeroed: no Raw, RawExpires, Unparsed, Quoted, Domain, Expires, Partitioned.
		Raw:         "",
		RawExpires:  "",
		Unparsed:    nil,
		Quoted:      false,
		Domain:      "",
		Expires:     time.Time{},
		Partitioned: false,
	})

	rotateCSRFToken(w, r, h.csrfSecret, h.forcedScheme, h.trustedProxies)

	dest := "/"
	if next != "" {
		dest = next
	}

	http.Redirect(w, r, dest, http.StatusSeeOther) //nolint:gosec // G710: dest validated by safeNextURL
}

// ── logout (POST /logout) ──

// LogoutHandler handles POST /logout — deletes the TOTP session cookie.
// Returns 500 if server-side session deletion fails to prevent the cookie being
// cleared while the session remains valid on the server.
func (h *AuthHandler) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	sessID := extractSessionID(r)
	if sessID != "" && h.authMgr != nil {
		logoutErr := h.authMgr.Logout(r.Context(), sessID)
		if logoutErr != nil {
			h.log.ErrorContext(r.Context(), "logout session", "err", logoutErr)
			http.Error(w, "internal server error", http.StatusInternalServerError)

			return
		}
	}

	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly/SameSite set; Secure follows isHTTPS
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		// Unused optional fields zeroed: no Raw, RawExpires, Unparsed, Quoted, Domain, Expires, Partitioned.
		Raw:         "",
		RawExpires:  "",
		Unparsed:    nil,
		Quoted:      false,
		Domain:      "",
		Expires:     time.Time{},
		Partitioned: false,
	})

	clearCSRFCookie(w, r, h.forcedScheme, h.trustedProxies)

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ── setup (GET+POST /setup) ──

type setupViewData struct {
	Nonce        string
	Token        string
	Error        string
	CSRFToken    string
	QRCode       template.URL // data: URL; template.URL bypasses html/template sanitization
	IsFirstAdmin bool
}

// SetupPage handles GET /setup — renders the account creation form.
func (h *AuthHandler) SetupPage(w http.ResponseWriter, r *http.Request) {
	if h.authMgr == nil {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	token := r.URL.Query().Get("invite")

	// Without an invite, /setup only works while bootstrapping the first admin.
	// Once an admin exists it is closed (403) — extra accounts come via invite links.
	if token == "" && !h.bootstrapOpen(w, r) {
		return
	}

	if token == "" && !h.bootstrapEnabled(w, r) {
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	renderErr := h.setupTmpl.Execute(w, setupViewData{
		Nonce:        nonceFromContext(r.Context()),
		Token:        token,
		IsFirstAdmin: token == "",
		CSRFToken:    csrfFromContext(r.Context()),
		// Error/QRCode unset on initial GET render; populated on POST failure or QR display.
		Error:  "",
		QRCode: "",
	})
	if renderErr != nil {
		h.log.ErrorContext(r.Context(), "render setup template", "err", renderErr)
	}
}

func setupErrMsg(err error) string {
	switch {
	case errors.Is(err, domain.ErrInviteExpired):
		return "Invite link has expired."
	case errors.Is(err, domain.ErrInviteUsed):
		return "Invite link has already been used."
	case errors.Is(err, domain.ErrUserExists):
		return "Username is already taken."
	case errors.Is(err, domain.ErrForbidden):
		return "Setup is only available when no users exist."
	case errors.Is(err, domain.ErrWeakPassword):
		return "Password must be at least 12 characters with upper, lower, digit, and special character."
	case errors.Is(err, domain.ErrInvalidUsername):
		return domain.ErrInvalidUsername.Error()
	default:
		return "Something went wrong. Please try again."
	}
}

// SetupHandler handles POST /setup — creates user and returns QR code (HTML) or the otpauth
// enrollment URI (JSON, headless provisioning).
func (h *AuthHandler) SetupHandler(w http.ResponseWriter, r *http.Request) {
	if h.authMgr == nil {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	if negotiate(r) == formatJSON {
		h.setupJSON(w, r)

		return
	}

	const maxBody = 4096

	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	username := r.FormValue("username")
	password := r.FormValue("password")
	passwordConfirm := r.FormValue("password_confirm")
	token := r.FormValue("invite")

	data := setupViewData{
		Nonce:        nonceFromContext(r.Context()),
		CSRFToken:    csrfFromContext(r.Context()),
		Token:        token,
		IsFirstAdmin: token == "",
		// Error/QRCode populated below in the failure/success branches; zero on initial render.
		Error:  "",
		QRCode: "",
	}

	if password != passwordConfirm {
		data.Error = "Passwords do not match."
		h.renderSetup(w, r, &data)

		return
	}

	// Without an invite, creating an account is only allowed while bootstrapping the
	// first admin; once one exists the endpoint is closed (403). Bootstrapping itself demands
	// the operator's bootstrap token, so the first visitor cannot claim the server.
	if token == "" && (!h.bootstrapOpen(w, r) || !h.bootstrapProven(w, r, &data)) {
		return
	}

	var (
		enrollment *auth.Enrollment
		setupErr   error
	)

	if token != "" {
		enrollment, setupErr = h.authMgr.AcceptInvite(r.Context(), token, username, password)
	} else {
		enrollment, setupErr = h.authMgr.AcceptFirstAdmin(r.Context(), username, password)
	}

	if setupErr != nil {
		data.Error = setupErrMsg(setupErr)
	} else {
		data.QRCode = template.URL(enrollment.QRCode) //nolint:gosec // data: URL from internal crypto, not user input
	}

	h.renderSetup(w, r, &data)
}

// ── change password (GET+POST /change-password) ──

type changePwViewData struct {
	Nonce     string
	Error     string
	CSRFToken string
	Success   bool
}

// ChangePasswordPage handles GET /change-password.
func (h *AuthHandler) ChangePasswordPage(w http.ResponseWriter, r *http.Request) {
	if h.authMgr == nil {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	renderErr := h.changePwTmpl.Execute(w, changePwViewData{
		Nonce:     nonceFromContext(r.Context()),
		CSRFToken: csrfFromContext(r.Context()),
		// Error/Success populated by the caller before re-render on failure; zero on GET.
		Error:   "",
		Success: false,
	})
	if renderErr != nil {
		h.log.ErrorContext(r.Context(), "render change password template", "err", renderErr)
	}
}

// ChangePasswordHandler handles POST /change-password.
func (h *AuthHandler) ChangePasswordHandler(w http.ResponseWriter, r *http.Request) {
	if h.authMgr == nil {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	const maxBody = 4096

	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	oldPassword := r.FormValue("old_password")
	newPassword := r.FormValue("new_password")
	newPasswordConfirm := r.FormValue("new_password_confirm")
	totpCode := r.FormValue("totp_code")

	data := changePwViewData{
		Nonce:     nonceFromContext(r.Context()),
		CSRFToken: csrfFromContext(r.Context()),
		// Error/Success populated below on failure/success branches; zero on initial render.
		Error:   "",
		Success: false,
	}

	errMsg, rotatedCSRF := h.doChangePassword(w, r, oldPassword, newPassword, newPasswordConfirm, totpCode)
	if errMsg == "redirect" {
		return
	}

	// A password change (full or partial) rotates the CSRF cookie; the re-rendered form must
	// carry the new token or resubmitting from this page fails the double-submit check (403).
	if rotatedCSRF != "" {
		data.CSRFToken = rotatedCSRF
	}

	data.Error = errMsg
	data.Success = errMsg == ""

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	renderErr := h.changePwTmpl.Execute(w, data)
	if renderErr != nil {
		h.log.ErrorContext(r.Context(), "render change password template", "err", renderErr)
	}
}

// setupGuard enforces CSRF only for the HTML branch of POST /setup. A JSON client carries no
// cookie session, and an invite token — when present — is itself the bearer-equivalent
// credential, so there is no session to forge. The branch is chosen by content negotiation:
// browsers list text/html first and ?raw=1 resolves to formatPlain, so only machine callers
// (Accept: application/json, */*, or no Accept header) reach the unguarded JSON path. A
// scripted caller that previously posted the HTML form with */* never worked anyway — CSRF
// rejected it — so no working client crosses the branch boundary.
func (h *AuthHandler) setupGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if negotiate(r) != formatJSON {
			h.guard(next)(w, r)

			return
		}

		// The body must be application/json (parameters like charset allowed). Safelisted types
		// such as text/plain make a cross-site fetch preflight-free; a non-safelisted content
		// type forces the browser into an OPTIONS preflight it cannot complete without a CORS
		// approval — this holds even where Fetch Metadata is unavailable.
		if !isJSONContentType(r.Header.Get("Content-Type")) {
			writeJSONErrorStatus(w, r, h.log, http.StatusUnsupportedMediaType,
				"setup requires Content-Type: application/json")

			return
		}

		// A page can still force Accept: application/json via a cross-site fetch. fetch-metadata
		// flags that: browsers send Sec-Fetch-Site on every request, plain HTTP clients never do.
		// "none" (direct address-bar) and "same-origin" are the acceptable values — an absent
		// header is covered by the two checks below.
		switch site := r.Header.Get("Sec-Fetch-Site"); site {
		case "", "same-origin", "none":
		default:
			writeJSONErrorStatus(w, r, h.log, http.StatusForbidden, "setup requires a same-origin request")

			return
		}

		// When a browser announced its Origin, it must be this request's own origin: a
		// cross-site POST always carries Origin, so the host comparison settles it even where
		// Sec-Fetch-Site is missing. Machine clients send neither header.
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(r, origin) {
			writeJSONErrorStatus(w, r, h.log, http.StatusForbidden, "setup requires a same-origin request")

			return
		}

		next(w, r)
	}
}

// isJSONContentType reports whether a Content-Type header value names application/json
// (parameters such as charset are allowed to follow).
func isJSONContentType(header string) bool {
	mime, _, _ := strings.Cut(header, ";")

	return strings.EqualFold(strings.TrimSpace(mime), mimeJSON)
}

// hasValidSession reports whether the request carries a valid TOTP session cookie.
// Used to skip auth pages (login/setup) for already-signed-in users. /login is a public
// path, so the auth middleware does not populate the context — resolve the session here.
func (h *AuthHandler) hasValidSession(r *http.Request) bool {
	if h.authMgr == nil {
		return false
	}

	sessID := extractSessionID(r)
	if sessID == "" {
		return false
	}

	_, err := h.authMgr.GetSession(r.Context(), sessID)

	return err == nil
}

// bootstrapOpen reports whether first-admin setup is still allowed (no users yet).
// When closed (an admin already exists) it writes the 403 "setup closed" page and returns
// false; on a lookup error it writes 500 and returns false.
func (h *AuthHandler) bootstrapOpen(w http.ResponseWriter, r *http.Request) bool {
	empty, err := h.authMgr.IsEmpty(r.Context())
	if err != nil {
		h.log.ErrorContext(r.Context(), "check empty", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)

		return false
	}

	if !empty {
		data := setupClosedPageData()
		h.writeErrorPageData(w, r, &data)

		return false
	}

	return true
}

// bootstrapEnabled renders the "setup is disabled" page when no bootstrap token is configured.
func (h *AuthHandler) bootstrapEnabled(w http.ResponseWriter, r *http.Request) bool {
	if len(h.bootstrapDigest) > 0 {
		return true
	}

	data := setupDisabledPageData()
	h.writeErrorPageData(w, r, &data)

	return false
}

// bootstrapProven checks the form's bootstrap token for a first-admin setup: with none configured
// the disabled page is shown, a wrong or missing one re-renders the form with 403.
func (h *AuthHandler) bootstrapProven(w http.ResponseWriter, r *http.Request, data *setupViewData) bool {
	if !h.bootstrapEnabled(w, r) {
		return false
	}

	if h.bootstrapTokenValid(r.FormValue("bootstrap_token")) {
		return true
	}

	data.Error = "Invalid bootstrap token."

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	h.renderSetup(w, r, data)

	return false
}

// bootstrapTokenValid reports whether presented equals the configured bootstrap token. Digests
// are compared in constant time, so neither the token's content nor its length leaks through
// timing; with no token configured nothing is valid.
func (c *common) bootstrapTokenValid(presented string) bool {
	if len(c.bootstrapDigest) == 0 || presented == "" {
		return false
	}

	sum := sha256.Sum256([]byte(presented))

	return subtle.ConstantTimeCompare(sum[:], c.bootstrapDigest) == 1
}

// renderSetup renders the setup template, logging any render error.
func (h *AuthHandler) renderSetup(w http.ResponseWriter, r *http.Request, data *setupViewData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.setupTmpl.Execute(w, data)
	if err != nil {
		h.log.ErrorContext(r.Context(), "render setup template", "err", err)
	}
}

// doChangePassword performs the password change and returns an error string (empty on success)
// plus the rotated CSRF token when the change went through (empty when no rotation happened or
// token generation failed — the request's existing token then stays valid). When the session is
// expired it writes a redirect and returns a sentinel ("redirect").
func (h *AuthHandler) doChangePassword(
	w http.ResponseWriter, r *http.Request, oldPassword, newPassword, confirm, totpCode string,
) (errMsg, rotatedCSRF string) {
	if newPassword != confirm {
		return "New passwords do not match.", ""
	}

	sessID := extractSessionID(r)

	newSessID, changeErr := h.authMgr.ChangePassword(r.Context(), sessID, oldPassword, newPassword, totpCode)
	if changeErr == nil || errors.Is(changeErr, domain.ErrSessionRevocation) {
		// Replace the session cookie: all old sessions were invalidated; use the fresh one.
		http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly/SameSite set; Secure follows isHTTPS
			Name:     sessionCookieName,
			Value:    newSessID,
			Path:     "/",
			MaxAge:   int(h.sessionMaxAge().Seconds()),
			HttpOnly: true,
			Secure:   h.isHTTPS(r),
			SameSite: http.SameSiteStrictMode,
			// Unused optional fields zeroed: no Raw, RawExpires, Unparsed, Quoted, Domain, Expires, Partitioned.
			Raw:         "",
			RawExpires:  "",
			Unparsed:    nil,
			Quoted:      false,
			Domain:      "",
			Expires:     time.Time{},
			Partitioned: false,
		})

		rotatedCSRF = rotateCSRFToken(w, r, h.csrfSecret, h.forcedScheme, h.trustedProxies)

		if errors.Is(changeErr, domain.ErrSessionRevocation) {
			return "Password changed, but older sessions or API tokens could not be revoked. " +
				"You are signed in with a new session. Contact an administrator to revoke older sessions " +
				"and API tokens.", rotatedCSRF
		}

		return "", rotatedCSRF
	}

	if errors.Is(changeErr, domain.ErrSessionExpired) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)

		return "redirect", ""
	}

	return changePasswordErrMsg(changeErr), ""
}

// changePasswordErrMsg maps a failed (non-rotating) password change onto the form's message.
func changePasswordErrMsg(err error) string {
	switch {
	case errors.Is(err, domain.ErrInvalidPassword):
		return "Current password is incorrect."
	case errors.Is(err, domain.ErrInvalidTOTP):
		return "TOTP code is invalid or has expired."
	case errors.Is(err, domain.ErrWeakPassword):
		return "Password must be at least 12 characters with upper, lower, digit, and special character."
	default:
		return "Something went wrong. Please try again."
	}
}
