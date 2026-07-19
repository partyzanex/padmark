package http

import (
	"html/template"
	"net/http"

	_ "embed"

	"github.com/partyzanex/padmark/internal/domain"
)

//go:embed templates/index.html
var indexTmplSrc string

// PageHandler serves standalone pages that need no note/auth/admin collaborator beyond
// rendering: the editor landing page, the post-create confirmation page, and the API docs page.
type PageHandler struct {
	*common

	indexTmpl   *template.Template
	successTmpl *template.Template
	apidocsTmpl *template.Template
}

func newPageHandler(c *common, indexTmpl, successTmpl, apidocsTmpl *template.Template) *PageHandler {
	return &PageHandler{common: c, indexTmpl: indexTmpl, successTmpl: successTmpl, apidocsTmpl: apidocsTmpl}
}

// RegisterRoutes registers the editor landing page, post-create confirmation page, API docs
// page, and static asset serving — the routes that need no note/auth/admin collaborator.
func (h *PageHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api", h.APIDocsPage)
	mux.HandleFunc("GET /api/openapi.yaml", APISpec)
	mux.HandleFunc("GET /{$}", h.IndexPage)
	mux.HandleFunc("GET /success", h.SuccessPage)
	mux.Handle("GET /static/", withStaticCacheControl(StaticHandler))
}

// editorViewData is shared by IndexPage (create) and EditPage (edit).
type editorViewData struct {
	ID               string
	Title            string
	Content          string
	Nonce            string
	Privacy          string
	TTL              int64
	EditMode         bool
	BurnAfterReading bool
	IsOwner          bool
}

// IndexPage handles GET / — serves the note editor.
func (h *PageHandler) IndexPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.indexTmpl.Execute(w, editorViewData{
		Nonce:   nonceFromContext(r.Context()),
		Privacy: string(domain.PrivacyPublic),
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render index template", "err", err)
	}
}
