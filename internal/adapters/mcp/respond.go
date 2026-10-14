package mcp

import (
	"errors"
	"fmt"

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
		return nil, apiStatus("bad request", typed.Message)
	case *padmark.CreateNoteUnauthorized:
		return nil, unauthorizedErr()
	case *padmark.CreateNoteConflict:
		return nil, conflictErr()
	case *padmark.CreateNoteRequestEntityTooLarge:
		return nil, fmt.Errorf("content too large: %s", typed.Message)
	case *padmark.CreateNoteUnsupportedMediaType:
		return nil, unsupportedErr()
	case *padmark.CreateNoteUnprocessableEntity:
		return nil, apiStatus("rejected", typed.Message)
	case *padmark.CreateNoteServiceUnavailable:
		return nil, serviceUnavailableError(padmark.ErrorResponse(*typed))
	case *padmark.CreateNoteInternalServerError:
		return nil, serverErr()
	default:
		return nil, unexpectedRes(res)
	}
}

func mapGetNoteRes(res padmark.GetNoteRes) (*padmark.NoteResponse, error) {
	switch typed := res.(type) {
	case *padmark.NoteResponse:
		return typed, nil
	case *padmark.GetNoteUnauthorized:
		return nil, unauthorizedErr()
	case *padmark.GetNoteNotFound:
		return nil, notFoundErr("note")
	case *padmark.GetNoteGone:
		return nil, goneErr("note")
	case *padmark.GetNoteServiceUnavailable:
		return nil, serviceUnavailableError(padmark.ErrorResponse(*typed))
	case *padmark.GetNoteInternalServerError:
		return nil, serverErr()
	default:
		return nil, unexpectedRes(res)
	}
}

//nolint:cyclop // one branch per generated HTTP response variant
func mapUpdateNoteRes(res padmark.UpdateNoteRes) (*padmark.NoteResponse, error) {
	switch typed := res.(type) {
	case *padmark.NoteResponse:
		return typed, nil
	case *padmark.UpdateNoteBadRequest:
		return nil, apiStatus("bad request", typed.Message)
	case *padmark.UpdateNoteUnauthorized:
		return nil, unauthorizedErr()
	case *padmark.UpdateNoteForbidden:
		return nil, forbiddenErr("note")
	case *padmark.UpdateNoteNotFound:
		return nil, notFoundErr("note")
	case *padmark.UpdateNoteRequestEntityTooLarge:
		return nil, fmt.Errorf("content too large: %s", typed.Message)
	case *padmark.UpdateNoteUnsupportedMediaType:
		return nil, unsupportedErr()
	case *padmark.UpdateNoteUnprocessableEntity:
		return nil, apiStatus("rejected", typed.Message)
	case *padmark.UpdateNoteTooManyRequests:
		return nil, rateLimitedErr("note updates")
	case *padmark.UpdateNoteServiceUnavailable:
		return nil, serviceUnavailableError(padmark.ErrorResponse(*typed))
	case *padmark.UpdateNoteInternalServerError:
		return nil, serverErr()
	default:
		return nil, unexpectedRes(res)
	}
}

func mapDeleteNoteRes(res padmark.DeleteNoteRes) error {
	switch typed := res.(type) {
	case *padmark.DeleteNoteNoContent:
		return nil
	case *padmark.DeleteNoteUnauthorized:
		return unauthorizedErr()
	case *padmark.DeleteNoteForbidden:
		return forbiddenErr("note")
	case *padmark.DeleteNoteNotFound:
		return notFoundErr("note")
	case *padmark.DeleteNoteTooManyRequests:
		return rateLimitedErr("note deletes")
	case *padmark.DeleteNoteServiceUnavailable:
		return serviceUnavailableError(padmark.ErrorResponse(*typed))
	case *padmark.DeleteNoteInternalServerError:
		return serverErr()
	default:
		return unexpectedRes(res)
	}
}

func mapCreateLinkRes(res padmark.CreateLinkRes) (*padmark.CreateLinkResponseHeaders, error) {
	switch typed := res.(type) {
	case *padmark.CreateLinkResponseHeaders:
		return typed, nil
	case *padmark.CreateLinkBadRequest:
		return nil, apiStatus("bad request", typed.Message)
	case *padmark.CreateLinkUnauthorized:
		return nil, unauthorizedErr()
	case *padmark.CreateLinkConflict:
		return nil, conflictErr()
	case *padmark.CreateLinkRequestEntityTooLarge:
		return nil, fmt.Errorf("content too large: %s", typed.Message)
	case *padmark.CreateLinkUnsupportedMediaType:
		return nil, unsupportedErr()
	case *padmark.CreateLinkUnprocessableEntity:
		return nil, apiStatus("rejected", typed.Message)
	case *padmark.CreateLinkTooManyRequests:
		return nil, rateLimitedErr("link creations")
	case *padmark.CreateLinkServiceUnavailable:
		return nil, serviceUnavailableError(padmark.ErrorResponse(*typed))
	case *padmark.CreateLinkInternalServerError:
		return nil, serverErr()
	default:
		return nil, unexpectedRes(res)
	}
}

