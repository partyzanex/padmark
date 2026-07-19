package http

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"

	_ "embed"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

//go:embed templates/admin.html
var adminTmplSrc string

// AdminHandler serves the admin panel: user management, invites, and API-key issuance/revocation.
type AdminHandler struct {
	*common

	adminTmpl *template.Template
}

func newAdminHandler(c *common, adminTmpl *template.Template) *AdminHandler {
	return &AdminHandler{common: c, adminTmpl: adminTmpl}
}

// RegisterRoutes registers the admin panel and its invite/revoke/API-key management routes.
// Every route is wrapped in requireAdmin, so each handler below can assume the context user is
// a signed-in admin instead of re-checking it.
func (h *AdminHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", requireAdmin(h.AdminPage))
	mux.HandleFunc("POST /admin/invite", h.guard(requireAdmin(h.AdminInviteHandler)))
	mux.HandleFunc("POST /admin/users/{id}/revoke", h.guard(requireAdmin(h.AdminRevokeHandler)))
	mux.HandleFunc("POST /admin/api-keys", h.guard(requireAdmin(h.AdminCreateKeyHandler)))
	mux.HandleFunc("POST /admin/api-keys/{id}/revoke", h.guard(requireAdmin(h.AdminRevokeKeyHandler)))
}

// requireAdmin wraps next so it only runs for a request whose context user (resolved by the auth
// middleware, see middleware_auth.go) is a signed-in admin; anonymous or non-admin requests get
// 403. Every admin route uses this instead of repeating the same userFromContext/IsAdmin check.
func requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		usr := userFromContext(r)
		if usr == nil || !usr.IsAdmin {
			http.Error(w, "forbidden", http.StatusForbidden)

			return
		}

		next(w, r)
	}
}

// ── admin panel (GET /admin) ──

type adminViewData struct {
	Nonce       string
	InviteURL   string
	InviteError string
	RevokeError string
	KeyError    string
	NewKey      string
	CSRFToken   string
	Users       []*domain.User
	APITokens   []*auth.APITokenInfo
}

// AdminPage handles GET /admin — lists users and API tokens for admin. requireAdmin (RegisterRoutes)
// already guarantees the context user is a signed-in admin.
func (h *AdminHandler) AdminPage(w http.ResponseWriter, r *http.Request) {
	usr := userFromContext(r)

	data, err := h.adminData(r, usr.ID)
	if err != nil {
		h.log.ErrorContext(r.Context(), "load admin data", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)

		return
	}

	data.RevokeError = r.URL.Query().Get("revoke_error")
	data.KeyError = r.URL.Query().Get("key_error")

	h.renderAdmin(w, r, &data)
}

// ── admin: generate invite (POST /admin/invite) ──

// AdminInviteHandler handles POST /admin/invite — generates a single-use invite link.
// requireAdmin (RegisterRoutes) already guarantees the context user is a signed-in admin.
func (h *AdminHandler) AdminInviteHandler(w http.ResponseWriter, r *http.Request) {
	usr := userFromContext(r)

	data, err := h.adminData(r, usr.ID)
	if err != nil {
		h.log.ErrorContext(r.Context(), "load admin data", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)

		return
	}

	token, genErr := h.authMgr.GenerateInvite(r.Context(), usr.ID)
	if genErr != nil {
		data.InviteError = "Failed to generate invite link."
	} else {
		scheme := h.scheme(r)

		// r.Host is attacker-controllable, but this invite link is only displayed (html/template
		// escaped) for an admin to copy manually — it is not emailed or trusted server-side. If
		// invites ever become email links, validate r.Host against an allowed-hosts allowlist here.
		data.InviteURL = scheme + "://" + r.Host + "/setup?invite=" + url.QueryEscape(token)
	}

	h.renderAdmin(w, r, &data)
}

// ── admin: revoke user (POST /admin/users/{id}/revoke) ──

