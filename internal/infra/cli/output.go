package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

const (
	twMinWidth = 0
	twTabWidth = 0
	twPadding  = 2
)

// errWriter wraps an io.Writer collecting the first write error.
// All subsequent writes are no-ops once an error is stored.
type errWriter struct {
	w   io.Writer
	err error
}

func (ew *errWriter) printf(format string, args ...any) {
	if ew.err != nil {
		return
	}

	_, ew.err = fmt.Fprintf(ew.w, format, args...)
}

func printNote(w io.Writer, note *padmark.NoteResponse) error {
	tw := tabwriter.NewWriter(w, twMinWidth, twTabWidth, twPadding, ' ', 0)
	ew := &errWriter{w: tw}

	ew.printf("id:\t%s\n", note.ID)
	ew.printf("title:\t%s\n", note.Title)
	ew.printf("type:\t%s\n", note.ContentType)
	ew.printf("views:\t%d\n", note.Views)

	if note.BurnAfterReading {
		ew.printf("burn:\tyes\n")
	}

	if exp, ok := note.ExpiresAt.Get(); ok {
		//nolint:gosmopolitan // CLI tool: show local time for readability
		ew.printf("expires:\t%s\n", exp.Local().Format(time.DateTime))
	}

	//nolint:gosmopolitan // CLI tool: show local time for readability
	ew.printf("created:\t%s\n", note.CreatedAt.Local().Format(time.DateTime))
	//nolint:gosmopolitan // CLI tool: show local time for readability
	ew.printf("updated:\t%s\n", note.UpdatedAt.Local().Format(time.DateTime))

	if ew.err != nil {
		return fmt.Errorf("write note: %w", ew.err)
	}

	flushErr := tw.Flush()
	if flushErr != nil {
		return fmt.Errorf("flush: %w", flushErr)
	}

	content := &errWriter{w: w}
	content.printf("\n%s\n", note.Content)

	if content.err != nil {
		return fmt.Errorf("write content: %w", content.err)
	}

	return nil
}

func printNoteJSON(w io.Writer, note *padmark.NoteResponse) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	err := enc.Encode(note)
	if err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}

	return nil
}

func printCreateResult(w io.Writer, serverURL string, note *padmark.CreateNoteResponse) error {
	tw := tabwriter.NewWriter(w, twMinWidth, twTabWidth, twPadding, ' ', 0)
	ew := &errWriter{w: tw}

	ew.printf("id:\t%s\n", note.ID)
	ew.printf("url:\t%s/%s\n", serverURL, note.ID)
	ew.printf("edit-code:\t%s\n", note.EditCode)

	if note.BurnAfterReading {
		ew.printf("burn:\tyes\n")
	}

	if exp, ok := note.ExpiresAt.Get(); ok {
		//nolint:gosmopolitan // CLI tool: show local time for readability
		ew.printf("expires:\t%s\n", exp.Local().Format(time.DateTime))
	}

	if ew.err != nil {
		return fmt.Errorf("write result: %w", ew.err)
	}

	flushErr := tw.Flush()
	if flushErr != nil {
		return fmt.Errorf("flush: %w", flushErr)
	}

	return nil
}
