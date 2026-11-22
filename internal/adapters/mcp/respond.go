package mcp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/partyzanex/padmark/internal/errorcontext"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

// The tools map each error response to one error text in this file. pkg/client gives an
// *padmark.APIError for each error variant of a generated operation (ResponseError) and for an
// error status of UploadFile. Thus one table per kind of record covers all tools, and a tool
// names only the statuses where its text differs. Each text carries the remediation that the
// model needs. transportError wraps the HTTP transport errors in the handlers.

// statusRule returns the tool error for an error response.
type statusRule func(apiErr *padmark.APIError) error

// statusRules maps an HTTP status to its rule.
type statusRules map[int]statusRule

// recordKind holds the tool errors that depend on the kind of record.
type recordKind struct {
	// gone and tooLarge map 410 and 413.
	gone     statusRule
	tooLarge statusRule
	// unavailable maps 503: an operator-disabled API, or the file storage, or a failure.
	unavailable statusRule
	// name is the record in the error texts: note, link or file.
	name string
}

func noteRecord() recordKind {
	return recordKind{
		gone: destroyedAfterRead("note"), tooLarge: contentTooLarge, unavailable: serviceUnavailableError,
		name: "note",
	}
}

func linkRecord() recordKind {
	return recordKind{
		gone: destroyedAfterRead("link"), tooLarge: contentTooLarge, unavailable: serviceUnavailableError,
		name: "link",
	}
}

func fileRecord() recordKind {
	return recordKind{
		gone: withDetails(errGone), tooLarge: withDetails(errContentTooLarge), unavailable: fileUnavailableError,
		name: "file",
	}
}

// operation is one server call of a tool.
type operation struct {
	// context wraps each error of the call, for example "decode create note response".
	context string
	// rateScope names the limited calls in the text of 429, for example "note updates".
	rateScope string
	kind      recordKind
}

// rules returns the rule of each status that has the same text in all tools of the kind.
func (op operation) rules() statusRules {
	kind := op.kind

	return statusRules{
		http.StatusBadRequest:            withDetails(errBadRequest),
		http.StatusUnauthorized:          fixed(unauthorizedErr()),
		http.StatusForbidden:             fixed(forbiddenErr(kind.name)),
		http.StatusNotFound:              fixed(notFoundErr(kind.name)),
		http.StatusConflict:              fixed(errSlugConflict),
		http.StatusGone:                  kind.gone,
		http.StatusRequestEntityTooLarge: kind.tooLarge,
		http.StatusUnsupportedMediaType:  fixed(errUnsupportedContentType),
		http.StatusUnprocessableEntity:   withDetails(errValidation),
		http.StatusTooManyRequests:       fixed(rateLimitedErr(op.rateScope)),
		http.StatusInternalServerError:   fixed(errServer),
		http.StatusServiceUnavailable:    kind.unavailable,
	}
}

// responseError returns the tool error for the error variant res of a generated operation. A
// tool calls it after it took the success variant. overrides holds the statuses where the text
// of this tool differs from the kind rules.
func (op operation) responseError(res any, overrides statusRules) error {
	apiErr, ok := padmark.ResponseError(res)
	if !ok {
		return errorcontext.Wrap(unexpectedRes(res), op.context)
	}

	return errorcontext.Wrap(op.statusError(apiErr, overrides), op.context)
}

// fetch maps the result of one server call of a tool. A transport error gets the remediation
// text; an error variant gets the tool error of spec. call names the server call in the error
// contexts, for example "get link".
func fetch[Want any](res any, callErr error, call string, spec operation) (Want, error) {
	if callErr != nil {
		var zero Want

		return zero, errorcontext.Wrap(transportError(call, callErr), "handle "+call)
	}

	return successOf[Want](res, "map "+call+" res", spec, nil)
}

// successOf returns the success variant Want of res. For an error variant it returns the tool
// error of spec, wrapped with label, the context of the tool handler (for example "map get link
// res"). overrides holds the statuses where the text of this tool differs from the kind rules.
func successOf[Want any](res any, label string, spec operation, overrides statusRules) (Want, error) {
	if typed, ok := res.(Want); ok {
		return typed, nil
	}

	var zero Want

	return zero, fmt.Errorf("%s: %w", label, spec.responseError(res, overrides))
}

// statusError returns the tool error for apiErr: the rule of overrides, else the rule of the
// kind, else an unexpected response.
func (op operation) statusError(apiErr *padmark.APIError, overrides statusRules) error {
	if rule, ok := overrides[apiErr.StatusCode]; ok {
		return rule(apiErr)
	}

	if rule, ok := op.rules()[apiErr.StatusCode]; ok {
		return rule(apiErr)
	}

	return unexpectedStatus(apiErr)
}

// unexpectedStatus maps a status that the tool does not expect.
func unexpectedStatus(apiErr *padmark.APIError) error {
	return fmt.Errorf("%w: status %d: %s", errUnexpectedResponse, apiErr.StatusCode, apiErr.Message)
}

