package http

import (
	"errors"
	"net/http"

	"github.com/partyzanex/padmark/internal/domain"
)

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		http.Error(w, domain.ErrNotFound.Error(), http.StatusNotFound)
	case errors.Is(err, domain.ErrExpired):
		http.Error(w, domain.ErrExpired.Error(), http.StatusGone)
	case errors.Is(err, domain.ErrTitleRequired):
		http.Error(w, domain.ErrTitleRequired.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, domain.ErrTitleTooLong):
		http.Error(w, domain.ErrTitleTooLong.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, domain.ErrInvalidContentType):
		http.Error(w, domain.ErrInvalidContentType.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, domain.ErrContentTooLong):
		http.Error(w, domain.ErrContentTooLong.Error(), http.StatusRequestEntityTooLarge)
	case errors.Is(err, domain.ErrInvalidSlug):
		http.Error(w, domain.ErrInvalidSlug.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, domain.ErrSlugConflict):
		http.Error(w, domain.ErrSlugConflict.Error(), http.StatusConflict)
	case errors.Is(err, domain.ErrForbidden):
		http.Error(w, domain.ErrForbidden.Error(), http.StatusForbidden)
	default:
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}
