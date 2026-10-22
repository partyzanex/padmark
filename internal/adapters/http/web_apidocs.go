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

// redocVersion is the cache-busting query for the vendored Redoc bundle (see
// static/redoc.standalone.js.LICENSE.txt for its provenance), mirroring styleVersion.
//
//nolint:gochecknoglobals // computed once from embedded static content
var redocVersion = staticAssetVersion("static/redoc.standalone.js")

type apiDocsViewData struct {
	Nonce        string
	RedocVersion string
}

// APIDocsPage handles GET /api — renders the OpenAPI spec with Redoc.
// Redoc is vendored under /static/ and served from the app origin, so the page runs under the
// unmodified global CSP set by withSecurityHeaders — no third-party script origin is allowed.
func (h *PageHandler) APIDocsPage(w http.ResponseWriter, r *http.Request) {
	nonce := nonceFromContext(r.Context())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.apidocsTmpl.Execute(w, apiDocsViewData{Nonce: nonce, RedocVersion: redocVersion})
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
