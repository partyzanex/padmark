package cli //nolint:dupl // whole-file shape mirrors delete_link.go; see deleteAction's comment

import (
	"context"
	"errors"
	"fmt"

	urcli "github.com/urfave/cli/v3"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

func deleteCommand() *urcli.Command {
	return &urcli.Command{
		Name:      "delete",
		Usage:     "Delete a note",
		ArgsUsage: argsUsageID,
		Flags: []urcli.Flag{
			&urcli.StringFlag{
				Name:    FlagEditCode,
				Aliases: []string{"e"},
				Sources: urcli.EnvVars(EnvEditCode),
				Usage: "Edit code returned at note creation (env: PADMARK_EDIT_CODE). Optional if " +
					"--token identifies the note's owner. " + psWarnEditCode,
			},
		},
		Action: deleteAction,
	}
}

// deleteAction does not require --edit-code: the server also accepts the request when --token
// identifies the note's owner, and otherwise rejects an empty/wrong edit code with a clear API
// error — the CLI does not pre-validate which credential the caller intends to rely on. The
// shared "resolve ID → delete → print confirmation" skeleton lives in runDeleteCommand; only the
// ogen-forced per-operation bits (params/response types, client method) stay here and in
// delete_link.go, which is why the two files still read as near-duplicates to the dupl linter —
// see the package-level nolint above.
func deleteAction(ctx context.Context, cmd *urcli.Command) error {
	deleteNote := func(ctx context.Context, padmarkClient *padmark.Client, id, editCode string) error {
		params := padmark.DeleteNoteParams{
			ID:        id,
			XEditCode: padmark.OptString{Value: "", Set: false},
			EditCode:  padmark.OptString{Value: "", Set: false},
		}

		if editCode != "" {
			params.XEditCode = padmark.NewOptString(editCode)
		}

		res, err := padmarkClient.DeleteNote(ctx, params)
		if err != nil {
			return fmt.Errorf("delete note: %w", err)
		}

		return handleDeleteNoteRes(res)
	}

	return runDeleteCommand(ctx, cmd, noteIDArg, deleteNote)
}

func handleDeleteNoteRes(res padmark.DeleteNoteRes) error {
	switch typed := res.(type) {
	case *padmark.DeleteNoteNoContent:
		return nil
	case *padmark.DeleteNoteUnauthorized:
		return errors.New("unauthorized")
	case *padmark.DeleteNoteForbidden:
		return errors.New("forbidden: wrong edit code or --token is not the note's owner")
	case *padmark.DeleteNoteNotFound:
		return errors.New("note not found")
	case *padmark.DeleteNoteServiceUnavailable:
		return serviceUnavailableError(padmark.ErrorResponse(*typed))
	case *padmark.DeleteNoteInternalServerError:
		return fmt.Errorf("server error: %s", typed.Message)
	default:
		return fmt.Errorf("unexpected response type: %T", res)
	}
}
