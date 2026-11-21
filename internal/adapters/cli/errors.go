package cli

import (
	"errors"
	"fmt"
)

// ErrForbidden identifies an edit-code or ownership rejection by the server.
var ErrForbidden = errors.New("forbidden")

// ErrNotFound identifies a missing note or short link.
var ErrNotFound = errors.New("not found")

// Errors that the CLI commands return. Callers match the exported values with errors.Is.
var (
	ErrAPIDisabled = errors.New("API is disabled on this server " +
		"(api_disabled); ask the operator to enable it")
	ErrEmptyContent          = errors.New("content must not be empty")
	ErrFileGone              = errors.New("file has expired or was already downloaded")
	ErrFileNotFound          = fmt.Errorf("file %w", ErrNotFound)
	ErrFileTooLarge          = errors.New("file is larger than the server limit")
	ErrFilesDisabled         = fmt.Errorf("%w: file publishing is off on the server", ErrServiceUnavailable)
	ErrLinkGone              = errors.New("link has expired")
	ErrLinkNotFound          = fmt.Errorf("link %w", ErrNotFound)
	ErrNoteGone              = errors.New("note has expired or was already consumed")
	ErrNoteNotFound          = fmt.Errorf("note %w", ErrNotFound)
	ErrRateLimited           = errors.New("rate limit exceeded, try again later")
	ErrUnauthorized          = errors.New("unauthorized")
	errPrivateConflict       = errors.New("--private conflicts with --privacy; use --privacy only")
	errContentRequired       = errors.New("no content provided: pass --content, --file, or pipe content via stdin")
	errFileIDRequired        = errors.New("file ID or URL is required as the first argument")
	errFileOwnershipRequired = fmt.Errorf("%w: wrong edit code or --token is not the file's owner", ErrForbidden)
	errFilePathRequired      = errors.New("file path is required as the first argument; use - for stdin")
	errFileURL               = errors.New("URL is not a file address; use /{id}, /files/{id} or /files/{id}/download")
	errFileURLOrigin         = errors.New("file URL is not on the configured server")
	errInvalidFileID         = errors.New(`file ID must not be "." or ".."`)
	errTTLTooLarge           = fmt.Errorf("--ttl must be at most %d seconds", maxTTLSeconds)
	errNoFileChange          = errors.New("nothing to change: pass --ttl, --clear-expiry, --privacy or --burn")
	errNotRegularFile        = errors.New("not a regular file")
	errOutputExists          = errors.New("destination exists; pass --force to replace it")
	errOutputIsDirectory     = errors.New("destination is a directory; pass --output with a file path")
	errStdinNameRequired     = errors.New("--name is required when the file comes from stdin")
	errStdinTerminal         = errors.New("stdin is a terminal; pipe the file content or pass a file path")
	errTTLConflict           = errors.New("--ttl conflicts with --clear-expiry; use one of them")
	errUnsafeFileName        = errors.New("server file name is not safe as a local name; pass --output")
	errLinkIDRequired        = errors.New("link ID is required as the first argument")
	errLinkOwnershipRequired = fmt.Errorf("%w: wrong edit code or --token is not the link's owner", ErrForbidden)
	errNoTrustTarget         = errors.New(
		"nothing to trust: the token is not a pmk_ envelope and no --url was given")
	errNonPositiveTTL             = errors.New("--ttl must be a positive number of seconds")
	errNoteIDRequired             = errors.New("note ID is required as the first argument")
	errNoteOwnershipRequired      = fmt.Errorf("%w: wrong edit code or --token is not the note's owner", ErrForbidden)
	errNoteReadCredentialRequired = fmt.Errorf("%w: authenticate with a credential allowed to "+
		"read this note", ErrUnauthorized)
	errSetupMissingOTPURI = errors.New("setup succeeded but the response carries no otpauth_uri")
	errUsernameRequired   = errors.New("--username is required")
	errWrongEditCode      = fmt.Errorf("%w: wrong edit code", ErrForbidden)
)

// Errors that the CLI maps from the server response variants. Callers match them with errors.Is.
var (
	ErrBadRequest          = errors.New("bad request")
	ErrBurnReadRefused     = errors.New("refused")
	ErrContentTooLarge     = errors.New("content too large")
	ErrInvalidRequest      = errors.New("invalid request")
	ErrRequestTooLarge     = errors.New("request too large")
	ErrServer              = errors.New("server error")
	ErrServiceUnavailable  = errors.New("service unavailable")
	ErrSetupFailed         = errors.New("setup failed")
	ErrSlugConflict        = errors.New("slug conflict")
	ErrStepUpRefused       = errors.New("step-up refused")
	ErrTokenLimit          = errors.New("token cap reached (revoke one via /admin)")
	ErrUnexpectedResponse  = errors.New("unexpected response type")
	ErrUntrustedServer     = errors.New("token pins server")
	ErrValidation          = errors.New("validation error")
	errAdminStepUpRequired = fmt.Errorf("%w (admin and a valid step-up required)", ErrForbidden)
	errLinkTTL             = errors.New("must be a positive number of seconds (omit for no expiry)")
)
