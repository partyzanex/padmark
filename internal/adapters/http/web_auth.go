package http

import (
	"context"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"

	_ "embed"

	"github.com/partyzanex/padmark/internal/domain"
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

// ownerIDFromCtx returns the authenticated caller's user ID for stamping domain.Note.OwnerID at
// creation, or nil for an anonymous caller (note stays unowned, exactly like before this field
// existed).
func ownerIDFromCtx(ctx context.Context) *string {
	usr := userFromCtx(ctx)
	if usr == nil {
		return nil
	}

	return &usr.ID
}

// callerIDFromCtx returns the authenticated caller's user ID for the owner-bypass check in
// notes.Manager.Update/Delete, or "" for an anonymous caller (never matches an owner).
func callerIDFromCtx(ctx context.Context) string {
	usr := userFromCtx(ctx)
	if usr == nil {
		return ""
	}

	return usr.ID
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

	// POST /login is the legacy bearer-token login — no TOTP brute-force concern,
	// so the TOTP-tuned 10 req/min rate limit is not applied here.
	mux.HandleFunc("POST /login", h.guard(loginHandler(h.allowedTokens, h.cookieMaxAge, h.trustedProxies)))
	mux.HandleFunc("POST /totp-login", h.guard(withTOTPRateLimit(h.trustedProxies, h.TOTPLoginHandler)))
	mux.HandleFunc("POST /logout", h.guard(h.LogoutHandler))
	mux.HandleFunc("GET /setup", h.SetupPage)
	mux.HandleFunc("POST /setup", h.guard(h.SetupHandler))
	mux.HandleFunc("GET /change-password", h.ChangePasswordPage)
	mux.HandleFunc("POST /change-password", h.guard(h.ChangePasswordHandler))
}

// ── login page (GET /login) ──

type loginViewData struct {
	Nonce     string
	Next      string
	CSRFToken string
	Error     bool
	TOTPMode  bool // show TOTP form instead of token form
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
		TOTPMode:  h.authMgr != nil,
		CSRFToken: csrfFromContext(r.Context()),
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render login template", "err", err)
	}
}

// ── TOTP login (POST /login when authMgr is set) ──

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

	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly/SameSite set; Secure follows TLS detection
		Name:     sessionCookieName,
		Value:    sessID,
		Path:     "/",
		MaxAge:   int(h.sessionMaxAge().Seconds()),
		HttpOnly: true,
		Secure:   isHTTPS(r, h.trustedProxies),
		SameSite: http.SameSiteStrictMode,
	})

	rotateCSRFToken(w, r, h.csrfSecret, h.trustedProxies)

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

	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly/SameSite set; Secure follows TLS detection
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPS(r, h.trustedProxies),
		SameSite: http.SameSiteStrictMode,
	})

	clearCSRFCookie(w, r, h.trustedProxies)

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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	renderErr := h.setupTmpl.Execute(w, setupViewData{
		Nonce:        nonceFromContext(r.Context()),
		Token:        token,
		IsFirstAdmin: token == "",
		CSRFToken:    csrfFromContext(r.Context()),
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
	default:
		return "Something went wrong. Please try again."
	}
}

// SetupHandler handles POST /setup — creates user and returns QR code.
func (h *AuthHandler) SetupHandler(w http.ResponseWriter, r *http.Request) {
	if h.authMgr == nil {
		http.Error(w, "not found", http.StatusNotFound)

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
	}

	if password != passwordConfirm {
		data.Error = "Passwords do not match."
		h.renderSetup(w, r, &data)

		return
	}

	// Without an invite, creating an account is only allowed while bootstrapping the
	// first admin; once one exists the endpoint is closed (403).
	if token == "" && !h.bootstrapOpen(w, r) {
		return
	}

	var (
		qrURL    string
		setupErr error
	)

	if token != "" {
		qrURL, setupErr = h.authMgr.AcceptInvite(r.Context(), token, username, password)
	} else {
		qrURL, setupErr = h.authMgr.AcceptFirstAdmin(r.Context(), username, password)
	}

	if setupErr != nil {
		data.Error = setupErrMsg(setupErr)
	} else {
		data.QRCode = template.URL(qrURL) //nolint:gosec // data: URL from internal crypto, not user input
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

	data := changePwViewData{Nonce: nonceFromContext(r.Context()), CSRFToken: csrfFromContext(r.Context())}

	errMsg := h.doChangePassword(w, r, oldPassword, newPassword, newPasswordConfirm, totpCode)
	if errMsg == "redirect" {
		return
	}

	data.Error = errMsg
	data.Success = errMsg == ""

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	renderErr := h.changePwTmpl.Execute(w, data)
	if renderErr != nil {
		h.log.ErrorContext(r.Context(), "render change password template", "err", renderErr)
	}
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

// renderSetup renders the setup template, logging any render error.
func (h *AuthHandler) renderSetup(w http.ResponseWriter, r *http.Request, data *setupViewData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.setupTmpl.Execute(w, data)
	if err != nil {
		h.log.ErrorContext(r.Context(), "render setup template", "err", err)
	}
}

// doChangePassword performs the password change and returns an error string (empty on success).
// When the session is expired it writes a redirect and returns a sentinel ("redirect").
func (h *AuthHandler) doChangePassword(
	w http.ResponseWriter, r *http.Request, oldPassword, newPassword, confirm, totpCode string,
) string {
	if newPassword != confirm {
		return "New passwords do not match."
	}

	sessID := extractSessionID(r)

	newSessID, changeErr := h.authMgr.ChangePassword(r.Context(), sessID, oldPassword, newPassword, totpCode)
	if changeErr == nil {
		// Replace the session cookie: all old sessions were invalidated; use the fresh one.
		http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly/SameSite set; Secure follows TLS detection
			Name:     sessionCookieName,
			Value:    newSessID,
			Path:     "/",
			MaxAge:   int(h.sessionMaxAge().Seconds()),
			HttpOnly: true,
			Secure:   isHTTPS(r, h.trustedProxies),
			SameSite: http.SameSiteStrictMode,
		})

		rotateCSRFToken(w, r, h.csrfSecret, h.trustedProxies)

		return ""
	}

	if errors.Is(changeErr, domain.ErrSessionExpired) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)

		return "redirect"
	}

	switch {
	case errors.Is(changeErr, domain.ErrInvalidPassword):
		return "Current password is incorrect."
	case errors.Is(changeErr, domain.ErrInvalidTOTP):
		return "TOTP code is invalid or has expired."
	case errors.Is(changeErr, domain.ErrWeakPassword):
		return "Password must be at least 12 characters with upper, lower, digit, and special character."
	default:
		return "Something went wrong. Please try again."
	}
}
