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
	Nonce            string
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
	if isHTTPS(r, h.trustedProxies) {
		scheme = protoHTTPS
	}

	// r.Host may be attacker-controlled (Host header), but this URL is only rendered into the
	// page via html/template (escaped, no injection) for the user to copy — it is never emailed
	// or used server-side. Add allowed-host validation here if absolute links ever leave the page.
	noteURL := scheme + "://" + r.Host + "/" + id
	burn := query.Get("burn") == "1"

	expiresLabel := "never expires"

	raw := query.Get("expires")
	switch {
	case raw == "immediately":
		expiresLabel = "burns immediately after reading"
	case raw != "":
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
		Nonce:            nonceFromContext(r.Context()),
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render success template", "err", err)
	}
}
