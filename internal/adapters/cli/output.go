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
	twr := tabwriter.NewWriter(w, twMinWidth, twTabWidth, twPadding, ' ', 0)
	ewr := &errWriter{w: twr, err: nil}

	ewr.printf("id:\t%s\n", note.ID)
	ewr.printf("title:\t%s\n", note.Title)
	ewr.printf("type:\t%s\n", note.ContentType)
	ewr.printf("views:\t%d\n", note.Views)

	if note.BurnAfterReading {
		ewr.printf("burn:\tyes\n")
	}

	if exp, ok := note.ExpiresAt.Get(); ok {
		//nolint:gosmopolitan // CLI tool: show local time for readability
		ewr.printf("expires:\t%s\n", exp.Local().Format(time.DateTime))
	}

	//nolint:gosmopolitan // CLI tool: show local time for readability
	ewr.printf("created:\t%s\n", note.CreatedAt.Local().Format(time.DateTime))
	//nolint:gosmopolitan // CLI tool: show local time for readability
	ewr.printf("updated:\t%s\n", note.UpdatedAt.Local().Format(time.DateTime))

	if ewr.err != nil {
		return fmt.Errorf("write note: %w", ewr.err)
	}

	flushErr := twr.Flush()
	if flushErr != nil {
		return fmt.Errorf("flush: %w", flushErr)
	}

	content := &errWriter{w: w, err: nil}
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

// printLink prints a link's metadata (expand). Mirrors printNote's shape, minus the note-only
// fields (title, content) — a link's only "content" is its target URL.
func printLink(w io.Writer, link *padmark.LinkResponse) error {
	twr := tabwriter.NewWriter(w, twMinWidth, twTabWidth, twPadding, ' ', 0)
	ewr := &errWriter{w: twr, err: nil}

	ewr.printf("id:\t%s\n", link.ID)
	ewr.printf("target_url:\t%s\n", link.TargetURL.String())
	ewr.printf("views:\t%d\n", link.Views)

	if link.BurnAfterClick {
		ewr.printf("burn:\tyes\n")
	}

	if exp, ok := link.ExpiresAt.Get(); ok {
		//nolint:gosmopolitan // CLI tool: show local time for readability
		ewr.printf("expires:\t%s\n", exp.Local().Format(time.DateTime))
	}

	if dr, ok := link.DirectRedirect.Get(); ok {
		ewr.printf("direct-redirect:\t%t (overrides the server default)\n", dr)
	}

	//nolint:gosmopolitan // CLI tool: show local time for readability
	ewr.printf("created:\t%s\n", link.CreatedAt.Local().Format(time.DateTime))

	if ewr.err != nil {
		return fmt.Errorf("write link: %w", ewr.err)
	}

	flushErr := twr.Flush()
	if flushErr != nil {
		return fmt.Errorf("flush: %w", flushErr)
	}

	return nil
}

func printLinkJSON(w io.Writer, link *padmark.LinkResponse) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	err := enc.Encode(link)
	if err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}

	return nil
}

func printCreateLinkResult(w io.Writer, serverURL string, link *padmark.CreateLinkResponse) error {
	twr := tabwriter.NewWriter(w, twMinWidth, twTabWidth, twPadding, ' ', 0)
	erw := &errWriter{w: twr, err: nil}

	erw.printf("id:\t%s\n", link.ID)
	erw.printf("url:\t%s/r/%s\n", serverURL, link.ID)
	erw.printf("target_url:\t%s\n", link.TargetURL.String())
	erw.printf("edit-code:\t%s\n", link.EditCode)

	if link.BurnAfterClick {
		erw.printf("burn:\tyes\n")
	}

	if exp, ok := link.ExpiresAt.Get(); ok {
		//nolint:gosmopolitan // CLI tool: show local time for readability
		erw.printf("expires:\t%s\n", exp.Local().Format(time.DateTime))
	}

	if dr, ok := link.DirectRedirect.Get(); ok {
		erw.printf("direct-redirect:\t%t (overrides the server default)\n", dr)
	}

	if erw.err != nil {
		return fmt.Errorf("write result: %w", erw.err)
	}

	flushErr := twr.Flush()
	if flushErr != nil {
		return fmt.Errorf("flush: %w", flushErr)
	}

	return nil
}

func printCreateResult(w io.Writer, serverURL string, note *padmark.CreateNoteResponse) error {
	twr := tabwriter.NewWriter(w, twMinWidth, twTabWidth, twPadding, ' ', 0)
	erw := &errWriter{w: twr, err: nil}

	erw.printf("id:\t%s\n", note.ID)
	erw.printf("url:\t%s/%s\n", serverURL, note.ID)
	erw.printf("edit-code:\t%s\n", note.EditCode)

	if note.BurnAfterReading {
		erw.printf("burn:\tyes\n")
	}

	if exp, ok := note.ExpiresAt.Get(); ok {
		//nolint:gosmopolitan // CLI tool: show local time for readability
		erw.printf("expires:\t%s\n", exp.Local().Format(time.DateTime))
	}

	if erw.err != nil {
		return fmt.Errorf("write result: %w", erw.err)
	}

	flushErr := twr.Flush()
	if flushErr != nil {
		return fmt.Errorf("flush: %w", flushErr)
	}

	return nil
}
