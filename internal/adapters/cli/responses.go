package cli

import (
	"errors"
	"fmt"
	"net/http"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// The CLI maps each error response to one CLI error in this file. pkg/client gives an
// *padmark.APIError for each error variant of a generated operation (ResponseError) and for an
// error status of UploadFile or DownloadFile. Thus one table per kind of record covers all
// commands, and a command names only the statuses where its error differs.

// statusRule returns the CLI error for an error response.
type statusRule func(apiErr *padmark.APIError) error

// statusRules maps an HTTP status to its rule.
type statusRules map[int]statusRule

// recordKind holds the CLI errors and the ID rules that depend on the kind of record.
type recordKind struct {
	// notFound, gone and ownership are the errors of 404, 410 and 403. A nil value leaves the
	// status to the fallback of statusError.
	notFound  error
	gone      error
	ownership error
	// tooLarge is the error of 413. The server message gives the limit.
	tooLarge error
	// idRequired is the error of a missing ID argument.
	idRequired error
	// unavailable maps 503: an operator-disabled API, or the file storage, or a failure.
	unavailable statusRule
	// fileURLs lets the ID argument be the file URL that `upload` prints.
	fileURLs bool
}

func noteRecord() *recordKind {
	return &recordKind{
		notFound: ErrNoteNotFound, gone: ErrNoteGone, ownership: errNoteOwnershipRequired,
		tooLarge: ErrContentTooLarge, idRequired: errNoteIDRequired, unavailable: serviceUnavailableError,
		fileURLs: false,
	}
}

func linkRecord() *recordKind {
	return &recordKind{
		notFound: ErrLinkNotFound, gone: ErrLinkGone, ownership: errLinkOwnershipRequired,
		tooLarge: ErrRequestTooLarge, idRequired: errLinkIDRequired, unavailable: serviceUnavailableError,
		fileURLs: false,
	}
}

func fileRecord() *recordKind {
	return &recordKind{
		notFound: ErrFileNotFound, gone: ErrFileGone, ownership: errFileOwnershipRequired,
		tooLarge: ErrRequestTooLarge, idRequired: errFileIDRequired, unavailable: fileServiceUnavailableError,
		fileURLs: true,
	}
}

// accountRecord is the kind of the operations that are not about a record: tokens, invites and
// the readiness probe.
func accountRecord() *recordKind {
	return &recordKind{
		notFound: nil, gone: nil, ownership: nil, tooLarge: ErrRequestTooLarge, idRequired: nil,
		unavailable: serviceUnavailableError, fileURLs: false,
	}
}

// rules returns the rule of each status that has the same CLI error in all commands of kind.
func (kind *recordKind) rules() statusRules {
	rules := statusRules{
		http.StatusBadRequest:            withMessage(ErrBadRequest),
		http.StatusUnauthorized:          withMessage(ErrUnauthorized),
		http.StatusConflict:              withMessage(ErrSlugConflict),
		http.StatusRequestEntityTooLarge: withMessage(kind.tooLarge),
		http.StatusUnsupportedMediaType:  withMessage(ErrBadRequest),
		http.StatusUnprocessableEntity:   withMessage(ErrValidation),
		http.StatusTooManyRequests:       fixed(ErrRateLimited),
		http.StatusInternalServerError:   withMessage(ErrServer),
		http.StatusServiceUnavailable:    kind.unavailable,
	}

	for status, err := range map[int]error{
		http.StatusForbidden: kind.ownership,
		http.StatusNotFound:  kind.notFound,
		http.StatusGone:      kind.gone,
	} {
		if err != nil {
			rules[status] = fixed(err)
		}
	}

	return rules
}

// responseError returns the CLI error for the error variant res of a generated operation. A
// command calls it after it took the success variant. overrides holds the statuses where the
// error of this command differs from the kind rules.
func responseError(res any, kind *recordKind, overrides statusRules) error {
	apiErr, ok := padmark.ResponseError(res)
	if !ok {
		return fmt.Errorf("%w: %T", ErrUnexpectedResponse, res)
	}

	return statusError(apiErr, kind, overrides)
}

// statusError returns the CLI error for apiErr: the rule of overrides, else the rule of kind. A
// status without a rule is a server error from 500 on, else an unexpected response.
func statusError(apiErr *padmark.APIError, kind *recordKind, overrides statusRules) error {
	if rule, ok := overrides[apiErr.StatusCode]; ok {
		return rule(apiErr)
	}

	if rule, ok := kind.rules()[apiErr.StatusCode]; ok {
		return rule(apiErr)
	}

	if apiErr.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("%w: %s", ErrServer, apiErr.Message)
	}

	return fmt.Errorf("%w: status %d: %s", ErrUnexpectedResponse, apiErr.StatusCode, apiErr.Message)
}

// withMessage returns a rule that adds the server message to cause.
func withMessage(cause error) statusRule {
	return func(apiErr *padmark.APIError) error {
		return fmt.Errorf("%w: %s", cause, apiErr.Message)
	}
}

// fixed returns a rule that ignores the server message.
func fixed(err error) statusRule {
	return func(*padmark.APIError) error {
		return err
	}
}

// mutationRules are the overrides of an update or a delete. Its 401 answer has no body, or a
// body that the CLI does not show.
func mutationRules() statusRules {
	return statusRules{http.StatusUnauthorized: fixed(ErrUnauthorized)}
}

// fileTransferError maps an error of UploadFile or DownloadFile. A response with an error
// status comes as *padmark.APIError. Other errors (transport, local I/O) keep their type, so
// errors.As still finds them. conflict is the error of 409: a slug conflict for upload and a
// refused burn read for download.
func fileTransferError(err, conflict error) error {
	apiErr, ok := errors.AsType[*padmark.APIError](err)
	if !ok {
		return err
	}

	return statusError(apiErr, fileRecord(), statusRules{
		http.StatusForbidden:             withMessage(ErrForbidden),
		http.StatusConflict:              withMessage(conflict),
		http.StatusRequestEntityTooLarge: withMessage(ErrFileTooLarge),
	})
}

// filesOffMessage is the message of a /files response when the server has no object storage.
// The server sends no machine-readable code for this case, so the CLI compares the message
// (domain.ErrFeatureNotSupported). A 503 with a different message, for example "file storage is
// unavailable", is a temporary failure and keeps the server message.
const filesOffMessage = "feature is not enabled"

// filesOffCode is the code that a server can send when file publishing is off.
const filesOffCode = "files_disabled"

// apiDisabledCode is the code of each 503 answer of a server started with --disable-api.
const apiDisabledCode = "api_disabled"

// serviceUnavailableError distinguishes an operator-disabled API from other 503 responses.
func serviceUnavailableError(apiErr *padmark.APIError) error {
	if apiErr.Code == apiDisabledCode {
		return ErrAPIDisabled
	}

	return fmt.Errorf("%w: %s", ErrServiceUnavailable, apiErr.Message)
}

// fileServiceUnavailableError maps a 503 response of a /files operation. It also identifies a
// server that has file publishing off.
func fileServiceUnavailableError(apiErr *padmark.APIError) error {
	switch {
	case apiErr.Code == apiDisabledCode:
		return ErrAPIDisabled
	case apiErr.Code == filesOffCode, apiErr.Message == filesOffMessage:
		return ErrFilesDisabled
	default:
		return fmt.Errorf("%w: %s", ErrServiceUnavailable, apiErr.Message)
	}
}
