package http

import (
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"time"

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

const defaultSessionMaxAge = 30 * 24 * time.Hour

//go:embed templates/login.html
var loginTmplSrc string

//go:embed templates/setup.html
var setupTmplSrc string

//go:embed templates/admin.html
var adminTmplSrc string

const sessionCookieName = "padmark_session"

func extractSessionID(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}

	return cookie.Value
}

func userFromContext(r *http.Request) *domain.User {
	usr, _ := r.Context().Value(keyUser).(*domain.User)

	return usr
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
func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
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
func (h *Handler) TOTPLoginHandler(w http.ResponseWriter, r *http.Request) {
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

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessID,
		Path:     "/",
		MaxAge:   int(defaultSessionMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   isHTTPS(r, h.trustedProxies),
		SameSite: http.SameSiteStrictMode,
	})

	rotateCSRFToken(w, r, h.csrfSecret, h.trustedProxies)

	dest := "/"
	if next != "" {
		dest = next
	}

	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// ── logout (POST /logout) ──

// LogoutHandler handles POST /logout — deletes the TOTP session cookie.
// Returns 500 if server-side session deletion fails to prevent the cookie being
// cleared while the session remains valid on the server.
func (h *Handler) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	sessID := extractSessionID(r)
	if sessID != "" && h.authMgr != nil {
		logoutErr := h.authMgr.Logout(r.Context(), sessID)
		if logoutErr != nil {
			h.log.ErrorContext(r.Context(), "logout session", "err", logoutErr)
			http.Error(w, "internal server error", http.StatusInternalServerError)

			return
		}
	}

	http.SetCookie(w, &http.Cookie{
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
func (h *Handler) SetupPage(w http.ResponseWriter, r *http.Request) {
	if h.authMgr == nil {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	token := r.URL.Query().Get("invite")

	if token == "" {
		empty, err := h.authMgr.IsEmpty(r.Context())
		if err != nil {
			h.log.ErrorContext(r.Context(), "check empty", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)

			return
		}

		if !empty {
			http.Redirect(w, r, "/login", http.StatusSeeOther)

			return
		}
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
func (h *Handler) SetupHandler(w http.ResponseWriter, r *http.Request) {
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

	var qrURL string

	var setupErr error

	switch {
	case password != passwordConfirm:
		setupErr = domain.ErrWeakPassword
	case token != "":
		qrURL, setupErr = h.authMgr.AcceptInvite(r.Context(), token, username, password)
	default:
		qrURL, setupErr = h.authMgr.AcceptFirstAdmin(r.Context(), username, password)
	}

	data := setupViewData{Nonce: nonceFromContext(r.Context()), CSRFToken: csrfFromContext(r.Context())}

	if setupErr != nil {
		data.Token = token
		data.Error = setupErrMsg(setupErr)
		data.IsFirstAdmin = token == ""
	} else {
		data.QRCode = template.URL(qrURL) //nolint:gosec // data: URL from internal crypto, not user input
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	renderErr := h.setupTmpl.Execute(w, data)
	if renderErr != nil {
		h.log.ErrorContext(r.Context(), "render setup template", "err", renderErr)
	}
}

// ── change password (GET+POST /change-password) ──

type changePwViewData struct {
	Nonce     string
	Error     string
	CSRFToken string
	Success   bool
}

// ChangePasswordPage handles GET /change-password.
func (h *Handler) ChangePasswordPage(w http.ResponseWriter, r *http.Request) {
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

// doChangePassword performs the password change and returns an error string (empty on success).
// When the session is expired it writes a redirect and returns a sentinel ("redirect").
func (h *Handler) doChangePassword(
	w http.ResponseWriter, r *http.Request, oldPassword, newPassword, confirm string,
) string {
	if newPassword != confirm {
		return "New passwords do not match."
	}

	sessID := extractSessionID(r)

	changeErr := h.authMgr.ChangePassword(r.Context(), sessID, oldPassword, newPassword)
	if changeErr == nil {
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
	case errors.Is(changeErr, domain.ErrWeakPassword):
		return "Password must be at least 12 characters with upper, lower, digit, and special character."
	default:
		return "Something went wrong. Please try again."
	}
}

// ChangePasswordHandler handles POST /change-password.
func (h *Handler) ChangePasswordHandler(w http.ResponseWriter, r *http.Request) {
	if h.authMgr == nil {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	const maxBody = 4096

	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	oldPassword := r.FormValue("old_password")
	newPassword := r.FormValue("new_password")
	newPasswordConfirm := r.FormValue("new_password_confirm")

	data := changePwViewData{Nonce: nonceFromContext(r.Context()), CSRFToken: csrfFromContext(r.Context())}

	errMsg := h.doChangePassword(w, r, oldPassword, newPassword, newPasswordConfirm)
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

// ── admin panel (GET /admin) ──

type adminViewData struct {
	Nonce       string
	InviteURL   string
	InviteError string
	RevokeError string
	CSRFToken   string
	Users       []*domain.User
}

// AdminPage handles GET /admin — lists users for admin.
func (h *Handler) AdminPage(w http.ResponseWriter, r *http.Request) {
	usr := userFromContext(r)
	if usr == nil || !usr.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)

		return
	}

	users, err := h.authMgr.ListUsers(r.Context(), usr.ID)
	if err != nil {
		h.log.ErrorContext(r.Context(), "list users", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	renderErr := h.adminTmpl.Execute(w, adminViewData{
		Nonce:       nonceFromContext(r.Context()),
		Users:       users,
		CSRFToken:   csrfFromContext(r.Context()),
		RevokeError: r.URL.Query().Get("revoke_error"),
	})
	if renderErr != nil {
		h.log.ErrorContext(r.Context(), "render admin template", "err", renderErr)
	}
}

// ── admin: generate invite (POST /admin/invite) ──

// AdminInviteHandler handles POST /admin/invite — generates a single-use invite link.
func (h *Handler) AdminInviteHandler(w http.ResponseWriter, r *http.Request) {
	usr := userFromContext(r)
	if usr == nil || !usr.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)

		return
	}

	token, err := h.authMgr.GenerateInvite(r.Context(), usr.ID)

	users, listErr := h.authMgr.ListUsers(r.Context(), usr.ID)
	if listErr != nil {
		h.log.ErrorContext(r.Context(), "list users after invite", "err", listErr)
	}

	data := adminViewData{
		Nonce:     nonceFromContext(r.Context()),
		Users:     users,
		CSRFToken: csrfFromContext(r.Context()),
	}

	if err != nil {
		data.InviteError = "Failed to generate invite link."
	} else {
		scheme := "http"
		if isHTTPS(r, h.trustedProxies) {
			scheme = "https"
		}

		data.InviteURL = scheme + "://" + r.Host + "/setup?invite=" + url.QueryEscape(token)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	renderErr := h.adminTmpl.Execute(w, data)
	if renderErr != nil {
		h.log.ErrorContext(r.Context(), "render admin template", "err", renderErr)
	}
}

// ── admin: revoke user (POST /admin/users/{id}/revoke) ──

// AdminRevokeHandler handles POST /admin/users/{id}/revoke — removes a user.
func (h *Handler) AdminRevokeHandler(w http.ResponseWriter, r *http.Request) {
	usr := userFromContext(r)
	if usr == nil || !usr.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)

		return
	}

	targetID := r.PathValue("id")
	if targetID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)

		return
	}

	revokeErr := h.authMgr.RevokeUser(r.Context(), usr.ID, targetID)
	if revokeErr != nil {
		h.log.ErrorContext(r.Context(), "revoke user", "target_id", targetID, "err", revokeErr)

		msg := "Failed to revoke user."
		if errors.Is(revokeErr, domain.ErrForbidden) {
			msg = "Cannot revoke: self-revoke or last admin."
		}

		http.Redirect(w, r, "/admin?revoke_error="+url.QueryEscape(msg), http.StatusSeeOther)

		return
	}

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}
