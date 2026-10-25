package cli

import (
	"context"
	"fmt"
	"os"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/errorcontext"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

func getCommand() *urcli.Command {
	return &urcli.Command{
		Name:      "get",
		Usage:     "Get a note by ID",
		ArgsUsage: argsUsageID,
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
		return fmt.Errorf("read note ID: %w", err)
	}

	cl, err := newPadmarkClient(cmd)
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	res, err := cl.GetNote(ctx, padmark.GetNoteParams{ID: id})
	if err != nil {
		return fmt.Errorf("get note: %w", err)
	}

	note, apiErr := handleGetNoteRes(res)
	if apiErr != nil {
		return fmt.Errorf("decode get note response: %w", apiErr)
	}

	switch {
	case cmd.Bool(FlagRaw):
		ew := &errWriter{w: os.Stdout, err: nil}
		ew.printf("%s\n", note.Content)

		if ew.err != nil {
			return fmt.Errorf("write: %w", ew.err)
		}
	case cmd.Bool(FlagJSON):
		return errorcontext.Wrap(printNoteJSON(os.Stdout, note), "print note json")
	default:
		return errorcontext.Wrap(printNote(os.Stdout, note), "print note")
	}

	return nil
}

func handleGetNoteRes(res padmark.GetNoteRes) (*padmark.NoteResponse, error) {
	switch typed := res.(type) {
	case *padmark.NoteResponse:
		return typed, nil
	case *padmark.GetNoteUnauthorized:
		return nil, errNoteReadCredentialRequired
	case *padmark.GetNoteNotFound:
		return nil, ErrNoteNotFound
	case *padmark.GetNoteGone:
		return nil, ErrNoteGone
	case *padmark.GetNoteConflict:
		// The client always sends Accept: application/json; a 409 means something on the way
		// stripped it. The burn-after-reading note was not consumed.
		return nil, fmt.Errorf("%w: %s — the note is still intact", ErrBurnReadRefused, typed.Message)
	case *padmark.GetNoteServiceUnavailable:
		return nil, errorcontext.Wrap(serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode get note response")
	case *padmark.GetNoteInternalServerError:
		return nil, fmt.Errorf("%w: %s", ErrServer, typed.Message)
	default:
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResponse, res)
	}
}
