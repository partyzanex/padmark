package http

import (
	"net/http"

	_ "embed"
)

//go:embed templates/index.html
var indexTmplSrc string

// IndexPage handles GET / — serves the note editor.
func (h *Handler) IndexPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.indexTmpl.Execute(w, nil)
	if err != nil {
		h.log.ErrorContext(r.Context(), "render index template", "err", err)
	}
}
