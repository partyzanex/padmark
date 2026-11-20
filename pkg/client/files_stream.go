package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// This file is not generated. Keep it out of the ogen output names (oas_*_gen.go): ogen --clean
// removes only those files.
//
// The generated client cannot stream files in the two directions:
//   - CreateFile writes the "file" part before the text fields. The server reads only the
//     fields before "file", so it ignores ttl, privacy, slug and the other options.
//   - GetFileContent reads the full response body into memory before it returns.
//
// UploadFile and DownloadFile replace these two operations. Use the generated GetFile,
// UpdateFile and DeleteFile for the other file operations.

const (
	// maxFileJSONSize limits the JSON response of an upload and the error body that the client
	// reads. The server sends a small object; a larger body is not a padmark response.
	maxFileJSONSize = 1 << 20
	uploadFileField = "file"
	mimeOctetStream = "application/octet-stream"
	mimeJSON        = "application/json"
	decimalBase     = 10
)

var (
	// ErrInvalidUploadOptions reports an UploadFileOptions value that the client cannot send.
	ErrInvalidUploadOptions = errors.New("invalid upload options")
	// ErrIncompleteDownload reports a download that stopped before Content-Length bytes.
	// Discard the data written to dst.
	ErrIncompleteDownload = errors.New("incomplete file download")
	errEmptyFileID        = errors.New("file id is empty")
)

// UploadFileOptions holds the optional fields of an upload. The zero value uploads a public
// file without expiry, with a random slug and a random edit code.
type UploadFileOptions struct {
	// TTL is the lifetime of the file, counted from the upload. Zero means no expiry.
	// The server counts whole seconds, so TTL must be zero or at least one second.
	// The client drops the fraction of a second.
	TTL time.Duration
	// Privacy is "public", "authenticated" or "owner". Empty uses the server default (public).
	Privacy string
	// Slug is a custom file ID. Empty makes the server generate a random ID.
	Slug string
	// EditCode is a custom edit code (8 to 128 characters). Empty makes the server generate one.
	EditCode string
	// BurnAfterDownload makes the server delete the file after the first deliberate download.
	BurnAfterDownload bool
}

// DownloadedFile describes a completed download. The values come from the response headers.
type DownloadedFile struct {
	// Name is the file name from Content-Disposition. It is empty when the header has no name.
	// Clean the name before you use it as a local path: it comes from the uploader.
	Name string
	// Size is the number of bytes written to dst.
	Size int64
}

// APIError is a response with an error status from a file operation of UploadFile or
// DownloadFile. Use errors.As to read the status and the machine-readable code.
type APIError struct {
	// Message is the server message, or the HTTP status text when the body has no message.
	Message string
	// Code is the optional machine-readable code, for example "burn_read_not_explicit" or
	// "api_disabled". Compare Code, not Message.
	Code string
	// StatusCode is the HTTP status code of the response.
	StatusCode int
}

// Error returns the status code and the server message.
func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("padmark: status %d: %s (code %s)", e.StatusCode, e.Message, e.Code)
	}

	return fmt.Sprintf("padmark: status %d: %s", e.StatusCode, e.Message)
}

// UploadFile sends POST /files. It streams body to the server through a pipe, so the client
// does not keep the file in memory. The text fields go before the "file" part, as the server
// requires. name becomes the file name on the server.
//
// UploadFile reads body until io.EOF in a different goroutine. It returns only after that
// goroutine stops, so the caller can close body after the return. The request uses the HTTP
// client of the Client (WithClient), so it has the same authorization headers as the generated
// operations. A per-request timeout of that HTTP client also limits the full upload.
//
// Errors are fail-hard. A response with an error status returns *APIError (413 when the file
// is larger than the server limit). A transport or read error is returned with context.
func (c *Client) UploadFile(
	ctx context.Context, name string, body io.Reader, opts UploadFileOptions,
) (*CreateFileResponse, error) {
	fields, err := opts.formFields()
	if err != nil {
		return nil, err
	}

	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	done := make(chan struct{})

	go func() {
		defer close(done)

		writer.CloseWithError(writeUploadForm(form, fields, name, body))
	}()

	// stopForm closes the read end so that the form goroutine stops, also when the server
	// answered before it read the full body. Then it waits, so body is not read after the return.
	stopForm := func() {
		_ = reader.Close()

		<-done
	}

	resp, err := c.sendUpload(ctx, reader, form.FormDataContentType())
	if err != nil {
		stopForm()

		return nil, err
	}

	// Read the response before stopForm: a server that answers early (413) can close the
	// connection when the body stops, and the response body must arrive first.
	created, err := readUploadResponse(resp)

	stopForm()

	return created, err
}

func readUploadResponse(resp *http.Response) (*CreateFileResponse, error) {
	defer closeBody(resp)

	if resp.StatusCode != http.StatusCreated {
		return nil, decodeAPIError(resp)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFileJSONSize))
	if err != nil {
		return nil, fmt.Errorf("read upload response: %w", err)
	}

	var created CreateFileResponse

	err = created.UnmarshalJSON(data)
	if err != nil {
		return nil, fmt.Errorf("decode upload response: %w", err)
	}

	return &created, nil
}

