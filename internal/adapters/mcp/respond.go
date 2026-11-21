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

// Response-union mapping. Every padmark operation returns a success variant plus
// ErrorResponse-coded failure variants; these mappers turn each failure into a text error the
// model can act on. HTTP transport errors are wrapped by transportError in the handlers.

func mapCreateNoteRes(res padmark.CreateNoteRes) (*padmark.CreateNoteResponse, error) {
	switch typed := res.(type) {
	case *padmark.CreateNoteResponseHeaders:
		return &typed.Response, nil
	case *padmark.CreateNoteBadRequest:
		return nil, errorcontext.Wrap(apiStatus(ErrBadRequest, typed.Message), "decode create note response")
	case *padmark.CreateNoteUnauthorized:
		return nil, errorcontext.Wrap(unauthorizedErr(), "decode create note response")
	case *padmark.CreateNoteConflict:
		return nil, errorcontext.Wrap(conflictErr(), "decode create note response")
	case *padmark.CreateNoteRequestEntityTooLarge:
		return nil, fmt.Errorf("%w: %s", ErrContentTooLarge, typed.Message)
	case *padmark.CreateNoteUnsupportedMediaType:
		return nil, errorcontext.Wrap(unsupportedErr(), "decode create note response")
	case *padmark.CreateNoteUnprocessableEntity:
		return nil, errorcontext.Wrap(apiStatus(ErrRejected, typed.Message), "decode create note response")
	case *padmark.CreateNoteServiceUnavailable:
		return nil, errorcontext.Wrap(
			serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode create note response")
	case *padmark.CreateNoteInternalServerError:
		return nil, errorcontext.Wrap(serverErr(), "decode create note response")
	default:
		return nil, errorcontext.Wrap(unexpectedRes(res), "decode create note response")
	}
}

func mapGetNoteRes(res padmark.GetNoteRes) (*padmark.NoteResponse, error) {
	switch typed := res.(type) {
	case *padmark.NoteResponse:
		return typed, nil
	case *padmark.GetNoteUnauthorized:
		return nil, errorcontext.Wrap(unauthorizedErr(), "decode get note response")
	case *padmark.GetNoteNotFound:
		return nil, errorcontext.Wrap(notFoundErr("note"), "decode get note response")
	case *padmark.GetNoteGone:
		return nil, errorcontext.Wrap(goneErr("note"), "decode get note response")
	case *padmark.GetNoteConflict:
		return nil, errorcontext.Wrap(burnReadRefusedErr(typed.Message), "decode get note response")
	case *padmark.GetNoteServiceUnavailable:
		return nil, errorcontext.Wrap(
			serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode get note response")
	case *padmark.GetNoteInternalServerError:
		return nil, errorcontext.Wrap(serverErr(), "decode get note response")
	default:
		return nil, errorcontext.Wrap(unexpectedRes(res), "decode get note response")
	}
}

func mapUpdateNoteRes(res padmark.UpdateNoteRes) (*padmark.NoteResponse, error) {
	if typed, ok := res.(*padmark.NoteResponse); ok {
		return typed, nil
	}

	err := mapUpdateNoteClientErr(res)
	if err != nil {
		return nil, err
	}

	switch typed := res.(type) {
	case *padmark.UpdateNoteServiceUnavailable:
		return nil, errorcontext.Wrap(
			serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode update note response")
	case *padmark.UpdateNoteInternalServerError:
		return nil, errorcontext.Wrap(serverErr(), "decode update note response")
	default:
		return nil, errorcontext.Wrap(unexpectedRes(res), "decode update note response")
	}
}

// mapUpdateNoteClientErr maps the client-error variants of an UpdateNote response. It returns nil
// for every other variant, so the caller handles those.
func mapUpdateNoteClientErr(res padmark.UpdateNoteRes) error {
	switch typed := res.(type) {
	case *padmark.UpdateNoteBadRequest:
		return errorcontext.Wrap(apiStatus(ErrBadRequest, typed.Message), "decode update note response")
	case *padmark.UpdateNoteUnauthorized:
		return errorcontext.Wrap(unauthorizedErr(), "decode update note response")
	case *padmark.UpdateNoteForbidden:
		return errorcontext.Wrap(forbiddenErr("note"), "decode update note response")
	case *padmark.UpdateNoteNotFound:
		return errorcontext.Wrap(notFoundErr("note"), "decode update note response")
	case *padmark.UpdateNoteRequestEntityTooLarge:
		return fmt.Errorf("%w: %s", ErrContentTooLarge, typed.Message)
	case *padmark.UpdateNoteUnsupportedMediaType:
		return errorcontext.Wrap(unsupportedErr(), "decode update note response")
	case *padmark.UpdateNoteUnprocessableEntity:
		return errorcontext.Wrap(apiStatus(ErrRejected, typed.Message), "decode update note response")
	case *padmark.UpdateNoteTooManyRequests:
		return errorcontext.Wrap(rateLimitedErr("note updates"), "decode update note response")
	default:
		return nil
	}
}

func mapDeleteNoteRes(res padmark.DeleteNoteRes) error {
	return errorcontext.Wrap(mapDeleteRes(res, "note"), "delete note response")
}

func mapCreateLinkRes(res padmark.CreateLinkRes) (*padmark.CreateLinkResponseHeaders, error) {
	if typed, ok := res.(*padmark.CreateLinkResponseHeaders); ok {
		return typed, nil
	}

	err := mapCreateLinkClientErr(res)
	if err != nil {
		return nil, err
	}

	switch typed := res.(type) {
	case *padmark.CreateLinkServiceUnavailable:
		return nil, errorcontext.Wrap(
			serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode create link response")
	case *padmark.CreateLinkInternalServerError:
		return nil, errorcontext.Wrap(serverErr(), "decode create link response")
	default:
		return nil, errorcontext.Wrap(unexpectedRes(res), "decode create link response")
	}
}

// mapCreateLinkClientErr maps the client-error variants of a CreateLink response. It returns nil
// for every other variant, so the caller handles those.
func mapCreateLinkClientErr(res padmark.CreateLinkRes) error {
	switch typed := res.(type) {
	case *padmark.CreateLinkBadRequest:
		return errorcontext.Wrap(apiStatus(ErrBadRequest, typed.Message), "decode create link response")
	case *padmark.CreateLinkUnauthorized:
		return errorcontext.Wrap(unauthorizedErr(), "decode create link response")
	case *padmark.CreateLinkConflict:
		return errorcontext.Wrap(conflictErr(), "decode create link response")
	case *padmark.CreateLinkRequestEntityTooLarge:
		return fmt.Errorf("%w: %s", ErrContentTooLarge, typed.Message)
	case *padmark.CreateLinkUnsupportedMediaType:
		return errorcontext.Wrap(unsupportedErr(), "decode create link response")
	case *padmark.CreateLinkUnprocessableEntity:
		return errorcontext.Wrap(apiStatus(ErrRejected, typed.Message), "decode create link response")
	case *padmark.CreateLinkTooManyRequests:
		return errorcontext.Wrap(rateLimitedErr("link creations"), "decode create link response")
	default:
		return nil
	}
}

func mapCreateLinksBulkRes(res padmark.CreateLinksBulkRes) (*padmark.BulkCreateLinksResponse, error) {
	switch typed := res.(type) {
	case *padmark.BulkCreateLinksResponse:
		return typed, nil
	case *padmark.CreateLinksBulkBadRequest:
		return nil, errorcontext.Wrap(apiStatus(ErrBadRequest, typed.Message), "decode create links bulk response")
	case *padmark.CreateLinksBulkUnauthorized:
		return nil, errorcontext.Wrap(unauthorizedErr(), "decode create links bulk response")
	case *padmark.CreateLinksBulkRequestEntityTooLarge:
		return nil, fmt.Errorf("%w: %s", ErrContentTooLarge, typed.Message)
	case *padmark.CreateLinksBulkUnsupportedMediaType:
		return nil, errorcontext.Wrap(unsupportedErr(), "decode create links bulk response")
	case *padmark.CreateLinksBulkTooManyRequests:
		return nil, errorcontext.Wrap(rateLimitedErr("bulk link creations"), "decode create links bulk response")
	case *padmark.CreateLinksBulkServiceUnavailable:
		return nil, errorcontext.Wrap(
			serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode bulk link creation response",
		)
	case *padmark.CreateLinksBulkInternalServerError:
		return nil, errorcontext.Wrap(serverErr(), "decode create links bulk response")
	default:
		return nil, errorcontext.Wrap(unexpectedRes(res), "decode create links bulk response")
	}
}

func mapGetLinkRes(res padmark.GetLinkRes) (*padmark.LinkResponse, error) {
	switch typed := res.(type) {
	case *padmark.LinkResponse:
		return typed, nil
	case *padmark.GetLinkUnauthorized:
		return nil, errorcontext.Wrap(unauthorizedErr(), "decode get link response")
	case *padmark.GetLinkNotFound:
		return nil, errorcontext.Wrap(notFoundErr("link"), "decode get link response")
	case *padmark.GetLinkGone:
		return nil, errorcontext.Wrap(goneErr("link"), "decode get link response")
	case *padmark.GetLinkServiceUnavailable:
		return nil, errorcontext.Wrap(
			serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode get link response")
	case *padmark.GetLinkInternalServerError:
		return nil, errorcontext.Wrap(serverErr(), "decode get link response")
	default:
		return nil, errorcontext.Wrap(unexpectedRes(res), "decode get link response")
	}
}

func mapDeleteLinkRes(res padmark.DeleteLinkRes) error {
	return errorcontext.Wrap(mapDeleteRes(res, "link"), "delete link response")
}

func mapDeleteRes(res any, resource string) error {
	switch typed := res.(type) {
	case *padmark.DeleteNoteNoContent, *padmark.DeleteLinkNoContent:
		return nil
	case *padmark.DeleteNoteUnauthorized, *padmark.DeleteLinkUnauthorized:
		return errorcontext.Wrap(unauthorizedErr(), "authorize deletion")
	case *padmark.DeleteNoteForbidden, *padmark.DeleteLinkForbidden:
		return errorcontext.Wrap(forbiddenErr(resource), "authorize deletion")
	case *padmark.DeleteNoteNotFound, *padmark.DeleteLinkNotFound:
		return errorcontext.Wrap(notFoundErr(resource), "resolve deleted resource")
	case *padmark.DeleteNoteTooManyRequests, *padmark.DeleteLinkTooManyRequests:
		return errorcontext.Wrap(rateLimitedErr(resource+" deletes"), "limit deletion requests")
	case *padmark.DeleteNoteServiceUnavailable:
		return errorcontext.Wrap(
			serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode service unavailable response",
		)
	case *padmark.DeleteLinkServiceUnavailable:
		return errorcontext.Wrap(
			serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode service unavailable response",
		)
	case *padmark.DeleteNoteInternalServerError, *padmark.DeleteLinkInternalServerError:
		return ErrServer
	default:
		return errorcontext.Wrap(unexpectedRes(res), "decode deletion response")
	}
}

// uploadError maps an UploadFile error. The streaming client returns *padmark.APIError for a
// status error, ErrInvalidUploadOptions for a bad option, and a plain error for the transport.
func uploadError(err error) error {
	if apiErr, ok := errors.AsType[*padmark.APIError](err); ok {
		return errorcontext.Wrap(fileStatusError(apiErr), "decode upload file response")
	}

	if errors.Is(err, padmark.ErrInvalidUploadOptions) {
		return fmt.Errorf("upload file: %w", err)
	}

	// The server answered too late, so the server is running: the transport text would be wrong.
	if isTimeout(err) {
		return fmt.Errorf("upload file: %w: %w", ErrUploadTimeout, err)
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

// fileStatusError turns the status of an upload response into the error text for the model.
func fileStatusError(apiErr *padmark.APIError) error {
	switch apiErr.StatusCode {
	case http.StatusBadRequest:
		return apiStatus(ErrBadRequest, apiErr.Message)
	case http.StatusUnauthorized:
		return unauthorizedErr()
	case http.StatusForbidden:
		return apiStatus(ErrForbidden, apiErr.Message)
	case http.StatusConflict:
		return conflictErr()
	case http.StatusRequestEntityTooLarge:
		// The server message gives the limit in bytes.
		return apiStatus(ErrContentTooLarge, apiErr.Message)
	case http.StatusUnprocessableEntity:
		return apiStatus(ErrRejected, apiErr.Message)
	case http.StatusTooManyRequests:
		return rateLimitedErr("file uploads")
	case http.StatusServiceUnavailable:
		return fileUnavailableError(padmark.ErrorResponse{
			Message: apiErr.Message, Code: padmark.NewOptString(apiErr.Code),
		})
	case http.StatusInternalServerError:
		return serverErr()
	default:
		return fmt.Errorf("%w: status %d: %s", ErrUnexpectedResponse, apiErr.StatusCode, apiErr.Message)
	}
}

// fileUnavailableError reports a 503 of a file operation. Other than an operator-disabled API,
// it means that the server has no object storage, or that the storage does not respond.
func fileUnavailableError(response padmark.ErrorResponse) error {
	if response.Code.Or("") == "api_disabled" {
		return ErrAPIDisabled
	}

	return fmt.Errorf("%w: %s — file storage is off or not available on this server; "+
		"ask the operator to check the S3 settings", ErrServiceUnavailable,
		cmp.Or(response.Message, "no details provided"))
}

func mapGetFileRes(res padmark.GetFileRes) (*padmark.FileInfo, error) {
	switch typed := res.(type) {
	case *padmark.FileInfo:
		return typed, nil
	case *padmark.GetFileUnauthorized:
		return nil, errorcontext.Wrap(unauthorizedErr(), "decode get file response")
	case *padmark.GetFileNotFound:
		return nil, errorcontext.Wrap(notFoundErr("file"), "decode get file response")
	case *padmark.GetFileGone:
		return nil, errorcontext.Wrap(apiStatus(ErrGone, typed.Message), "decode get file response")
	case *padmark.GetFileServiceUnavailable:
		return nil, errorcontext.Wrap(
			fileUnavailableError(padmark.ErrorResponse(*typed)), "decode get file response")
	case *padmark.GetFileInternalServerError:
		return nil, errorcontext.Wrap(serverErr(), "decode get file response")
	default:
		return nil, errorcontext.Wrap(unexpectedRes(res), "decode get file response")
	}
}

func mapUpdateFileRes(res padmark.UpdateFileRes) (*padmark.FileInfo, error) {
	if typed, ok := res.(*padmark.FileInfo); ok {
		return typed, nil
	}

	err := mapUpdateFileClientErr(res)
	if err != nil {
		return nil, err
	}

	switch typed := res.(type) {
	case *padmark.UpdateFileServiceUnavailable:
		return nil, errorcontext.Wrap(
			fileUnavailableError(padmark.ErrorResponse(*typed)), "decode update file response")
	case *padmark.UpdateFileInternalServerError:
		return nil, errorcontext.Wrap(serverErr(), "decode update file response")
	default:
		return nil, errorcontext.Wrap(unexpectedRes(res), "decode update file response")
	}
}

// mapUpdateFileClientErr maps the client-error variants of an UpdateFile response. It returns nil
// for every other variant, so the caller handles those.
func mapUpdateFileClientErr(res padmark.UpdateFileRes) error {
	switch typed := res.(type) {
	case *padmark.UpdateFileBadRequest:
		return errorcontext.Wrap(apiStatus(ErrBadRequest, typed.Message), "decode update file response")
	case *padmark.UpdateFileUnauthorized:
		return errorcontext.Wrap(unauthorizedErr(), "decode update file response")
	case *padmark.UpdateFileForbidden:
		return errorcontext.Wrap(forbiddenErr("file"), "decode update file response")
	case *padmark.UpdateFileNotFound:
		return errorcontext.Wrap(notFoundErr("file"), "decode update file response")
	case *padmark.UpdateFileRequestEntityTooLarge:
		return errorcontext.Wrap(apiStatus(ErrContentTooLarge, typed.Message), "decode update file response")
	case *padmark.UpdateFileUnprocessableEntity:
		return errorcontext.Wrap(apiStatus(ErrRejected, typed.Message), "decode update file response")
	case *padmark.UpdateFileTooManyRequests:
		return errorcontext.Wrap(rateLimitedErr("file updates"), "decode update file response")
	default:
		return nil
	}
}

func mapDeleteFileRes(res padmark.DeleteFileRes) error {
	switch typed := res.(type) {
	case *padmark.DeleteFileNoContent:
		return nil
	case *padmark.DeleteFileUnauthorized:
		return errorcontext.Wrap(unauthorizedErr(), "decode delete file response")
	case *padmark.DeleteFileForbidden:
		return errorcontext.Wrap(forbiddenErr("file"), "decode delete file response")
	case *padmark.DeleteFileNotFound:
		return errorcontext.Wrap(notFoundErr("file"), "decode delete file response")
	case *padmark.DeleteFileTooManyRequests:
		return errorcontext.Wrap(rateLimitedErr("file deletes"), "decode delete file response")
	case *padmark.DeleteFileServiceUnavailable:
		return errorcontext.Wrap(
			fileUnavailableError(padmark.ErrorResponse(*typed)), "decode delete file response")
	case *padmark.DeleteFileInternalServerError:
		return ErrServer
	default:
		return errorcontext.Wrap(unexpectedRes(res), "decode delete file response")
	}
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
	return fmt.Errorf("%w: the server needs a bearer token (--token / %s) or edit_code", ErrUnauthorized, EnvToken)
}

func forbiddenErr(kind string) error {
	return fmt.Errorf("%w: wrong edit_code for the %s (an edit_code "+
		"is shown exactly once, at creation)", ErrForbidden, kind)
}

func notFoundErr(kind string) error {
	return fmt.Errorf("%s %w — wrong ID, already deleted/expired, or a different server", kind, ErrNotFound)
}

func goneErr(kind string) error {
	return fmt.Errorf("%w: the %s was destroyed after its first read (burn-after-reading with a TTL)", ErrGone, kind)
}

func conflictErr() error {
	return ErrSlugConflict
}

func rateLimitedErr(op string) error {
	return fmt.Errorf("%w: rate limit or failure lockout is active on %s — wait and retry", ErrRateLimited, op)
}

func unsupportedErr() error {
	return ErrUnsupportedContentType
}

// burnReadRefusedErr reports a burn-after-reading read the server refused as not explicit
// (409). The client always sends Accept: application/json, so this means a misbehaving proxy
// stripped the header; the note is still intact.
func burnReadRefusedErr(message string) error {
	return fmt.Errorf("%w: %s — the note is still intact", ErrBurnReadRefused, message)
}

func serverErr() error {
	return ErrServer
}

func unexpectedRes(res any) error {
	return fmt.Errorf("%w %T", ErrUnexpectedResponse, res)
}
