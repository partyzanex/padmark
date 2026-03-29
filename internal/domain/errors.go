package domain

import "errors"

var (
	ErrNotFound           = errors.New("not found")
	ErrExpired            = errors.New("note has expired")
	ErrTitleRequired      = errors.New("title is required")
	ErrTitleTooLong       = errors.New("title exceeds maximum length")
	ErrContentTooLong     = errors.New("content exceeds maximum length")
	ErrInvalidContentType = errors.New("unsupported content type")
	ErrInvalidSlug        = errors.New("invalid slug: use 1-100 alphanumeric/hyphen/underscore, start with alphanumeric")
	ErrSlugConflict       = errors.New("slug is already taken")
	ErrForbidden          = errors.New("invalid edit code")
)
