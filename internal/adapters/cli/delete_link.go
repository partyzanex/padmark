package cli //nolint:dupl // whole-file shape mirrors delete.go; see deleteLinkAction's comment

import (
	"context"
	"errors"
	"fmt"

	urcli "github.com/urfave/cli/v3"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// deleteLinkCommand is defined in delete_link.go (underscore, matching this package's existing
// single/multi-word filename convention — create.go, get.go, edit.go, delete.go — rather than a
// literal hyphenated "delete-link.go"); the command name itself is still "delete-link", matching
// the acceptance criteria's `padmark-cli delete-link <slug> --edit-code ...` usage.
func deleteLinkCommand() *urcli.Command {
	return &urcli.Command{
		Name:      "delete-link",
		Usage:     "Delete a short link",
		ArgsUsage: argsUsageID,
		Flags: []urcli.Flag{
			&urcli.StringFlag{
				Name:    FlagEditCode,
				Aliases: []string{"e"},
				Sources: urcli.EnvVars(EnvEditCode),
				Usage: "Edit code returned at link creation (env: PADMARK_EDIT_CODE). Optional if " +
					"--token identifies the link's owner",
			},
		},
		Action: deleteLinkAction,
	}
}

// deleteLinkAction does not require --edit-code: the server also accepts the request when
// --token identifies the link's owner, mirroring deleteAction's note-side contract exactly. The
// shared "resolve ID → delete → print confirmation" skeleton lives in runDeleteCommand; only the
// ogen-forced per-operation bits (params/response types, client method) stay here and in
// delete.go, which is why the two files still read as near-duplicates to the dupl linter — see
// the package-level nolint above.
func deleteLinkAction(ctx context.Context, cmd *urcli.Command) error {
	deleteLink := func(ctx context.Context, padmarkClient *padmark.Client, id, editCode string) error {
		params := padmark.DeleteLinkParams{ID: id}

		if editCode != "" {
			params.XEditCode = padmark.NewOptString(editCode)
		}

		res, err := padmarkClient.DeleteLink(ctx, params)
		if err != nil {
			return fmt.Errorf("delete link: %w", err)
		}

		return handleDeleteLinkRes(res)
	}

	return runDeleteCommand(ctx, cmd, linkIDArg, deleteLink)
}

func handleDeleteLinkRes(res padmark.DeleteLinkRes) error {
	switch typed := res.(type) {
	case *padmark.DeleteLinkNoContent:
		return nil
	case *padmark.DeleteLinkUnauthorized:
		return fmt.Errorf("unauthorized: %s", typed.Message)
	case *padmark.DeleteLinkForbidden:
		return errors.New("forbidden: wrong edit code")
	case *padmark.DeleteLinkNotFound:
		return errors.New("link not found")
	case *padmark.DeleteLinkInternalServerError:
		return fmt.Errorf("server error: %s", typed.Message)
	default:
		return fmt.Errorf("unexpected response type: %T", res)
	}
}
