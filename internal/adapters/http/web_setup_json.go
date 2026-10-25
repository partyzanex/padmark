package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/partyzanex/padmark/internal/adapters/http/response"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/auth"
)

// setupJSONRequest is the machine-readable body of the JSON branch of POST /setup (headless
// provisioning; an empty Invite bootstraps the first admin, a non-empty one enrolls a new user).
type setupJSONRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Invite   string `json:"invite"`
	// BootstrapToken proves ownership of the server when creating the first admin (no invite).
	BootstrapToken string `json:"bootstrap_token"`
}

// setupJSONResponse is returned once with the enrollment URI. The TOTP secret inside the URI
// is the only copy — it is stored sealed server-side and cannot be retrieved later.
type setupJSONResponse struct {
	OTPAuthURI string `json:"otp_auth_uri"`
	Username   string `json:"username"`
	IsAdmin    bool   `json:"is_admin"`
}

// setupJSONStatus maps a setup failure onto a machine-readable status. The table mirrors
// setupErrMsg for the HTML branch but stays handler-local: these sentinels are deliberately not
// routed through domainErrStatus, because /setup is a web route outside the ogen contract.
func setupJSONStatus(err error) int {
	switch {
	case errors.Is(err, domain.ErrUserExists):
		return http.StatusConflict
	case errors.Is(err, domain.ErrWeakPassword), errors.Is(err, domain.ErrInvalidUsername):
		return http.StatusUnprocessableEntity
	case errors.Is(err, domain.ErrInviteExpired), errors.Is(err, domain.ErrInviteUsed):
		return http.StatusGone
	case errors.Is(err, domain.ErrForbidden):
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}

// writeJSONStatus writes a 201-style success body with the ErrorJSON-family wire format.
func (h *AuthHandler) writeJSONStatus(w http.ResponseWriter, r *http.Request, status int, body any) {
	w.Header().Set("Content-Type", mimeJSON)
	w.WriteHeader(status)

	encErr := json.NewEncoder(w).Encode(body)
	if encErr != nil {
		h.log.ErrorContext(r.Context(), "write response", "err", encErr)
	}
}

// setupJSON serves the JSON branch of POST /setup: headless provisioning returns the enrollment
// otpauth URI instead of a QR page. The usecase calls are the same as the HTML form; only the
// body (JSON, not form fields), the response and the bootstrap-gate rendering differ — setup
// without an invite answers 403 JSON once any account exists.
func (h *AuthHandler) setupJSON(w http.ResponseWriter, r *http.Request) {
	if h.authMgr == nil {
		http.Error(w, "not found", http.StatusNotFound)

		return
	}

	const maxBody = 4096

	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	var req setupJSONRequest

	decErr := json.NewDecoder(r.Body).Decode(&req)
	if decErr != nil {
		response.WriteJSONErrorStatus(w, r, h.log, http.StatusBadRequest, decodeErrorMessage)

		return
	}

	if strings.TrimSpace(req.Username) == "" {
		response.WriteJSONErrorStatus(w, r, h.log, http.StatusBadRequest, "username is required")

		return
	}

	// Empty username/password validation beyond that is left to the usecase: ValidatePassword
	// is authoritative (422 below), and a missing password is just a weak one.
	if req.Invite == "" && !h.setupGateOpenJSON(w, r, req.BootstrapToken) {
		return
	}

	var (
		enrollment *auth.Enrollment
		setupErr   error
	)

	if req.Invite != "" {
		enrollment, setupErr = h.authMgr.AcceptInvite(r.Context(), req.Invite, req.Username, req.Password)
	} else {
		enrollment, setupErr = h.authMgr.AcceptFirstAdmin(r.Context(), req.Username, req.Password)
	}

	if setupErr != nil {
		h.setupJSONFailure(w, r, setupErr)

		return
	}

	h.writeJSONStatus(w, r, http.StatusCreated, setupJSONResponse{
		OTPAuthURI: enrollment.TOTPURI,
		Username:   req.Username,
		IsAdmin:    req.Invite == "",
	})
}

// Stable `code` values of the first-admin bootstrap refusals.
const (
	bootstrapDisabledCode     = "bootstrap_disabled"
	bootstrapTokenInvalidCode = "bootstrap_token_invalid"
)

// setupGateOpenJSON answers the bootstrap gate (no users yet) as JSON: 500 on lookup failure,
// 403 with the invite hint when the first admin already exists, and 403 when the bootstrap token
// is not configured on the server or presented does not match it.
func (h *AuthHandler) setupGateOpenJSON(w http.ResponseWriter, r *http.Request, presented string) bool {
	empty, gateErr := h.authMgr.IsEmpty(r.Context())
	if gateErr != nil {
		h.log.ErrorContext(r.Context(), "check empty", "err", gateErr)
		response.WriteJSONErrorStatus(w, r, h.log, http.StatusInternalServerError, internalErrorMessage)

		return false
	}

	if !empty {
		response.WriteJSONErrorStatus(w, r, h.log, http.StatusForbidden,
			"The first admin already exists. New accounts require an invite.")

		return false
	}

	if len(h.bootstrapDigest) == 0 {
		response.WriteJSONErrorCode(w, r, h.log, http.StatusForbidden,
			"First-admin setup is disabled: set PADMARK_BOOTSTRAP_TOKEN (at least 32 characters) "+
				"on the server and send it as bootstrap_token.", bootstrapDisabledCode)

		return false
	}

	if !h.bootstrapTokenValid(presented) {
		response.WriteJSONErrorCode(w, r, h.log, http.StatusForbidden, "Invalid bootstrap token.",
			bootstrapTokenInvalidCode)

		return false
	}

	return true
}

// setupJSONFailure maps a setup failure onto its machine-readable status; internal errors are
// logged and collapsed into the fixed message, mirroring the HTML branch's behaviour.
func (h *AuthHandler) setupJSONFailure(w http.ResponseWriter, r *http.Request, setupErr error) {
	status := setupJSONStatus(setupErr)
	msg := setupErrMsg(setupErr)

	if status == http.StatusInternalServerError {
		h.log.ErrorContext(r.Context(), "setup", "err", setupErr)

		msg = internalErrorMessage
	}

	response.WriteJSONErrorStatus(w, r, h.log, status, msg)
}
