package http

import (
	"net/http"

	_ "embed"
)

//go:embed templates/index.html
var indexTmplSrc string

// editorViewData is shared by IndexPage (create) and EditPage (edit).
type editorViewData struct {
	ID               string
	Title            string
	Content          string
	Nonce            string
	TTL              int64 // remaining seconds, for pre-selecting the burn time option
	EditMode         bool
	BurnAfterReading bool
}

// IndexPage handles GET / — serves the note editor.
func (h *Handler) IndexPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.indexTmpl.Execute(w, editorViewData{Nonce: nonceFromContext(r.Context())})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render index template", "err", err)
	}
}