// DownloadFile sends GET /files/{id}/download and copies the content to dst. It does not keep
// the file in memory. The request sends Accept: application/octet-stream, so it is a
// deliberate download: it consumes a burn-after-download file.
//
// The request uses the HTTP client of the Client (WithClient), so it has the same
// authorization headers as the generated operations.
//
// Errors are fail-hard. A response with an error status returns *APIError (for example, 404,
// 401 or 410). When the transfer stops before Content-Length bytes, DownloadFile returns
// ErrIncompleteDownload or the transport error; dst then has part of the file. Discard it.
func (c *Client) DownloadFile(ctx context.Context, id string, dst io.Writer) (*DownloadedFile, error) {
	target, err := c.fileURL(ctx, id, "download")
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}

	req.Header.Set("Accept", mimeOctetStream)

	resp, err := c.cfg.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send download request: %w", err)
	}

	defer closeBody(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, decodeAPIError(resp)
	}

	written, err := io.Copy(dst, resp.Body)
	if err != nil {
		return nil, fmt.Errorf("copy file content: %w", err)
	}

	if resp.ContentLength >= 0 && written != resp.ContentLength {
		return nil, fmt.Errorf("%w: got %d of %d bytes", ErrIncompleteDownload, written, resp.ContentLength)
	}

	return &DownloadedFile{Name: attachmentName(resp.Header.Get("Content-Disposition")), Size: written}, nil
}

func (c *Client) sendUpload(ctx context.Context, body io.Reader, contentType string) (*http.Response, error) {
	target, err := c.fileURL(ctx)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, body)
	if err != nil {
		return nil, fmt.Errorf("create upload request: %w", err)
	}

	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", mimeJSON)

	resp, err := c.cfg.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send upload request: %w", err)
	}

	return resp, nil
}

// fileURL returns the URL of /files, or of /files/{id}/{suffix...} when id is given. JoinPath
// reads each element as an escaped path, so each element is escaped first: a "/" or "%" in id
// stays in one path segment.
func (c *Client) fileURL(ctx context.Context, idAndSuffix ...string) (string, error) {
	if len(idAndSuffix) > 0 && idAndSuffix[0] == "" {
		return "", errEmptyFileID
	}

	segments := make([]string, 0, len(idAndSuffix)+1)
	segments = append(segments, "files")

	for _, segment := range idAndSuffix {
		segments = append(segments, url.PathEscape(segment))
	}

	return c.requestURL(ctx).JoinPath(segments...).String(), nil
}

// formFields returns the text fields in the order the server reads them.
func (o *UploadFileOptions) formFields() ([][2]string, error) {
	var fields [][2]string

	if o.TTL < 0 || (o.TTL > 0 && o.TTL < time.Second) {
		return nil, fmt.Errorf("%w: TTL must be zero or at least one second", ErrInvalidUploadOptions)
	}

	if o.TTL > 0 {
		fields = append(fields, [2]string{"ttl", strconv.FormatInt(int64(o.TTL/time.Second), decimalBase)})
	}

	if o.Privacy != "" {
		fields = append(fields, [2]string{"privacy", o.Privacy})
	}

	if o.BurnAfterDownload {
		fields = append(fields, [2]string{"burn_after_download", "true"})
	}

	if o.Slug != "" {
		fields = append(fields, [2]string{"slug", o.Slug})
	}

	if o.EditCode != "" {
		fields = append(fields, [2]string{"edit_code", o.EditCode})
	}

	return fields, nil
}

// writeUploadForm writes the text fields, then the file part, then the closing boundary.
func writeUploadForm(form *multipart.Writer, fields [][2]string, name string, body io.Reader) error {
	for _, field := range fields {
		err := form.WriteField(field[0], field[1])
		if err != nil {
			return fmt.Errorf("write field %s: %w", field[0], err)
		}
	}

	part, err := form.CreateFormFile(uploadFileField, name)
	if err != nil {
		return fmt.Errorf("create file part: %w", err)
	}

	_, err = io.Copy(part, body)
	if err != nil {
		return fmt.Errorf("copy file content: %w", err)
	}

	err = form.Close()
	if err != nil {
		return fmt.Errorf("close upload form: %w", err)
	}

	return nil
}

// decodeAPIError reads the JSON error body. A body that is not a padmark error keeps the HTTP
// status text as the message.
func decodeAPIError(resp *http.Response) error {
	apiErr := &APIError{Message: http.StatusText(resp.StatusCode), Code: "", StatusCode: resp.StatusCode}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFileJSONSize))
	if err != nil || len(data) == 0 {
		return apiErr
	}

	var body ErrorResponse

	if body.UnmarshalJSON(data) == nil && body.Message != "" {
		apiErr.Message = body.Message
		apiErr.Code = body.Code.Or("")
	}

	return apiErr
}

// attachmentName returns the filename parameter of a Content-Disposition value.
func attachmentName(disposition string) string {
	_, params, err := mime.ParseMediaType(disposition)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(params["filename"])
}

// closeBody drains a small rest of the body so that the connection can be used again.
func closeBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxFileJSONSize))
	_ = resp.Body.Close()
}
