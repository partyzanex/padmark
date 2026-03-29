package http

import (
	"errors"
	"net/http"

	_ "embed"

	"github.com/partyzanex/padmark/internal/domain"
)

//go:embed templates/error.html
var errorTmplSrc string

type errorViewData struct {
	Title     string
	Desc      string
	Detail    string
	ErrorType string // "client" or "server"
	Nonce     string
	Code      int
}

func domainErrToPageData(err error) errorViewData {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return errorViewData{
			Code:      http.StatusNotFound,
			ErrorType: "client",
			Title:     "Paste not found",
			Desc:      "The paste you're looking for doesn't exist, has expired, or was deleted.",
		}
	case errors.Is(err, domain.ErrForbidden):
		return errorViewData{
			Code:      http.StatusForbidden,
			ErrorType: "client",
			Title:     "Forbidden",
			Desc:      "The edit code is invalid or missing.",
		}
	case errors.Is(err, domain.ErrExpired):
		return errorViewData{
			Code:      http.StatusGone,
			ErrorType: "client",
			Title:     "Paste expired",
			Desc:      "This paste had a limited lifetime and has been automatically deleted after expiration.",
		}
	default:
		return errorViewData{
			Code:      http.StatusInternalServerError,
			ErrorType: "server",
			Title:     "Internal server error",
			Desc:      "Something went wrong on our end. Please try again later.",
		}
	}
}

// writeErrorPage renders the HTML error template for browser requests.
func (h *Handler) writeErrorPage(w http.ResponseWriter, r *http.Request, err error) {
	data := domainErrToPageData(err)
	data.Nonce = nonceFromContext(r.Context())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(data.Code)

	tmplErr := h.errorTmpl.Execute(w, data)
	if tmplErr != nil {
		h.log.ErrorContext(r.Context(), "render error template", "err", tmplErr)
	}
}
