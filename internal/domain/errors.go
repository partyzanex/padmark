package domain

import "errors"

var (
	ErrNotFound           = errors.New("not found")
	ErrTitleRequired      = errors.New("title is required")
	ErrContentTooLong     = errors.New("content exceeds maximum length")
	ErrInvalidContentType = errors.New("unsupported content type")
)
