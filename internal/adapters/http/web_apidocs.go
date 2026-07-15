//go:generate cp -f ../../../openapi.yaml spec/openapi.yaml
//go:generate ../../../bin/ogen --loglevel warn --target ogenapi --package ogenapi --clean ../../../openapi.yaml
//go:generate ../../../bin/ogen --loglevel warn --target ../../../pkg/client --package client --clean ../../../openapi.yaml

package http

import (
	"log/slog"
	"net/http"

	_ "embed"
)

//go:embed spec/openapi.yaml
var openapiSpec []byte

//go:embed templates/apidocs.html
var apidocsTmplSrc string

type apiDocsViewData struct {
	Nonce string
}

// APIDocsPage handles GET /api — renders the OpenAPI spec with Redoc.
// Redoc loads from an external CDN, so this page widens its own CSP to allow that origin in
// script-src; the global CSP (set by withSecurityHeaders) stays free of third-party origins.
func (h *PageHandler) APIDocsPage(w http.ResponseWriter, r *http.Request) {
	nonce := nonceFromContext(r.Context())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", buildCSP(nonce, redocScriptSrc))

	err := h.apidocsTmpl.Execute(w, apiDocsViewData{Nonce: nonce})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render apidocs template", "err", err)
	}
}

// APISpec handles GET /api/openapi.yaml — returns the raw OpenAPI spec.
func APISpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")

	_, err := w.Write(openapiSpec)
	if err != nil {
		// Headers are already sent — changing the response is impossible.
		// Only log the error; calling http.Error here would corrupt the body.
		slog.Error("write openapi spec", "err", err)
	}
}
