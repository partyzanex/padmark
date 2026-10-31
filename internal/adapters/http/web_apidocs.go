//go:generate cp -f ../../../openapi.yaml spec/openapi.yaml
//go:generate ../../../bin/ogen --loglevel warn --target ogenapi --package ogenapi --clean ../../../openapi.yaml
//go:generate ../../../bin/ogen --loglevel warn --target ../../../pkg/client --package client --clean ../../../openapi.yaml

package http

import (
	_ "embed"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"

	"github.com/partyzanex/padmark/internal/adapters/http/middleware"
	"github.com/partyzanex/padmark/internal/buildinfo"
)

//go:embed spec/openapi.yaml
var openapiSpec []byte

//nolint:gochecknoglobals // render the embedded contract once for this build
var releaseOpenAPISpec = []byte(regexp.MustCompile(`(?m)^ {2}version: .+$`).ReplaceAllLiteralString(
	string(openapiSpec), "  version: "+strconv.Quote(buildinfo.Version()),
))

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
// unmodified global CSP set by WithSecurityHeaders — no third-party script origin is allowed.
func (h *PageHandler) APIDocsPage(w http.ResponseWriter, r *http.Request) {
	nonce := middleware.NonceFromContext(r.Context())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.apidocsTmpl.Execute(w, apiDocsViewData{Nonce: nonce, RedocVersion: redocVersion})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render apidocs template", "err", err)
	}
}

// APISpec returns the handler for GET /api/openapi.yaml, which serves the raw OpenAPI spec.
func APISpec(log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")

		_, err := w.Write(releaseOpenAPISpec)
		if err != nil {
			// Headers are already sent — changing the response is impossible.
			// Only log the error; calling http.Error here would corrupt the body.
			log.ErrorContext(r.Context(), "write openapi spec", "err", err)
		}
	}
}
