package web

import (
	_ "embed"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/bluele/gcache"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

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

// AuthHandler serves login, logout, account setup (web_setup.go, web_setup_json.go), and
// password-change (web_password.go) endpoints.
type AuthHandler struct {
	*common

	// sessions and onboarding hold disabledAccounts when the account system is off.
	sessions   SessionManager
	onboarding OnboardingManager

	loginTmpl    *template.Template
	setupTmpl    *template.Template
	changePwTmpl *template.Template
}

func newAuthHandler(c *common, accounts AuthManager) *AuthHandler {
	return &AuthHandler{
		common: c, sessions: accounts, onboarding: accounts,
		loginTmpl:    parseTmpl("login", loginTmplSrc),
		setupTmpl:    parseTmpl("setup", setupTmplSrc),
		changePwTmpl: parseTmpl("change_password", changePwTmplSrc),
	}
}

// RegisterRoutes registers login, logout, account setup, and password-change routes.
func (h *AuthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /login", h.LoginPage)
	// Per-IP limiter first (cheapest, and every attempt spends the caller's IP budget, including
	// ones the account lock then rejects), then the per-username lock — see WithLoginLockout.
	loginLock := middleware.NewLoginLockout(gcache.NewRealClock())
	mux.HandleFunc("POST /totp-login", h.guard(middleware.WithTOTPRateLimit(h.trustedProxies, h.log,
		middleware.WithLoginLockout(loginLock, h.log, h.TOTPLoginHandler))))
	mux.HandleFunc("POST /logout", h.guard(h.LogoutHandler))
	mux.HandleFunc("GET /setup", h.SetupPage)
	mux.HandleFunc("POST /setup", h.setupGuard(h.SetupHandler))
	mux.HandleFunc("GET /change-password", h.ChangePasswordPage)
	mux.HandleFunc("POST /change-password", h.guard(h.ChangePasswordHandler))
}

// ── login page (GET /login) ──.

type loginViewData struct {
	Nonce     string
	Next      string
	CSRFToken string
	Header    headerView
	Error     bool
}

// LoginPage handles GET /login.
func (h *AuthHandler) LoginPage(w http.ResponseWriter, r *http.Request) {
	// Already signed in → don't show the form again; go where the user intended (or home).
	if h.hasValidSession(r) {
		dest := "/"
		if next := middleware.SafeNextURL(r.URL.Query().Get("next")); next != "" {
			dest = next
		}

		http.Redirect(w, r, dest, http.StatusSeeOther) //nolint:gosec // G710: dest validated by SafeNextURL

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.loginTmpl.Execute(w, loginViewData{
		Header:    h.header(r, headerKindLogin),
		Error:     r.URL.Query().Get("error") == "1",
		Nonce:     middleware.NonceFromContext(r.Context()),
		Next:      middleware.SafeNextURL(r.URL.Query().Get("next")),
		CSRFToken: middleware.CSRFFromContext(r.Context()),
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render login template", "err", err)
	}
}

// ── TOTP login (POST /totp-login) ──.

// TOTPLoginHandler handles POST /totp-login — username + password + TOTP code → session cookie.
func (h *AuthHandler) TOTPLoginHandler(w http.ResponseWriter, r *http.Request) {
	if h.accounts == accountsOff {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	const maxBody = 4096

	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	username := r.FormValue("username")
	password := r.FormValue("password")
	code := r.FormValue("code")
	next := middleware.SafeNextURL(r.FormValue("next"))

	// The session cookie the browser presented, if any, is revoked by the usecase on success.
	sessID, err := h.sessions.Login(r.Context(), &auth.LoginRequest{
		Username: username, Password: password, TOTPCode: code,
		UserAgent: truncateUA(r.UserAgent()), ClientIP: remoteIP(r), PreviousSessionID: middleware.ExtractSessionID(r),
	})
	if err != nil {
		dest := "/login?error=1"
		if next != "" {
			dest += "&next=" + url.QueryEscape(next)
		}

		http.Redirect(w, r, dest, http.StatusSeeOther)

		return
	}

	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly/SameSite set; Secure follows IsHTTPS
		Name:     middleware.SessionCookieName,
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

	// The CSRF token is bound to the session: re-mint it for the session just issued.
	middleware.RotateCSRFToken(w, r, h.csrfSecret, sessID, h.forcedScheme, h.trustedProxies)

	dest := "/"
	if next != "" {
		dest = next
	}

	http.Redirect(w, r, dest, http.StatusSeeOther) //nolint:gosec // G710: dest validated by SafeNextURL
}

// ── logout (POST /logout) ──.

// LogoutHandler handles POST /logout — deletes the TOTP session cookie.
// Returns 500 if server-side session deletion fails to prevent the cookie being
// cleared while the session remains valid on the server.
func (h *AuthHandler) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	sessID := middleware.ExtractSessionID(r)
	if sessID != "" && h.accounts == accountsOn {
		logoutErr := h.sessions.Logout(r.Context(), sessID)
		if logoutErr != nil {
			h.log.ErrorContext(r.Context(), "logout session", "err", logoutErr)
			http.Error(w, "internal server error", http.StatusInternalServerError)

			return
		}
	}

	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly/SameSite set; Secure follows IsHTTPS
		Name:     middleware.SessionCookieName,
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

	middleware.ClearCSRFCookie(w, r, h.forcedScheme, h.trustedProxies)

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// hasValidSession reports whether the request carries a valid TOTP session cookie.
// Used to skip auth pages (login/setup) for already-signed-in users. /login is a public
// path, so the auth middleware does not populate the context — resolve the session here.
func (h *AuthHandler) hasValidSession(r *http.Request) bool {
	sessID := middleware.ExtractSessionID(r)
	if sessID == "" {
		return false
	}

	_, err := h.sessions.GetSession(r.Context(), sessID)

	return err == nil
}
