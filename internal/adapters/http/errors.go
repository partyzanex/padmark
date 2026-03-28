package http

import (
	"errors"
	"net/http"

	"github.com/partyzanex/padmark/internal/domain"
)

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, domain.ErrTitleRequired), errors.Is(err, domain.ErrInvalidContentType):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, domain.ErrContentTooLong):
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
	default:
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}
