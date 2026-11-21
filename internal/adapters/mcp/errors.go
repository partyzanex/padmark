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

// Errors that upload_file and update_file return before a server call. Each text tells the
// model or the operator what to change.
var (
	ErrUploadsOff = errors.New("upload_file is off: the operator must start padmark-mcp with " +
		"--upload-root (env " + EnvUploadRoot + ") set to the directory to share")
	ErrPathOutsideRoot = errors.New("the path is outside the upload root; use a file in the upload root")
	ErrNotRegularFile  = errors.New("the path is not a regular file")
	ErrHiddenPath      = errors.New("the path has a name that starts with a dot; such files often hold " +
		"secrets and upload_file does not read them. Copy the file under a name without the dot and " +
		"set name to the original name")
	ErrUploadTimeout = errors.New("the upload timed out: the server did not answer in time; try again, " +
		"or the operator can raise --" + FlagTimeout)
	errEmptyPath          = errors.New("path must not be empty")
	errNoSuchFile         = errors.New("no such file in the upload root")
	errFileReplaced       = errors.New("the file changed while it was opened; try again")
	errUploadRootNotDir   = errors.New("is not a directory")
	errNoFileChange       = errors.New("nothing to change: set ttl, clear_expiry, privacy or burn_after_download")
	errTTLWithClearExpiry = errors.New("set ttl or clear_expiry, not both")
	errUploadTTLRange     = errors.New("ttl must be 1 to 9223372036 seconds; omit it for no expiry")
	errUpdateTTLRange     = errors.New("ttl must be 0 to 9223372036 seconds; 0 expires the file now")
	errReadLocalFile      = errors.New("read the local file")
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
