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

const (
	// firstLineScanLimit caps how many lines we inspect when deriving a title from content.
	firstLineScanLimit = 20

	argsUsageID   = "<id>"
	untitledTitle = "Untitled"
)

func createCommand() *urcli.Command {
	return &urcli.Command{
		Name:      "create",
		Usage:     "Create a new note",
		ArgsUsage: " ",
		Flags: []urcli.Flag{
			&urcli.StringFlag{
				Name:    FlagTitle,
				Aliases: []string{"t"},
				Usage:   "Note title (derived from the first non-empty line of content if omitted)",
			},
			&urcli.StringFlag{
				Name:    FlagContent,
				Aliases: []string{"c"},
				Usage:   "Note content (reads from stdin if neither --content nor --file is set)",
			},
			&urcli.StringFlag{
				Name:    FlagFile,
				Aliases: []string{"f"},
				Usage:   "Read note content from `file`",
			},
			&urcli.StringFlag{
				Name:  FlagSlug,
				Usage: "Custom URL slug (letters, digits, hyphens, underscores)",
			},
			&urcli.BoolFlag{
				Name:  FlagPlain,
				Usage: "Use text/plain content type instead of text/markdown",
			},
			&urcli.BoolFlag{
				Name:  FlagPrivate,
				Usage: "Require authentication (any bearer token or session) to read the note",
			},
			&urcli.BoolFlag{
				Name:  FlagBurn,
				Usage: "Burn after reading: delete on first read (add --ttl for a grace period)",
			},
			&urcli.Int64Flag{
				Name:  FlagTTL,
				Usage: "Seconds the note survives after the first read (only with --burn)",
			},
			&urcli.StringFlag{
				Name:    FlagEditCode,
				Aliases: []string{"e"},
				Usage:   "Custom edit code (random 12-char code generated if omitted)",
				Sources: urcli.EnvVars(EnvEditCode),
			},
		},
		Action: createAction,
	}
}

// validateBurnTTL enforces the documented contract that --ttl (a grace period after the first
// read) is only meaningful together with --burn. Without this, `create` silently dropped a
// lone --ttl while `edit` silently sent it — an inconsistency. Both now reject it explicitly.
func validateBurnTTL(cmd *urcli.Command) error {
	if cmd.IsSet(FlagTTL) && !cmd.Bool(FlagBurn) {
		return errors.New("--ttl requires --burn (it sets the grace period after the first read)")
	}

	return nil
}

func createAction(ctx context.Context, cmd *urcli.Command) error {
	err := validateBurnTTL(cmd)
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

	req := buildCreateReq(cmd, content)

	cl, err := newPadmarkClient(cmd)
	if err != nil {
		return err
	}

	res, err := cl.CreateNote(ctx, req)
	if err != nil {
		return fmt.Errorf("create note: %w", err)
	}

	note, apiErr := handleCreateNoteRes(res)
	if apiErr != nil {
		return apiErr
	}

	return printCreateResult(os.Stdout, resolveServerURL(cmd), note)
}

func buildCreateReq(cmd *urcli.Command, content string) *padmark.CreateNoteRequest {
	title := cmd.String(FlagTitle)
	if title == "" {
		title = firstLine(content)
	}

	req := &padmark.CreateNoteRequest{Content: content}
	if title != "" {
		req.Title = padmark.NewOptString(title)
	}

	if cmd.Bool(FlagPlain) {
		req.ContentType = padmark.NewOptCreateNoteRequestContentType(
			padmark.CreateNoteRequestContentTypeTextPlain,
		)
	}

	if slug := cmd.String(FlagSlug); slug != "" {
		req.Slug = padmark.NewOptString(slug)
	}

	if cmd.Bool(FlagPrivate) {
		req.Private = padmark.NewOptBool(true)
	}

	if ec := cmd.String(FlagEditCode); ec != "" {
		req.EditCode = padmark.NewOptString(ec)
	}

	if cmd.Bool(FlagBurn) {
		req.BurnAfterReading = padmark.NewOptBool(true)

		if ttl := cmd.Int64(FlagTTL); ttl > 0 {
			req.TTL = padmark.NewOptInt64(ttl)
		}
	}

	return req
}

func handleCreateNoteRes(res padmark.CreateNoteRes) (*padmark.CreateNoteResponse, error) {
	switch typed := res.(type) {
	case *padmark.CreateNoteResponseHeaders:
		resp := typed.Response

		return &resp, nil
	case *padmark.CreateNoteBadRequest:
		return nil, fmt.Errorf("bad request: %s", typed.Message)
	case *padmark.CreateNoteUnauthorized:
		return nil, fmt.Errorf("unauthorized: %s", typed.Message)
	case *padmark.CreateNoteConflict:
		return nil, fmt.Errorf("slug conflict: %s", typed.Message)
	case *padmark.CreateNoteRequestEntityTooLarge:
		return nil, fmt.Errorf("content too large: %s", typed.Message)
	case *padmark.CreateNoteUnprocessableEntity:
		return nil, fmt.Errorf("validation error: %s", typed.Message)
	case *padmark.CreateNoteInternalServerError:
		return nil, fmt.Errorf("server error: %s", typed.Message)
	default:
		return nil, fmt.Errorf("unexpected response type: %T", res)
	}
}

// firstLine returns the first non-empty, non-heading line of content as a title fallback.
func firstLine(content string) string {
	for _, line := range strings.SplitN(content, "\n", firstLineScanLimit) {
		trimmed := strings.TrimLeft(line, "#")
		if len(trimmed) < len(line) {
			trimmed = strings.TrimPrefix(trimmed, " ")
		}

		line = strings.TrimSpace(trimmed)

		if line != "" {
			return line
		}
	}

	return untitledTitle
}
