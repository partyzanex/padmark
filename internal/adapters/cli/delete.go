package cli

import (
	"context"
	"fmt"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/errorcontext"
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
// delete_link.go.
func deleteAction(ctx context.Context, cmd *urcli.Command) error {
	deleteNote := func(ctx context.Context, padmarkClient *padmark.Client, id, editCode string) error {
		params := padmark.DeleteNoteParams{
			ID:        id,
			XEditCode: padmark.OptString{Value: "", Set: false},
			// The deprecated query form stays unset: the code travels in X-Edit-Code only.
			EditCode: padmark.OptString{Value: "", Set: false}, //nolint:staticcheck // deprecated field, never set
		}

		if editCode != "" {
			params.XEditCode = padmark.NewOptString(editCode)
		}

		res, err := padmarkClient.DeleteNote(ctx, params)
		if err != nil {
			return fmt.Errorf("delete note: %w", err)
		}

		return errorcontext.Wrap(handleDeleteNoteRes(res), "decode delete note response")
	}

	return errorcontext.Wrap(runDeleteCommand(ctx, cmd, noteIDArg, deleteNote), "delete resource")
}

func handleDeleteNoteRes(res padmark.DeleteNoteRes) error {
	switch typed := res.(type) {
	case *padmark.DeleteNoteNoContent:
		return nil
	case *padmark.DeleteNoteUnauthorized:
		return ErrUnauthorized
	case *padmark.DeleteNoteForbidden:
		return errNoteOwnershipRequired
	case *padmark.DeleteNoteNotFound:
		return ErrNoteNotFound
	case *padmark.DeleteNoteServiceUnavailable:
		return errorcontext.Wrap(
			serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode service unavailable response",
		)
	case *padmark.DeleteNoteInternalServerError:
		return fmt.Errorf("%w: %s", ErrServer, typed.Message)
	default:
		return fmt.Errorf("%w: %T", ErrUnexpectedResponse, res)
	}
}
