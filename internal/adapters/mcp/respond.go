package mcp

import (
	"cmp"
	"fmt"

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
		return nil, errorcontext.Wrap(serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode create note response")
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
		return nil, errorcontext.Wrap(serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode get note response")
	case *padmark.GetNoteInternalServerError:
		return nil, errorcontext.Wrap(serverErr(), "decode get note response")
	default:
		return nil, errorcontext.Wrap(unexpectedRes(res), "decode get note response")
	}
}

//nolint:cyclop // one branch per generated HTTP response variant
func mapUpdateNoteRes(res padmark.UpdateNoteRes) (*padmark.NoteResponse, error) {
	switch typed := res.(type) {
	case *padmark.NoteResponse:
		return typed, nil
	case *padmark.UpdateNoteBadRequest:
		return nil, errorcontext.Wrap(apiStatus(ErrBadRequest, typed.Message), "decode update note response")
	case *padmark.UpdateNoteUnauthorized:
		return nil, errorcontext.Wrap(unauthorizedErr(), "decode update note response")
	case *padmark.UpdateNoteForbidden:
		return nil, errorcontext.Wrap(forbiddenErr("note"), "decode update note response")
	case *padmark.UpdateNoteNotFound:
		return nil, errorcontext.Wrap(notFoundErr("note"), "decode update note response")
	case *padmark.UpdateNoteRequestEntityTooLarge:
		return nil, fmt.Errorf("%w: %s", ErrContentTooLarge, typed.Message)
	case *padmark.UpdateNoteUnsupportedMediaType:
		return nil, errorcontext.Wrap(unsupportedErr(), "decode update note response")
	case *padmark.UpdateNoteUnprocessableEntity:
		return nil, errorcontext.Wrap(apiStatus(ErrRejected, typed.Message), "decode update note response")
	case *padmark.UpdateNoteTooManyRequests:
		return nil, errorcontext.Wrap(rateLimitedErr("note updates"), "decode update note response")
	case *padmark.UpdateNoteServiceUnavailable:
		return nil, errorcontext.Wrap(serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode update note response")
	case *padmark.UpdateNoteInternalServerError:
		return nil, errorcontext.Wrap(serverErr(), "decode update note response")
	default:
		return nil, errorcontext.Wrap(unexpectedRes(res), "decode update note response")
	}
}

func mapDeleteNoteRes(res padmark.DeleteNoteRes) error {
	return errorcontext.Wrap(mapDeleteRes(res, "note"), "delete note response")
}

func mapCreateLinkRes(res padmark.CreateLinkRes) (*padmark.CreateLinkResponseHeaders, error) {
	switch typed := res.(type) {
	case *padmark.CreateLinkResponseHeaders:
		return typed, nil
	case *padmark.CreateLinkBadRequest:
		return nil, errorcontext.Wrap(apiStatus(ErrBadRequest, typed.Message), "decode create link response")
	case *padmark.CreateLinkUnauthorized:
		return nil, errorcontext.Wrap(unauthorizedErr(), "decode create link response")
	case *padmark.CreateLinkConflict:
		return nil, errorcontext.Wrap(conflictErr(), "decode create link response")
	case *padmark.CreateLinkRequestEntityTooLarge:
		return nil, fmt.Errorf("%w: %s", ErrContentTooLarge, typed.Message)
	case *padmark.CreateLinkUnsupportedMediaType:
		return nil, errorcontext.Wrap(unsupportedErr(), "decode create link response")
	case *padmark.CreateLinkUnprocessableEntity:
		return nil, errorcontext.Wrap(apiStatus(ErrRejected, typed.Message), "decode create link response")
	case *padmark.CreateLinkTooManyRequests:
		return nil, errorcontext.Wrap(rateLimitedErr("link creations"), "decode create link response")
	case *padmark.CreateLinkServiceUnavailable:
		return nil, errorcontext.Wrap(serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode create link response")
	case *padmark.CreateLinkInternalServerError:
		return nil, errorcontext.Wrap(serverErr(), "decode create link response")
	default:
		return nil, errorcontext.Wrap(unexpectedRes(res), "decode create link response")
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
		return nil, errorcontext.Wrap(serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode get link response")
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
