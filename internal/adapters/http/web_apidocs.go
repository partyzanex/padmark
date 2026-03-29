//go:generate cp -f ../../../openapi.yaml spec/openapi.yaml
//go:generate ../../../bin/ogen --target ogenapi --package ogenapi --clean ../../../openapi.yaml
//go:generate ../../../bin/ogen --target ../../../pkg/client --package client --clean ../../../openapi.yaml

package http

import (
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
func (h *Handler) APIDocsPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.apidocsTmpl.Execute(w, apiDocsViewData{Nonce: nonceFromContext(r.Context())})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render apidocs template", "err", err)
	}
}

// APISpec handles GET /api/openapi.yaml — returns the raw OpenAPI spec.
func APISpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")

	_, err := w.Write(openapiSpec)
	if err != nil {
		http.Error(w, "failed to write spec", http.StatusInternalServerError)

		return
	}
}
