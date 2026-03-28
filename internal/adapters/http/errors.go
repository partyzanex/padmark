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
	case errors.Is(err, domain.ErrTitleRequired):
		http.Error(w, domain.ErrTitleRequired.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, domain.ErrInvalidContentType):
		http.Error(w, domain.ErrInvalidContentType.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, domain.ErrContentTooLong):
		http.Error(w, domain.ErrContentTooLong.Error(), http.StatusRequestEntityTooLarge)
	default:
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}