// AdminRevokeHandler handles POST /admin/users/{id}/revoke — removes a user.
// requireAdmin (RegisterRoutes) already guarantees the context user is a signed-in admin.
func (h *AdminHandler) AdminRevokeHandler(w http.ResponseWriter, r *http.Request) {
	usr := userFromContext(r)

	targetID, parseErr := uuid.Parse(r.PathValue("id"))
	if parseErr != nil {
		// A malformed ID could never have matched a real user anyway — same outcome as a failed
		// RevokeUser call below, just caught one step earlier.
		http.Redirect(w, r, "/admin?revoke_error="+url.QueryEscape("Failed to revoke user."), http.StatusSeeOther)

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

// ── admin: create API key (POST /admin/api-keys) ──

// AdminCreateKeyHandler handles POST /admin/api-keys — issues an API key for the signed-in admin
// and re-renders the admin page with the plain key shown exactly once. The plain key is never
// logged nor placed in a URL: it lives only in the rendered page.
// requireAdmin (RegisterRoutes) already guarantees the context user is a signed-in admin.
func (h *AdminHandler) AdminCreateKeyHandler(w http.ResponseWriter, r *http.Request) {
	usr := userFromContext(r)

	plain, createErr := h.authMgr.CreateAPIToken(r.Context(), usr.ID)

	data, err := h.adminData(r, usr.ID)
	if err != nil {
		h.log.ErrorContext(r.Context(), "load admin data", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)

		return
	}

	switch {
	case createErr != nil:
		h.log.ErrorContext(r.Context(), "create api token", "err", createErr)

		data.KeyError = "Failed to create API key."
		if errors.Is(createErr, domain.ErrAPITokenLimit) {
			data.KeyError = "This user already holds the maximum number of API keys; revoke one first."
		}
	default:
		scheme := h.scheme(r)

		// r.Host is attacker-controllable, but this envelope is only displayed (html/template
		// escaped) for an admin to copy manually — it is never trusted server-side. The server
		// authenticates on the raw key inside the envelope, not on the URL. Same threat model as
		// the invite link above; if that ever changes, validate r.Host against an allowlist here.
		envelope, encErr := domain.EncodeAPITokenEnvelope(scheme+"://"+r.Host, plain)
		if encErr != nil {
			h.log.ErrorContext(r.Context(), "encode api token envelope", "err", encErr)

			data.KeyError = "Failed to create API key."
		} else {
			data.NewKey = envelope
		}
	}

	h.renderAdmin(w, r, &data)
}

// ── admin: revoke API key (POST /admin/api-keys/{id}/revoke) ──

// AdminRevokeKeyHandler handles POST /admin/api-keys/{id}/revoke — deletes an API key by its
// public ID (the token hash) and redirects back to /admin. requireAdmin (RegisterRoutes) already
// guarantees the context user is a signed-in admin.
func (h *AdminHandler) AdminRevokeKeyHandler(w http.ResponseWriter, r *http.Request) {
	usr := userFromContext(r)

	tokenID := r.PathValue("id")
	if tokenID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)

		return
	}

	revokeErr := h.authMgr.RevokeAPIToken(r.Context(), usr.ID, tokenID)
	if revokeErr != nil {
		h.log.ErrorContext(r.Context(), "revoke api token", "err", revokeErr)
		http.Redirect(w, r, "/admin?key_error="+url.QueryEscape("Failed to revoke API key."), http.StatusSeeOther)

		return
	}

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// adminData assembles the shared admin-page view model — signed-in nonce/CSRF plus the user
// and API-token lists. Both lists require admin rights, already verified by the caller.
func (h *AdminHandler) adminData(r *http.Request, adminID uuid.UUID) (adminViewData, error) {
	data := adminViewData{
		Nonce:     nonceFromContext(r.Context()),
		CSRFToken: csrfFromContext(r.Context()),
	}

	users, err := h.authMgr.ListUsers(r.Context(), adminID)
	if err != nil {
		return data, fmt.Errorf("list users: %w", err)
	}

	tokens, err := h.authMgr.ListAPITokens(r.Context(), adminID)
	if err != nil {
		return data, fmt.Errorf("list api tokens: %w", err)
	}

	data.Users = users
	data.APITokens = tokens

	return data, nil
}

// renderAdmin renders the admin template, logging any render error.
func (h *AdminHandler) renderAdmin(w http.ResponseWriter, r *http.Request, data *adminViewData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.adminTmpl.Execute(w, data)
	if err != nil {
		h.log.ErrorContext(r.Context(), "render admin template", "err", err)
	}
}
