package web

import (
	_ "embed"
	"errors"
	"net/http"
	"time"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
	"github.com/partyzanex/padmark/internal/domain"
)

// ── change password (GET+POST /change-password) ──.

//go:embed templates/change_password.html
var changePwTmplSrc string

type changePwViewData struct {
	Nonce     string
	Error     string
	CSRFToken string
	Header    headerView
	Success   bool
}

// ChangePasswordPage handles GET /change-password.
func (h *AuthHandler) ChangePasswordPage(w http.ResponseWriter, r *http.Request) {
	if h.accounts == accountsOff {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	renderErr := h.changePwTmpl.Execute(w, changePwViewData{
		Header:    h.header(r, headerKindPlain),
		Nonce:     middleware.NonceFromContext(r.Context()),
		CSRFToken: middleware.CSRFFromContext(r.Context()),
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
	if h.accounts == accountsOff {
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
		Header:    h.header(r, headerKindPlain),
		Nonce:     middleware.NonceFromContext(r.Context()),
		CSRFToken: middleware.CSRFFromContext(r.Context()),
		// Error/Success populated below on failure/success branches; zero on initial render.
		Error:   "",
		Success: false,
	}

	errMsg, rotatedCSRF := h.doChangePassword(w, r, oldPassword, newPassword, newPasswordConfirm, totpCode)
	if errMsg == "redirect" {
		return
	}

	// A password change rotates the CSRF cookie; the re-rendered form must
	// carry the new token or resubmitting from this page fails the double-submit check (403).
	if rotatedCSRF != "" {
		data.CSRFToken = rotatedCSRF
		// The sign-out form of the header is on the same page and needs the same token.
		data.Header.CSRFToken = rotatedCSRF
	}

	data.Error = errMsg
	data.Success = errMsg == ""

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	renderErr := h.changePwTmpl.Execute(w, data)
	if renderErr != nil {
		h.log.ErrorContext(r.Context(), "render change password template", "err", renderErr)
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

	sessID := middleware.ExtractSessionID(r)

	newSessID, changeErr := h.sessions.ChangePassword(r.Context(), sessID, oldPassword, newPassword, totpCode)
	if changeErr == nil {
		// Replace the session cookie: all old sessions were invalidated; use the fresh one.
		http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: HttpOnly/SameSite set; Secure follows IsHTTPS
			Name:     middleware.SessionCookieName,
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

		rotatedCSRF = middleware.RotateCSRFToken(w, r, h.csrfSecret, newSessID, h.forcedScheme, h.trustedProxies)

		return "", rotatedCSRF
	}

	if errors.Is(changeErr, domain.ErrSessionExpired) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)

		return "redirect", ""
	}

	return changePasswordErrMsg(changeErr), ""
}

// changePasswordErrMsg maps a failed (non-rotating) password change onto the form's message. A
// failed change keeps the old password and access, so the default message asks for a retry.
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
