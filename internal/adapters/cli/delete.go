package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

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
					"--token identifies the note's owner",
			},
		},
		Action: deleteAction,
	}
}

// deleteAction does not require --edit-code: the server also accepts the request when --token
// identifies the note's owner, and otherwise rejects an empty/wrong edit code with a clear API
// error — the CLI does not pre-validate which credential the caller intends to rely on.
func deleteAction(ctx context.Context, cmd *urcli.Command) error {
	id, err := noteIDArg(cmd)
	if err != nil {
		return err
	}

	padmarkClient, err := newPadmarkClient(cmd)
	if err != nil {
		return err
	}

	params := padmark.DeleteNoteParams{ID: id}

	if editCode := cmd.String(FlagEditCode); editCode != "" {
		params.XEditCode = padmark.NewOptString(editCode)
	}

	res, err := padmarkClient.DeleteNote(ctx, params)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}

	apiErr := handleDeleteNoteRes(res)
	if apiErr != nil {
		return apiErr
	}

	ew := &errWriter{w: os.Stdout}
	ew.printf("deleted %s\n", id)

	if ew.err != nil {
		return fmt.Errorf("write output: %w", ew.err)
	}

	return nil
}

func handleDeleteNoteRes(res padmark.DeleteNoteRes) error {
	switch typed := res.(type) {
	case *padmark.DeleteNoteNoContent:
		return nil
	case *padmark.DeleteNoteUnauthorized:
		return fmt.Errorf("unauthorized: %s", typed.Message)
	case *padmark.DeleteNoteForbidden:
		return errors.New("forbidden: wrong edit code")
	case *padmark.DeleteNoteNotFound:
		return errors.New("note not found")
	case *padmark.DeleteNoteInternalServerError:
		return fmt.Errorf("server error: %s", typed.Message)
	default:
		return fmt.Errorf("unexpected response type: %T", res)
	}
}
