package cli

import (
	"context"
	"fmt"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/errorcontext"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

// deleteLinkCommand is defined in delete_link.go (underscore, matching this package's existing
// single/multi-word filename convention — create.go, get.go, edit.go, delete.go — rather than a
// literal hyphenated "delete-link.go"); the command name itself is still "delete-link", matching
// the acceptance criteria's `padmark-cli delete-link <slug> --edit-code ...` usage.
func deleteLinkCommand() *urcli.Command {
	return newDeleteCommand("delete-link", "Delete a short link", "link", deleteLinkAction)
}

// deleteLinkAction does not require --edit-code: the server also accepts the request when
// --token identifies the link's owner, mirroring deleteAction's note-side contract exactly. The
// shared "resolve ID → delete → print confirmation" skeleton lives in runDeleteCommand; only the
// ogen-forced per-operation bits (params/response types, client method) stay here and in
// delete.go.
func deleteLinkAction(ctx context.Context, cmd *urcli.Command) error {
	deleteLink := func(ctx context.Context, padmarkClient *padmark.Client, id, editCode string) error {
		params := padmark.DeleteLinkParams{
			ID:        id,
			XEditCode: padmark.OptString{Value: "", Set: false},
		}

		if editCode != "" {
			params.XEditCode = padmark.NewOptString(editCode)
		}

		res, err := padmarkClient.DeleteLink(ctx, params)
		if err != nil {
			return fmt.Errorf("delete link: %w", err)
		}

		return errorcontext.Wrap(handleDeleteLinkRes(res), "decode delete link response")
	}

	return errorcontext.Wrap(runDeleteCommand(ctx, cmd, linkIDArg, deleteLink), "delete resource")
}

func handleDeleteLinkRes(res padmark.DeleteLinkRes) error {
	switch typed := res.(type) {
	case *padmark.DeleteLinkNoContent:
		return nil
	case *padmark.DeleteLinkUnauthorized:
		return ErrUnauthorized
	case *padmark.DeleteLinkForbidden:
		return errLinkOwnershipRequired
	case *padmark.DeleteLinkNotFound:
		return ErrLinkNotFound
	case *padmark.DeleteLinkTooManyRequests:
		return ErrRateLimited
	case *padmark.DeleteLinkServiceUnavailable:
		return errorcontext.Wrap(
			serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode service unavailable response",
		)
	case *padmark.DeleteLinkInternalServerError:
		return fmt.Errorf("%w: %s", ErrServer, typed.Message)
	default:
		return fmt.Errorf("%w: %T", ErrUnexpectedResponse, res)
	}
}
