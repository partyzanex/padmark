package web

import (
	_ "embed"
	"html/template"
	"net/http"
	"net/url"
	"slices"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
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
	Header  headerView
	ID      string
	Title   string
	Content string
	Nonce   string
	Privacy string
	// ContentType is the stored format of the edited note. The preview shows a text/plain note
	// as plain text, as its page does. Empty for a new note, which the server makes Markdown.
	ContentType      string
	Access           privacyChoice
	TTLOptions       []ttlOption
	EditMode         bool
	BurnAfterReading bool
	IsOwner          bool
	// ShowContents pre-sets the "Contents sidebar" switch: the stored value in edit mode.
	ShowContents bool
	// ContentWithheld marks a burn-after-reading note opened in the editor: its content is not
	// pre-filled (the editor must not read it without burning it), and saving replaces it.
	ContentWithheld bool
}

// privacyOption is one row of the editor's privacy list. Hint, when set, replaces the default
// description of that level. Disabled shows a level that only a signed-in user can choose.
type privacyOption struct {
	Value    string
	Label    string
	Hint     string
	Selected bool
	Disabled bool
}

// privacyChoice is the argument of the "privacy-options" template: the rows of the list and the
// sign-in link that enables the disabled rows.
type privacyChoice struct {
	SignInURL string
	Options   []privacyOption
}

// privacyOptions lists the privacy levels of the editor (review R2). Public is always available.
// Without accounts nothing enforces the other levels, so the list hides them. With accounts a
// signed-out user sees them disabled: the sign-in link of the form enables them. The current
// level of an edited record is always available, so saving without a change keeps it; without
// accounts its hint says it is not enforced.
func privacyOptions(accounts accountMode, signedIn bool, current domain.Privacy) []privacyOption {
	levels := []struct {
		value   domain.Privacy
		label   string
		offered bool
	}{
		{domain.PrivacyPublic, "Public", true},
		{domain.PrivacyAuthenticated, "Login required", accounts == accountsOn && signedIn},
		{domain.PrivacyOwner, "Owner only", accounts == accountsOn && signedIn},
	}

	options := make([]privacyOption, 0, len(levels))

	for index := range levels {
		level := &levels[index]

		selected := level.value == current
		available := level.offered || selected

		if !available && accounts == accountsOff {
			continue
		}

		option := privacyOption{
			Value: string(level.value), Label: level.label, Hint: "", Selected: selected, Disabled: !available,
		}
		if accounts == accountsOff && level.value != domain.PrivacyPublic {
			option.Hint = "Not enforced: accounts are off, anyone with the link can view"
		}

		options = append(options, option)
	}

	return options
}

// newPrivacyChoice returns the privacy list of a form at requestURI. When the list has a
// disabled row, the sign-in link returns the user to requestURI.
func newPrivacyChoice(accounts accountMode, signedIn bool, current domain.Privacy, requestURI string) privacyChoice {
	options := privacyOptions(accounts, signedIn, current)
	choice := privacyChoice{Options: options, SignInURL: ""}

	if slices.ContainsFunc(options, func(option privacyOption) bool { return option.Disabled }) {
		choice.SignInURL = "/login?next=" + url.QueryEscape(requestURI)
	}

	return choice
}

// privacyChoice returns the privacy list of the form that r opens, for the user of header.
func (c *common) privacyChoice(r *http.Request, header headerView, current domain.Privacy) privacyChoice {
	return newPrivacyChoice(c.accounts, header.SignedIn(), current, r.URL.RequestURI())
}

// IndexPage handles GET / — serves the note editor.
func (h *PageHandler) IndexPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	header := h.header(r, headerKindNote)

	err := h.indexTmpl.Execute(w, editorViewData{
		Header:  header,
		Nonce:   middleware.NonceFromContext(r.Context()),
		Privacy: string(domain.PrivacyPublic),
		Access:  h.privacyChoice(r, header, domain.PrivacyPublic),
		// ID/Title/Content blank — fresh editor. EditMode/BurnAfterReading/IsOwner are zero defaults
		// (Create form, not owner view).
		ID:               "",
		Title:            "",
		Content:          "",
		TTLOptions:       burnTTLOptions(0),
		EditMode:         false,
		BurnAfterReading: false,
		IsOwner:          false,
		ShowContents:     false,
		ContentWithheld:  false,
		ContentType:      "",
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render index template", "err", err)
	}
}
