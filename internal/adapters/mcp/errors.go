package mcp

import (
	"errors"
)

// Errors that the MCP tools return for a disabled API, invalid input and server failures.
var (
	ErrAPIDisabled  = errors.New("API is disabled on this server (api_disabled); ask the operator to enable it")
	ErrEmptyContent = errors.New("content must not be empty")
	ErrServer       = errors.New("padmark server error — check the server's logs")
	ErrSlugConflict = errors.New("conflict: the slug is already in use — pick " +
		"another slug or omit it to let the server " +
		"generate one")
	ErrUnsupportedContentType = errors.New("unsupported content type — send JSON (Content-Type: application/json)")
	errEmptyTargetURLs        = errors.New("target_urls must contain at least one URL")
	errNoTrustTarget          = errors.New("nothing to trust: the token is not a pmk_ " +
		"envelope and no --base-url was given")
)

// Errors that the MCP tools map from the server response variants.
var (
	ErrBadRequest         = errors.New("bad request")
	ErrBurnReadRefused    = errors.New("refused")
	ErrContentTooLarge    = errors.New("content too large")
	ErrForbidden          = errors.New("forbidden")
	ErrGone               = errors.New("gone")
	ErrNotFound           = errors.New("not found")
	ErrRateLimited        = errors.New("too many requests")
	ErrRejected           = errors.New("rejected")
	ErrServiceUnavailable = errors.New("service unavailable")
	ErrUnauthorized       = errors.New("unauthorized")
	ErrUnexpectedResponse = errors.New("unexpected response type")
	ErrUntrustedServer    = errors.New("token pins server")
	errInvalidTargetURL   = errors.New("is not a valid URL (http/https required)")
	errTooManyTargets     = errors.New("target_urls exceeds")
)
