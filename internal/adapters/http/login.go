package http

import (
	"net/http"

	_ "embed"
)

//go:embed templates/login.html
var loginTmplSrc string

type loginViewData struct {
	Error bool
}

// LoginPage handles GET /login — renders the token input form.
func (h *Handler) LoginPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.loginTmpl.Execute(w, loginViewData{
		Error: r.URL.Query().Get("error") == "1",
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render login template", "err", err)
	}
}
