package web

import (
	_ "embed"
	"net/http"
	"time"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
	"github.com/partyzanex/padmark/internal/domain"
)

//go:embed templates/success.html
var successTmplSrc string

type successViewData struct {
	ID           string
	URL          string
	ExpiresLabel string
	Privacy      string
	Nonce        string
	// FileName and SizeLabel describe an uploaded file; they are empty for notes and links.
	FileName         string
	SizeLabel        string
	Header           headerView
	BurnAfterReading bool
	IsLink           bool
	IsFile           bool
}

// SuccessPage handles GET /success and renders the post-create confirmation page.
// Data is passed via query parameters set by the frontend after a successful POST /notes.
// For a file (kind=file) the page reads the name, size and settings from the file record
// instead, so that the name does not go into the URL.
func (h *PageHandler) SuccessPage(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	id := query.Get("id")
	if id == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)

		return
	}

	scheme := h.scheme(r)

	// r.Host may be attacker-controlled (Host header), but this URL is only rendered into the
	// page via html/template (escaped, no injection) for the user to copy — it is never emailed
	// or used server-side. Add allowed-host validation here if absolute links ever leave the page.
	// Notes, short links and files share one slug namespace, so all answer at /{id}.
	noteURL := scheme + "://" + r.Host + "/" + id

	isLink := query.Get("kind") == "link"
	isFile := query.Get("kind") == "file"

	// burnLabel is the expiry text of a burn resource without a lifetime.
	burnLabel := "burns immediately after reading"
	if isLink {
		burnLabel = "burns after the first click"
	}

	burn := query.Get("burn") == "1"

	// The privacy level only selects the page's wording; an unknown value shows the public text.
	privacy := domain.Privacy(query.Get("privacy"))
	if !privacy.Valid() {
		privacy = domain.PrivacyPublic
	}

	data := successViewData{
		Header:           h.header(r, headerKindView),
		ID:               id,
		URL:              noteURL,
		ExpiresLabel:     successExpiresLabel(query.Get("expires"), burnLabel),
		Privacy:          string(privacy),
		BurnAfterReading: burn,
		Nonce:            middleware.NonceFromContext(r.Context()),
		IsLink:           isLink,
		IsFile:           isFile,
		FileName:         "",
		SizeLabel:        "",
	}
	if isFile {
		h.fileSuccess(r, id, &data)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.successTmpl.Execute(w, data)
	if err != nil {
		h.log.ErrorContext(r.Context(), "render success template", "err", err)
	}
}

// successExpiresLabel is the expiry text of the success page for the "expires" query value.
// burnLabel is the text for "immediately".
func successExpiresLabel(raw, burnLabel string) string {
	switch {
	case raw == "immediately":
		return burnLabel
	case raw != "":
		parsed, err := time.Parse(time.RFC3339, raw)
		if err == nil {
			return "expires " + parsed.Format("Jan 2, 2006")
		}
	default:
		// No expiry keeps the "never expires" label.
	}

	return "never expires"
}
