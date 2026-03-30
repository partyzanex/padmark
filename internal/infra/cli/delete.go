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
		ArgsUsage: "<id>",
		Flags: []urcli.Flag{
			&urcli.StringFlag{
				Name:    FlagEditCode,
				Aliases: []string{"e"},
				Sources: urcli.EnvVars(EnvEditCode),
				Usage:   "Edit code returned at note creation (env: PADMARK_EDIT_CODE)",
			},
		},
		Action: deleteAction,
	}
}

func deleteAction(ctx context.Context, cmd *urcli.Command) error {
	id, err := noteIDArg(cmd)
	if err != nil {
		return err
	}

	editCode := cmd.String(FlagEditCode)
	if editCode == "" {
		return errors.New("--edit-code is required (or set PADMARK_EDIT_CODE)")
	}

	cl, err := newPadmarkClient(cmd)
	if err != nil {
		return err
	}

	params := padmark.DeleteNoteParams{
		ID:        id,
		XEditCode: padmark.NewOptString(editCode),
	}

	res, err := cl.DeleteNote(ctx, params)
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
		return fmt.Errorf("not found: %s", typed.Message)
	case *padmark.DeleteNoteInternalServerError:
		return fmt.Errorf("server error: %s", typed.Message)
	default:
		return fmt.Errorf("unexpected response type: %T", res)
	}
}
