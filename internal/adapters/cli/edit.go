package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	urcli "github.com/urfave/cli/v3"

	"github.com/partyzanex/padmark/internal/errorcontext"
	padmark "github.com/partyzanex/padmark/pkg/client"
)

func editCommand() *urcli.Command {
	return &urcli.Command{
		Name:      "edit",
		Usage:     "Update an existing note",
		ArgsUsage: argsUsageID,
		Flags: []urcli.Flag{
			&urcli.StringFlag{
				Name:    FlagEditCode,
				Aliases: []string{"e"},
				Sources: urcli.EnvVars(EnvEditCode),
				Usage: "Edit code returned at note creation (env: PADMARK_EDIT_CODE). Optional if " +
					"--token identifies the note's owner. " + psWarnEditCode,
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
			&urcli.StringFlag{
				Name:  FlagPrivacy,
				Usage: "Privacy level: public, authenticated, or owner",
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
	err := validateBurnTTL(cmd)
	if err != nil {
		return fmt.Errorf("validate burn TTL: %w", err)
	}

	id, editCode, err := editActionArgs(cmd)
	if err != nil {
		return fmt.Errorf("edit action args: %w", err)
	}

	content, err := readContent(cmd)
	if err != nil {
		return fmt.Errorf("read note content: %w", err)
	}

	if strings.TrimSpace(content) == "" {
		return ErrEmptyContent
	}

	req := buildUpdateReq(cmd, content, editCode)

	cl, err := newPadmarkClient(cmd)
	if err != nil {
		return fmt.Errorf("create API client: %w", err)
	}

	res, err := cl.UpdateNote(ctx, req, padmark.UpdateNoteParams{ID: id})
	if err != nil {
		return fmt.Errorf("update note: %w", err)
	}

	note, apiErr := handleUpdateNoteRes(res)
	if apiErr != nil {
		return fmt.Errorf("decode update note response: %w", apiErr)
	}

	return errorcontext.Wrap(printNote(os.Stdout, note), "print note")
}

// editActionArgs resolves the note ID and edit code. editCode may come back empty: the server
// still accepts the request when --token identifies the note's owner (see buildUpdateReq), and
// otherwise rejects it with a clear "invalid edit code" API error — the CLI does not pre-validate
// which credential the caller intends to rely on.
func editActionArgs(cmd *urcli.Command) (id, editCode string, err error) {
	id, err = noteIDArg(cmd)
	if err != nil {
		return "", "", fmt.Errorf("read note ID: %w", err)
	}

	return id, cmd.String(FlagEditCode), nil
}

func buildUpdateReq(cmd *urcli.Command, content, editCode string) *padmark.UpdateNoteRequest {
	title := cmd.String(FlagTitle)
	if title == "" {
		title = firstLine(content)
	}

	req := &padmark.UpdateNoteRequest{
		// Opt fields zeroed: the server applies its defaults. Only the flags the CLI explicitly
		// reads (--title, --edit-code, --burn, --ttl, --privacy) populate these later.
		Title:            padmark.OptString{Value: "", Set: false},
		Content:          content,
		ContentType:      padmark.OptUpdateNoteRequestContentType{Value: "", Set: false},
		EditCode:         padmark.OptString{Value: "", Set: false},
		BurnAfterReading: padmark.OptBool{Value: false, Set: false},
		TTL:              padmark.OptNilInt64{Value: 0, Set: false, Null: false},
		Privacy:          padmark.OptUpdateNoteRequestPrivacy{Value: "", Set: false},
		Private:          padmark.OptBool{Value: false, Set: false}, //nolint:staticcheck // deprecated alias, unused
	}
	if editCode != "" {
		req.EditCode = padmark.NewOptString(editCode)
	}

	if title != "" {
		req.Title = padmark.NewOptString(title)
	}

	if cmd.Bool(FlagPlain) {
		req.ContentType = padmark.NewOptUpdateNoteRequestContentType(
			padmark.UpdateNoteRequestContentTypeTextPlain,
		)
	}

	if cmd.IsSet(FlagPrivacy) {
		req.Privacy = padmark.NewOptUpdateNoteRequestPrivacy(padmark.UpdateNoteRequestPrivacy(cmd.String(FlagPrivacy)))
	}

	if cmd.IsSet(FlagBurn) {
		req.BurnAfterReading = padmark.NewOptBool(cmd.Bool(FlagBurn))
	}

	// TTL only with --burn (enforced by validateBurnTTL), matching `create`'s contract.
	if cmd.Bool(FlagBurn) {
		if ttl := cmd.Int64(FlagTTL); ttl > 0 {
			req.TTL = padmark.NewOptNilInt64(ttl)
		}
	}

	return req
}

func handleUpdateNoteRes(res padmark.UpdateNoteRes) (*padmark.NoteResponse, error) {
	switch typed := res.(type) {
	case *padmark.NoteResponse:
		return typed, nil
	case *padmark.UpdateNoteBadRequest:
		return nil, fmt.Errorf("%w: %s", ErrBadRequest, typed.Message)
	case *padmark.UpdateNoteUnauthorized:
		return nil, ErrUnauthorized
	case *padmark.UpdateNoteForbidden:
		return nil, errWrongEditCode
	case *padmark.UpdateNoteNotFound:
		return nil, ErrNoteNotFound
	case *padmark.UpdateNoteUnprocessableEntity:
		return nil, fmt.Errorf("%w: %s", ErrValidation, typed.Message)
	case *padmark.UpdateNoteServiceUnavailable:
		return nil, errorcontext.Wrap(serviceUnavailableError(padmark.ErrorResponse(*typed)), "decode update note response")
	case *padmark.UpdateNoteInternalServerError:
		return nil, fmt.Errorf("%w: %s", ErrServer, typed.Message)
	default:
		return nil, fmt.Errorf("%w: %T", ErrUnexpectedResponse, res)
	}
}