func mapCreateLinksBulkRes(res padmark.CreateLinksBulkRes) (*padmark.BulkCreateLinksResponse, error) {
	switch typed := res.(type) {
	case *padmark.BulkCreateLinksResponse:
		return typed, nil
	case *padmark.CreateLinksBulkBadRequest:
		return nil, apiStatus("bad request", typed.Message)
	case *padmark.CreateLinksBulkUnauthorized:
		return nil, unauthorizedErr()
	case *padmark.CreateLinksBulkRequestEntityTooLarge:
		return nil, fmt.Errorf("content too large: %s", typed.Message)
	case *padmark.CreateLinksBulkUnsupportedMediaType:
		return nil, unsupportedErr()
	case *padmark.CreateLinksBulkTooManyRequests:
		return nil, rateLimitedErr("bulk link creations")
	case *padmark.CreateLinksBulkServiceUnavailable:
		return nil, serviceUnavailableError(padmark.ErrorResponse(*typed))
	case *padmark.CreateLinksBulkInternalServerError:
		return nil, serverErr()
	default:
		return nil, unexpectedRes(res)
	}
}

func mapGetLinkRes(res padmark.GetLinkRes) (*padmark.LinkResponse, error) {
	switch typed := res.(type) {
	case *padmark.LinkResponse:
		return typed, nil
	case *padmark.GetLinkUnauthorized:
		return nil, unauthorizedErr()
	case *padmark.GetLinkNotFound:
		return nil, notFoundErr("link")
	case *padmark.GetLinkGone:
		return nil, goneErr("link")
	case *padmark.GetLinkServiceUnavailable:
		return nil, serviceUnavailableError(padmark.ErrorResponse(*typed))
	case *padmark.GetLinkInternalServerError:
		return nil, serverErr()
	default:
		return nil, unexpectedRes(res)
	}
}

func mapDeleteLinkRes(res padmark.DeleteLinkRes) error {
	switch typed := res.(type) {
	case *padmark.DeleteLinkNoContent:
		return nil
	case *padmark.DeleteLinkUnauthorized:
		return unauthorizedErr()
	case *padmark.DeleteLinkForbidden:
		return forbiddenErr("link")
	case *padmark.DeleteLinkNotFound:
		return notFoundErr("link")
	case *padmark.DeleteLinkTooManyRequests:
		return rateLimitedErr("link deletes")
	case *padmark.DeleteLinkServiceUnavailable:
		return serviceUnavailableError(padmark.ErrorResponse(*typed))
	case *padmark.DeleteLinkInternalServerError:
		return serverErr()
	default:
		return unexpectedRes(res)
	}
}

// transportError wraps a network-level failure with the remediation the model needs. The
// padmark server may be started later than the agent (docker compose up, dev cycle), so a
// tool call failing with connection refused is the expected shape, not a fatal proxy error.
func transportError(op string, err error) error {
	return fmt.Errorf("%s: %w — is padmark-server running at the configured server URL?", op, err)
}

// Error texts. Each carries the remediation the model needs to self-correct.

func apiStatus(kind, message string) error {
	if message == "" {
		message = "no details provided"
	}

	return fmt.Errorf("%s: %s", kind, message)
}

func unauthorizedErr() error {
	return fmt.Errorf("unauthorized: the server needs a bearer token (--token / %s) or edit_code", EnvToken)
}

func forbiddenErr(kind string) error {
	return fmt.Errorf("forbidden: wrong edit_code for the %s (an edit_code is shown exactly once, at creation)", kind)
}

func notFoundErr(kind string) error {
	return fmt.Errorf("%s not found — wrong ID, already deleted/expired, or a different server", kind)
}

func goneErr(kind string) error {
	return fmt.Errorf("gone: the %s was destroyed after its first read (burn-after-reading with a TTL)", kind)
}

func conflictErr() error {
	return errors.New("conflict: the slug is already in use — pick another slug or omit it to let the server generate one")
}

func rateLimitedErr(op string) error {
	return fmt.Errorf("too many requests: rate limit or failure lockout is active on %s — wait and retry", op)
}

func unsupportedErr() error {
	return errors.New("unsupported content type — send JSON (Content-Type: application/json)")
}

func serverErr() error {
	return errors.New("padmark server error — check the server's logs")
}

func unexpectedRes(res any) error {
	return fmt.Errorf("unexpected response type %T", res)
}
