package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	urcli "github.com/urfave/cli/v3"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

func editCommand() *urcli.Command {
	return &urcli.Command{
		Name:      "edit",
		Usage:     "Update an existing note",
		ArgsUsage: "<id>",
		Flags: []urcli.Flag{
			&urcli.StringFlag{
				Name:    FlagEditCode,
				Aliases: []string{"e"},
				Sources: urcli.EnvVars(EnvEditCode),
				Usage:   "Edit code returned at note creation (env: PADMARK_EDIT_CODE)",
			},
			&urcli.StringFlag{
				Name:    FlagTitle,
				Aliases: []string{"t"},
				Usage:   "New title (derived from the first non-empty line of content if omitted)",
			},
			&urcli.StringFlag{
				Name:    FlagContent,
				Aliases: []string{"c"},
				Usage:   "New content (reads from stdin if neither --content nor --file is set)",
			},
			&urcli.StringFlag{
				Name:    FlagFile,
				Aliases: []string{"f"},
				Usage:   "Read new content from `file`",
			},
			&urcli.BoolFlag{
				Name:  FlagPlain,
				Usage: "Set content type to text/plain",
			},
			&urcli.BoolFlag{
				Name:  FlagBurn,
				Usage: "Enable burn-after-reading",
			},
			&urcli.Int64Flag{
				Name:  FlagTTL,
				Usage: "Grace period in seconds after first read (only with --burn)",
			},
		},
		Action: editAction,
	}
}

func editAction(ctx context.Context, cmd *urcli.Command) error {
	id, editCode, err := editActionArgs(cmd)
	if err != nil {
		return err
	}

	content, err := readContent(cmd)
	if err != nil {
		return err
	}

	if strings.TrimSpace(content) == "" {
		return errors.New("content must not be empty")
	}

	req := buildUpdateReq(cmd, content, editCode)

	cl, err := newPadmarkClient(cmd)
	if err != nil {
		return err
	}

	res, err := cl.UpdateNote(ctx, req, padmark.UpdateNoteParams{ID: id})
	if err != nil {
		return fmt.Errorf("update note: %w", err)
	}

	note, apiErr := handleUpdateNoteRes(res)
	if apiErr != nil {
		return apiErr
	}

	return printNote(os.Stdout, note)
}

func editActionArgs(cmd *urcli.Command) (id, editCode string, err error) {
	id, err = noteIDArg(cmd)
	if err != nil {
		return "", "", err
	}

	editCode = cmd.String(FlagEditCode)
	if editCode == "" {
		return "", "", errors.New("--edit-code is required (or set PADMARK_EDIT_CODE)")
	}

	return id, editCode, nil
}

func buildUpdateReq(cmd *urcli.Command, content, editCode string) *padmark.UpdateNoteRequest {
	title := cmd.String(FlagTitle)
	if title == "" {
		title = firstLine(content)
	}

	req := &padmark.UpdateNoteRequest{
		Title:    title,
		Content:  content,
		EditCode: editCode,
	}

	if cmd.Bool(FlagPlain) {
		req.ContentType = padmark.NewOptUpdateNoteRequestContentType(
			padmark.UpdateNoteRequestContentTypeTextPlain,
		)
	}

	if cmd.IsSet(FlagBurn) {
		req.BurnAfterReading = padmark.NewOptBool(cmd.Bool(FlagBurn))
	}

	if ttl := cmd.Int64(FlagTTL); ttl > 0 {
		req.TTL = padmark.NewOptInt64(ttl)
	}

	return req
}

func handleUpdateNoteRes(res padmark.UpdateNoteRes) (*padmark.NoteResponse, error) {
	switch typed := res.(type) {
	case *padmark.NoteResponse:
		return typed, nil
	case *padmark.UpdateNoteBadRequest:
		return nil, fmt.Errorf("bad request: %s", typed.Message)
	case *padmark.UpdateNoteUnauthorized:
		return nil, fmt.Errorf("unauthorized: %s", typed.Message)
	case *padmark.UpdateNoteForbidden:
		return nil, errors.New("forbidden: wrong edit code")
	case *padmark.UpdateNoteNotFound:
		return nil, fmt.Errorf("not found: %s", typed.Message)
	case *padmark.UpdateNoteUnprocessableEntity:
		return nil, fmt.Errorf("validation error: %s", typed.Message)
	case *padmark.UpdateNoteInternalServerError:
		return nil, fmt.Errorf("server error: %s", typed.Message)
	default:
		return nil, fmt.Errorf("unexpected response type: %T", res)
	}
}
