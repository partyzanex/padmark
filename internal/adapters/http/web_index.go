package http

import (
	_ "embed"
	"html/template"
	"net/http"

	"github.com/partyzanex/padmark/internal/adapters/http/middleware"
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
// page, the llms.txt agent surface, and static asset serving — the routes that need no
// note/auth/admin collaborator.
func (h *PageHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api", h.APIDocsPage)
	mux.HandleFunc("GET /api/openapi.yaml", APISpec(h.log))
	mux.HandleFunc("GET /api/reference.md", APIReference(h.log))
	mux.HandleFunc("GET /llms.txt", LLMsTxt(h.log))
	// HTML pages declare /static/favicon.svg; a raw text/markdown view cannot, so the browser asks
	// for /favicon.ico and would otherwise get a 404 from the note route.
	mux.Handle("GET "+middleware.FaviconPath, http.RedirectHandler("/static/favicon.svg", http.StatusMovedPermanently))
	mux.HandleFunc("GET /{$}", h.IndexPage)
	mux.HandleFunc("GET /success", h.SuccessPage)
	mux.Handle("GET /static/", middleware.WithStaticCacheControl(StaticHandler))
}

// editorViewData is shared by IndexPage (create) and EditPage (edit).
type editorViewData struct {
	ID               string
	Title            string
	Content          string
	Nonce            string
	Privacy          string
	PrivacyOptions   []privacyOption
	TTLOptions       []ttlOption
	EditMode         bool
	BurnAfterReading bool
	IsOwner          bool
	// ContentWithheld marks a burn-after-reading note opened in the editor: its content is not
	// pre-filled (the editor must not read it without burning it), and saving replaces it.
	ContentWithheld bool
}

// privacyOption is one entry of the editor's privacy select. Hint, when set, replaces the
// default hint text for that level.
type privacyOption struct {
	Value    string
	Label    string
	Hint     string
	Selected bool
}

// privacyOptions lists the privacy levels the editor offers (review R2): public always,
// authenticated only with the account system (otherwise nothing enforces it), owner only to a
// signed-in user (an anonymous note has no owner). The current level of an edited note is always
// listed, so saving without a change keeps it; without accounts its hint says it is not enforced.
//
//nolint:revive // accounts is the --enable-accounts setting that selects the offered levels
func privacyOptions(accounts, signedIn bool, current domain.Privacy) []privacyOption {
	levels := []struct {
		value   domain.Privacy
		label   string
		offered bool
	}{
		{domain.PrivacyPublic, "Public", true},
		{domain.PrivacyAuthenticated, "Requires login", accounts},
		{domain.PrivacyOwner, "Owner only", accounts && signedIn},
	}

	options := make([]privacyOption, 0, len(levels))

	for index := range levels {
		level := &levels[index]

		selected := level.value == current
		if !level.offered && !selected {
			continue
		}

		option := privacyOption{Value: string(level.value), Label: level.label, Hint: "", Selected: selected}
		if !accounts && level.value != domain.PrivacyPublic {
			option.Hint = "Not enforced: accounts are off, anyone with the link can view"
		}

		options = append(options, option)
	}

	return options
}

// IndexPage handles GET / — serves the note editor.
func (h *PageHandler) IndexPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.indexTmpl.Execute(w, editorViewData{
		Nonce:          middleware.NonceFromContext(r.Context()),
		Privacy:        string(domain.PrivacyPublic),
		PrivacyOptions: privacyOptions(h.authMgr != nil, h.signedIn(r), domain.PrivacyPublic),
		// ID/Title/Content blank — fresh editor. EditMode/BurnAfterReading/IsOwner are zero defaults
		// (Create form, not owner view).
		ID:               "",
		Title:            "",
		Content:          "",
		TTLOptions:       burnTTLOptions(0),
		EditMode:         false,
		BurnAfterReading: false,
		IsOwner:          false,
		ContentWithheld:  false,
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render index template", "err", err)
	}
}
