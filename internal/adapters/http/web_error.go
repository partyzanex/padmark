package http

import (
	"errors"
	"net/http"

	_ "embed"

	"github.com/partyzanex/padmark/internal/domain"
)

//go:embed templates/error.html
var errorTmplSrc string

const (
	errorTypeClient = "client"
	errorTypeServer = "server"
)

type errorViewData struct {
	Title     string
	Desc      string
	Detail    string
	ErrorType string // errorTypeClient or errorTypeServer
	Nonce     string
	Code      int
}

func domainErrToPageData(err error) errorViewData {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return errorViewData{
			Code:      http.StatusNotFound,
			ErrorType: errorTypeClient,
			Title:     "Paste not found",
			Desc:      "The paste you're looking for doesn't exist, has expired, or was deleted.",
		}
	case errors.Is(err, domain.ErrInvalidEditCode):
		return errorViewData{
			Code:      http.StatusForbidden,
			ErrorType: errorTypeClient,
			Title:     "Forbidden",
			Desc:      "The edit code is invalid or missing.",
		}
	case errors.Is(err, domain.ErrForbidden):
		return errorViewData{
			Code:      http.StatusForbidden,
			ErrorType: errorTypeClient,
			Title:     "Forbidden",
			Desc:      "You don't have permission to perform this action.",
		}
	case errors.Is(err, domain.ErrExpired):
		return errorViewData{
			Code:      http.StatusGone,
			ErrorType: errorTypeClient,
			Title:     "Paste expired",
			Desc:      "This paste had a limited lifetime and has been automatically deleted after expiration.",
		}
	default:
		return errorViewData{
			Code:      http.StatusInternalServerError,
			ErrorType: errorTypeServer,
			Title:     "Internal server error",
			Desc:      "Something went wrong on our end. Please try again later.",
		}
	}
}

// writeErrorPage renders the HTML error template for browser requests.
func (h *Handler) writeErrorPage(w http.ResponseWriter, r *http.Request, err error) {
	data := domainErrToPageData(err)
	h.writeErrorPageData(w, r, &data)
}

// writeErrorPageData renders the HTML error template from explicit view data,
// for cases that need a tailored title/description rather than a domain sentinel.
func (h *Handler) writeErrorPageData(w http.ResponseWriter, r *http.Request, data *errorViewData) {
	data.Nonce = nonceFromContext(r.Context())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(data.Code)

	tmplErr := h.errorTmpl.Execute(w, data)
	if tmplErr != nil {
		h.log.ErrorContext(r.Context(), "render error template", "err", tmplErr)
	}
}

// setupClosedPageData is the error shown when /setup is hit without a valid invite
// after the first admin already exists (the bootstrap endpoint is closed).
func setupClosedPageData() errorViewData {
	return errorViewData{
		Code:      http.StatusForbidden,
		ErrorType: errorTypeClient,
		Title:     "Setup is closed",
		Desc:      "The first admin already exists. New accounts can only be created via an invite link.",
	}
}
