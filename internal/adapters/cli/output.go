package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// All text output of the three kinds of record goes through printFields, and all JSON output
// through printJSON. A record printer only selects its fields.

const (
	twMinWidth  = 0
	twTabWidth  = 0
	twPadding   = 2
	decimalBase = 10
)

// Labels of the text output that more than one record printer uses.
const (
	labelID       = "id"
	labelURL      = "url"
	labelEditCode = "edit-code"
	labelCreated  = "created"
	labelDownload = "download"
	labelSize     = "size"
	yes           = "yes"
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

// field is one "label: value" line of the text output.
type field struct {
	label string
	value string
}

// optionalTime is an optional timestamp of a generated response, for example OptNilDateTime.
type optionalTime interface {
	Get() (v time.Time, ok bool)
}

// printFields writes fields as aligned "label:  value" lines. what names the record in the
// error of a failed write.
func printFields(w io.Writer, what string, fields []field) error {
	twr := tabwriter.NewWriter(w, twMinWidth, twTabWidth, twPadding, ' ', 0)
	ewr := &errWriter{w: twr, err: nil}

	for idx := range fields {
		ewr.printf("%s:\t%s\n", fields[idx].label, fields[idx].value)
	}

	if ewr.err != nil {
		return fmt.Errorf("write %s: %w", what, ewr.err)
	}

	flushErr := twr.Flush()
	if flushErr != nil {
		return fmt.Errorf("flush: %w", flushErr)
	}

	return nil
}

// printJSON writes a response of the API as indented JSON for --json.
func printJSON(w io.Writer, response any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	err := enc.Encode(response)
	if err != nil {
		return fmt.Errorf("encode JSON: %w", err)
	}

	return nil
}

// timeText formats a timestamp of the text output.
func timeText(stamp time.Time) string {
	return stamp.UTC().Format(time.RFC3339)
}

// burnField is the line of a record that is deleted after its first use.
func burnField() field {
	return field{label: "burn", value: yes}
}

// withExpiry adds the expiry line when the record has an expiry.
func withExpiry(fields []field, expiresAt optionalTime) []field {
	exp, ok := expiresAt.Get()
	if !ok {
		return fields
	}

	return append(fields, field{label: "expires", value: timeText(exp)})
}

// withDirectRedirect adds the line of a link that overrides the server redirect default.
func withDirectRedirect(fields []field, direct padmark.OptNilBool) []field {
	value, ok := direct.Get()
	if !ok {
		return fields
	}

	return append(fields, field{
		label: "direct-redirect", value: strconv.FormatBool(value) + " (overrides the server default)",
	})
}

func printNote(w io.Writer, note *padmark.NoteResponse) error {
	fields := []field{
		{label: labelID, value: note.ID},
		{label: "title", value: note.Title},
		{label: "type", value: string(note.ContentType)},
		{label: "views", value: strconv.Itoa(note.Views)},
	}

	if note.BurnAfterReading {
		fields = append(fields, burnField())
	}

	if note.ShowContents.Or(false) {
		fields = append(fields, field{label: "contents", value: yes})
	}

	fields = withExpiry(fields, note.ExpiresAt)
	fields = append(fields,
		field{label: labelCreated, value: timeText(note.CreatedAt)},
		field{label: "updated", value: timeText(note.UpdatedAt)})

	err := printFields(w, "note", fields)
	if err != nil {
		return err
	}

	content := &errWriter{w: w, err: nil}
	content.printf("\n%s\n", note.Content)

	if content.err != nil {
		return fmt.Errorf("write content: %w", content.err)
	}

	return nil
}

// printLink prints a link's metadata (expand and update-link). It has the shape of printNote
// without the note-only fields (title, content): the target URL is the "content" of a link.
func printLink(w io.Writer, link *padmark.LinkResponse) error {
	fields := []field{
		{label: labelID, value: link.ID},
		{label: "target_url", value: link.TargetURL.String()},
		{label: "views", value: strconv.Itoa(link.Views)},
	}

	if link.BurnAfterClick {
		fields = append(fields, burnField())
	}

	fields = withExpiry(fields, link.ExpiresAt)
	fields = withDirectRedirect(fields, link.DirectRedirect)
	fields = append(fields, field{label: labelCreated, value: timeText(link.CreatedAt)})

	return printFields(w, "link", fields)
}

func printCreateLinkResult(w io.Writer, serverURL string, link *padmark.CreateLinkResponse) error {
	fields := []field{
		{label: labelID, value: link.ID},
		{label: labelURL, value: serverURL + "/" + link.ID},
		{label: "target_url", value: link.TargetURL.String()},
		{label: labelEditCode, value: link.EditCode},
	}

	if link.BurnAfterClick {
		fields = append(fields, burnField())
	}

	fields = withExpiry(fields, link.ExpiresAt)
	fields = withDirectRedirect(fields, link.DirectRedirect)

	return printFields(w, "result", fields)
}

func printCreateResult(w io.Writer, serverURL string, note *padmark.CreateNoteResponse) error {
	fields := []field{
		{label: labelID, value: note.ID},
		{label: labelURL, value: serverURL + "/" + note.ID},
		{label: labelEditCode, value: note.EditCode},
	}

	if note.BurnAfterReading {
		fields = append(fields, burnField())
	}

	fields = withExpiry(fields, note.ExpiresAt)

	return printFields(w, "result", fields)
}

// filesPathSegment is the first path segment of the file API.
const filesPathSegment = "files"

// fileDownloadURL returns the download address of a file. The upload response has no
// download_url field, so the CLI builds it from the server URL that it used for the request.
func fileDownloadURL(serverURL, id string) string {
	return strings.TrimRight(serverURL, "/") + "/" + filesPathSegment + "/" + url.PathEscape(id) + "/download"
}

// printCreateFileResult prints the result of `upload`. The edit code is in the output because
// the server returns it only once.
func printCreateFileResult(w io.Writer, serverURL string, file *padmark.CreateFileResponse) error {
	fields := []field{
		{label: labelID, value: file.ID},
		{label: labelURL, value: file.URL.String()},
		{label: labelDownload, value: fileDownloadURL(serverURL, file.ID)},
		{label: "name", value: file.Name},
		{label: labelSize, value: strconv.FormatInt(file.Size, decimalBase)},
		{label: labelEditCode, value: file.EditCode},
	}

	if file.BurnAfterDownload {
		fields = append(fields, burnField())
	}

	fields = withExpiry(fields, file.ExpiresAt)

	return printFields(w, "result", fields)
}

// printFile prints the metadata of a file (file-info and update-file).
func printFile(w io.Writer, serverURL string, file *padmark.FileInfo) error {
	fields := []field{
		{label: labelID, value: file.ID},
		{label: labelURL, value: file.URL.String()},
		{label: labelDownload, value: fileDownloadURL(serverURL, file.ID)},
		{label: "name", value: file.Name},
		{label: labelSize, value: strconv.FormatInt(file.Size, decimalBase)},
		{label: "privacy", value: string(file.Privacy)},
		{label: "downloads", value: strconv.Itoa(file.Downloads)},
	}

	if file.BurnAfterDownload {
		fields = append(fields, burnField())
	}

	fields = withExpiry(fields, file.ExpiresAt)
	fields = append(fields, field{label: labelCreated, value: timeText(file.CreatedAt)})

	return printFields(w, "file", fields)
}
