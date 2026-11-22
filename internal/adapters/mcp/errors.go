package mcp

import (
	"errors"
)

// The errors of this package are unexported: no other package matches them. The tool result
// carries their text, and each text tells the model or the operator what to change.

// Errors that the tools return for a disabled API, invalid input and server failures.
var (
	errAPIDisabled  = errors.New("API is disabled on this server (api_disabled); ask the operator to enable it")
	errEmptyContent = errors.New("content must not be empty")
	errServer       = errors.New("padmark server error — check the server's logs")
	errSlugConflict = errors.New("conflict: the slug is already in use — pick " +
		"another slug or omit it to let the server " +
		"generate one")
	errUnsupportedContentType = errors.New("unsupported content type — send JSON (Content-Type: application/json)")
	errEmptyTargetURLs        = errors.New("target_urls must contain at least one URL")
)

// Errors that upload_file and update_file return before a server call.
var (
	errUploadsOff = errors.New("upload_file is off: the operator must start padmark-mcp with " +
		"--upload-root (env " + EnvUploadRoot + ") set to the directory to share")
	errPathOutsideRoot = errors.New("the path is outside the upload root; use a file in the upload root")
	errNotRegularFile  = errors.New("the path is not a regular file")
	errHiddenPath      = errors.New("the path has a name that starts with a dot; such files often hold " +
		"secrets and upload_file does not read them. Copy the file under a name without the dot and " +
		"set name to the original name")
	errUploadTimeout = errors.New("the upload timed out: the server did not answer in time; try again, " +
		"or the operator can raise --" + FlagTimeout)
	errEmptyPath          = errors.New("path must not be empty")
	errNoSuchFile         = errors.New("no such file in the upload root")
	errFileReplaced       = errors.New("the file changed while it was opened; try again")
	errUploadRootNotDir   = errors.New("is not a directory")
	errNoFileChange       = errors.New("nothing to change: set ttl, clear_expiry, privacy or burn_after_download")
	errTTLWithClearExpiry = errors.New("set ttl or clear_expiry, not both")
	errUploadTTLRange     = errors.New("ttl must be 1 to 9223372036 seconds; omit it for no expiry")
	errUpdateTTLRange     = errors.New("ttl must be 0 to 9223372036 seconds; 0 expires the file now")
	errLinkTTLRange       = errors.New("ttl must be 0 to 9223372036 seconds; 0 expires the link now")
	errReadLocalFile      = errors.New("read the local file")
)

// Errors that the tools map from the error responses of the server.
var (
	errBadRequest         = errors.New("bad request")
	errBurnReadRefused    = errors.New("refused")
	errContentTooLarge    = errors.New("content too large")
	errForbidden          = errors.New("forbidden")
	errGone               = errors.New("gone")
	errNotFound           = errors.New("not found")
	errRateLimited        = errors.New("too many requests")
	errServiceUnavailable = errors.New("service unavailable")
	errUnauthorized       = errors.New("unauthorized")
	errUnexpectedResponse = errors.New("unexpected response type")
	// errValidation is the error of 422. padmark-cli uses the same text for the same status.
	errValidation       = errors.New("validation error")
	errInvalidTargetURL = errors.New("is not a valid URL (http/https required)")
	errTooManyTargets   = errors.New("target_urls exceeds")
)
