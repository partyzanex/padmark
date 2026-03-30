package cli

import (
	"context"
	"fmt"
	"os"

	urcli "github.com/urfave/cli/v3"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

func getCommand() *urcli.Command {
	return &urcli.Command{
		Name:      "get",
		Usage:     "Get a note by ID",
		ArgsUsage: "<id>",
		Flags: []urcli.Flag{
			&urcli.BoolFlag{
				Name:  FlagRaw,
				Usage: "Print raw note content only (no metadata)",
			},
			&urcli.BoolFlag{
				Name:  FlagJSON,
				Usage: "Print the note as JSON",
			},
		},
		Action: getAction,
	}
}

func getAction(ctx context.Context, cmd *urcli.Command) error {
	id, err := noteIDArg(cmd)
	if err != nil {
		return err
	}

	cl, err := newPadmarkClient(cmd)
	if err != nil {
		return err
	}

	res, err := cl.GetNote(ctx, padmark.GetNoteParams{ID: id})
	if err != nil {
		return fmt.Errorf("get note: %w", err)
	}

	note, apiErr := handleGetNoteRes(res)
	if apiErr != nil {
		return apiErr
	}

	switch {
	case cmd.Bool(FlagRaw):
		ew := &errWriter{w: os.Stdout}
		ew.printf("%s\n", note.Content)

		if ew.err != nil {
			return fmt.Errorf("write: %w", ew.err)
		}
	case cmd.Bool(FlagJSON):
		return printNoteJSON(os.Stdout, note)
	default:
		return printNote(os.Stdout, note)
	}

	return nil
}

func handleGetNoteRes(res padmark.GetNoteRes) (*padmark.NoteResponse, error) {
	switch typed := res.(type) {
	case *padmark.NoteResponse:
		return typed, nil
	case *padmark.GetNoteNotFound:
		return nil, fmt.Errorf("not found: %s", typed.Message)
	case *padmark.GetNoteGone:
		return nil, fmt.Errorf("gone (note has been consumed or expired): %s", typed.Message)
	case *padmark.GetNoteInternalServerError:
		return nil, fmt.Errorf("server error: %s", typed.Message)
	default:
		return nil, fmt.Errorf("unexpected response type: %T", res)
	}
}
