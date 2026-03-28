package domain

import "errors"

var (
	ErrNotFound           = errors.New("not found")
	ErrExpired            = errors.New("note has expired")
	ErrTitleRequired      = errors.New("title is required")
	ErrContentTooLong     = errors.New("content exceeds maximum length")
	ErrInvalidContentType = errors.New("unsupported content type")
	ErrInvalidSlug        = errors.New("invalid slug: 1-100 alphanumeric/hyphen/underscore chars, must start with alphanumeric")
	ErrSlugConflict       = errors.New("slug is already taken")
)
