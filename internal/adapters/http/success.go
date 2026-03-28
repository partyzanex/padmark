package http

import (
	"net/http"
	"time"

	_ "embed"
)

//go:embed templates/success.html
var successTmplSrc string

type successViewData struct {
	URL              string
	ExpiresLabel     string
	BurnAfterReading bool
}

// SuccessPage handles GET /success and renders the post-create confirmation page.
// Data is passed via query parameters set by the frontend after a successful POST /notes.
func (h *Handler) SuccessPage(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	id := query.Get("id")
	if id == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}

	noteURL := scheme + "://" + r.Host + "/" + id
	burn := query.Get("burn") == "1"

	expiresLabel := "never expires"

	raw := query.Get("expires")
	if raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err == nil {
			expiresLabel = "expires " + parsed.Format("Jan 2, 2006")
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.successTmpl.Execute(w, successViewData{
		URL:              noteURL,
		ExpiresLabel:     expiresLabel,
		BurnAfterReading: burn,
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render success template", "err", err)
	}
}
