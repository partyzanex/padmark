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
		return domainErrToDefaultPageData(err)
	}
}

// domainErrToDefaultPageData handles every domain error without page-specific copy above (today:
// the create/update validation errors — title/content-type/slug/conflict — which currently only
// ever occur on JSON-only endpoints) plus genuinely unknown errors. It still classifies via
// domainErrStatus rather than defaulting everything to 500, so a future HTML-rendering path that
// happens to surface one of these shows the correct 4xx status instead of silently becoming an
// "internal server error" page.
func domainErrToDefaultPageData(err error) errorViewData {
	status, message := domainErrStatus(err)
	if message == "" {
		return errorViewData{
			Code:      http.StatusInternalServerError,
			ErrorType: errorTypeServer,
			Title:     "Internal server error",
			Desc:      "Something went wrong on our end. Please try again later.",
		}
	}

	return errorViewData{
		Code:      status,
		ErrorType: errorTypeClient,
		Title:     "Request rejected",
		Desc:      message,
	}
}

// writeErrorPage renders the HTML error template for browser requests.
func (h *common) writeErrorPage(w http.ResponseWriter, r *http.Request, err error) {
	data := domainErrToPageData(err)
	h.writeErrorPageData(w, r, &data)
}

// writeNoteError renders an error respecting content negotiation: browsers get the HTML error
// page, while API/CLI clients (JSON or plain) get a machine-readable status via writeError. Used
// on the note-view path, where handlePrivateAuth runs before the format switch and so must not
// assume HTML — otherwise a CLI reading e.g. an already-burned note receives an undecodable
// text/html body instead of a clean 404.
func (h *common) writeNoteError(w http.ResponseWriter, r *http.Request, err error) {
	if negotiate(r) == formatHTML {
		h.writeErrorPage(w, r, err)

		return
	}

	h.writeError(w, r, err)
}

// writeErrorPageData renders the HTML error template from explicit view data,
// for cases that need a tailored title/description rather than a domain sentinel.
func (h *common) writeErrorPageData(w http.ResponseWriter, r *http.Request, data *errorViewData) {
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