// withDetails returns a rule that adds the server message to cause.
func withDetails(cause error) statusRule {
	return func(apiErr *padmark.APIError) error {
		return apiStatus(cause, apiErr.Message)
	}
}

// fixed returns a rule that ignores the server message.
func fixed(err error) statusRule {
	return func(*padmark.APIError) error {
		return err
	}
}

// contentTooLarge maps 413 of a note or a link. The server message gives the limit.
func contentTooLarge(apiErr *padmark.APIError) error {
	return fmt.Errorf("%w: %s", errContentTooLarge, apiErr.Message)
}

// destroyedAfterRead maps 410 of a note or a link.
func destroyedAfterRead(kind string) statusRule {
	return fixed(fmt.Errorf("%w: the %s was destroyed after its first read (burn-after-reading with a TTL)",
		errGone, kind))
}

// apiDisabledCode is the code of each 503 answer of a server started with --disable-api.
const apiDisabledCode = "api_disabled"

// serviceUnavailableError distinguishes an operator-disabled API from other 503 responses.
func serviceUnavailableError(apiErr *padmark.APIError) error {
	if apiErr.Code == apiDisabledCode {
		return errAPIDisabled
	}

	return fmt.Errorf("%w: %s", errServiceUnavailable, apiErr.Message)
}

// fileUnavailableError reports a 503 of a file operation. Other than an operator-disabled API,
// it means that the server has no object storage, or that the storage does not respond.
func fileUnavailableError(apiErr *padmark.APIError) error {
	if apiErr.Code == apiDisabledCode {
		return errAPIDisabled
	}

	return fmt.Errorf("%w: %s — file storage is off or not available on this server; "+
		"ask the operator to check the S3 settings", errServiceUnavailable,
		cmp.Or(apiErr.Message, "no details provided"))
}

// uploadError maps an UploadFile error. The streaming client returns *padmark.APIError for a
// status error, ErrInvalidUploadOptions for a bad option, and a plain error for the transport.
func uploadError(err error) error {
	if apiErr, ok := errors.AsType[*padmark.APIError](err); ok {
		upload := operation{context: "decode upload file response", rateScope: "file uploads", kind: fileRecord()}

		return errorcontext.Wrap(upload.statusError(apiErr, statusRules{
			http.StatusForbidden: withDetails(errForbidden),
			// The JSON remediation of the kind rule is wrong for a multipart upload.
			http.StatusUnsupportedMediaType: unexpectedStatus,
		}), upload.context)
	}

	if errors.Is(err, padmark.ErrInvalidUploadOptions) {
		return fmt.Errorf("upload file: %w", err)
	}

	// The server answered too late, so the server is running: the transport text would be wrong.
	if isTimeout(err) {
		return fmt.Errorf("upload file: %w: %w", errUploadTimeout, err)
	}

	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("upload file: the upload was cancelled: %w", err)
	}

	return errorcontext.Wrap(transportError("upload file", err), "handle upload file")
}

// isTimeout reports whether err is a deadline of the context or a network timeout, such as the
// response header timeout of the transfer client.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	netErr, ok := errors.AsType[net.Error](err)

	return ok && netErr.Timeout()
}

// transportError wraps a network-level failure with the remediation the model needs. The
// padmark server may be started later than the agent (docker compose up, dev cycle), so a
// tool call failing with connection refused is the expected shape, not a fatal proxy error.
func transportError(op string, err error) error {
	return fmt.Errorf("%s: %w — is padmark-server running at the configured server URL?", op, err)
}

// Error texts. Each carries the remediation the model needs to self-correct.

func apiStatus(cause error, message string) error {
	return fmt.Errorf("%w: %s", cause, cmp.Or(message, "no details provided"))
}

func unauthorizedErr() error {
	return fmt.Errorf("%w: the server needs a bearer token (--token / %s) or edit_code", errUnauthorized, EnvToken)
}

func forbiddenErr(kind string) error {
	return fmt.Errorf("%w: wrong edit_code for the %s (an edit_code "+
		"is shown exactly once, at creation)", errForbidden, kind)
}

func notFoundErr(kind string) error {
	return fmt.Errorf("%s %w — wrong ID, already deleted/expired, or a different server", kind, errNotFound)
}

func rateLimitedErr(op string) error {
	return fmt.Errorf("%w: rate limit or failure lockout is active on %s — wait and retry", errRateLimited, op)
}

// burnReadRefused maps 409 of get_note: the server refused a burn-after-reading read as not
// explicit. The client always sends Accept: application/json, so this means a misbehaving proxy
// stripped the header; the note is still intact.
func burnReadRefused(apiErr *padmark.APIError) error {
	return fmt.Errorf("%w: %s — the note is still intact", errBurnReadRefused, apiErr.Message)
}

func unexpectedRes(res any) error {
	return fmt.Errorf("%w %T", errUnexpectedResponse, res)
}
