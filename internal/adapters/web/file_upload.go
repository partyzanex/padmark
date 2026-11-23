package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
	"github.com/partyzanex/padmark/internal/adapters/web/ogenapi"
	"github.com/partyzanex/padmark/internal/adapters/web/response"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

// The limits of the upload form.
const (
	// multipartOverhead is the space for the form fields and part headers above the file limit.
	multipartOverhead = 1 << 20
	// maxFormFieldSize limits each text field of the upload form.
	maxFormFieldSize = 1 << 10
	// maxFormFieldsSize limits all bytes before the file data: the preamble, the text fields,
	// the part headers and the read-ahead of the multipart reader (4 KiB). The five fields and a
	// file name of 255 bytes need less than 8 KiB; without this limit, part headers could hold
	// the full upload limit in memory.
	maxFormFieldsSize = 64 << 10
	fileFormField     = "file"
)

// The text fields of the upload form, as in openapi.yaml. Other names give 400.
const (
	uploadFieldTTL      = "ttl"
	uploadFieldPrivacy  = "privacy"
	uploadFieldBurn     = "burn_after_download"
	uploadFieldSlug     = "slug"
	uploadFieldEditCode = "edit_code"
	uploadFieldCount    = 5
)

var (
	errUploadNotMultipart = errors.New("upload must be multipart/form-data with a \"file\" part")
	errUploadField        = errors.New("invalid upload form field")
	errUploadFieldsSize   = errors.New("the form data before the file part is too large")
)

// Upload handles POST /files. The body is multipart/form-data: optional text fields ttl
// (seconds), privacy, burn_after_download, slug and edit_code, then the "file" part. The file
// goes to object storage as it arrives, so the handler reads only the fields before the file.
// A part after the file part fails the upload (uploadBody.finish).
func (h *FileHandler) Upload(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		h.writeError(w, r, domain.ErrFeatureNotSupported)

		return
	}

	limit := h.manager.MaxSize() + multipartOverhead
	// A declared body above the limit cannot succeed, so the handler refuses it before it
	// takes an upload slot or reads the body.
	if r.ContentLength > limit {
		h.writeUploadError(w, r, domain.ErrFileTooLarge)

		return
	}

	client := middleware.ClientIP(r, h.trustedProxies)

	err := h.uploads.acquire(client)
	if err != nil {
		closeAfterResponse(w)
		status := uploadBusyStatus(w, err)
		response.WriteJSONErrorStatus(w, r, h.log, status, err.Error())

		return
	}
	defer h.uploads.release(client)

	r.Body = http.MaxBytesReader(w, r.Body, limit)

	form, err := h.readUploadForm(r)
	if err != nil {
		h.writeUploadError(w, r, err)

		return
	}

	file, err := h.createFile(w, r, form)
	if err != nil {
		h.writeUploadError(w, r, err)

		return
	}

	info := h.toJSON(r, file)
	body := &ogenapi.CreateFileResponse{
		ID: info.ID, URL: info.URL, Name: info.Name, Size: info.Size,
		Privacy:   ogenapi.CreateFileResponsePrivacy(info.Privacy),
		Downloads: info.Downloads, BurnAfterDownload: info.BurnAfterDownload,
		ExpiresAt: info.ExpiresAt, CreatedAt: info.CreatedAt,
		// The edit code is in the upload response only.
		EditCode: file.EditCode,
	}

	w.Header().Set("Location", "/"+file.ID)
	h.writeJSON(w, r, http.StatusCreated, body)
}

// createFile streams the file part to the usecase. Only the file data has the transfer
// deadlines. Until the fields are valid, the server read timeout applies, so a client that
// never sends the file part cannot hold the request.
func (h *FileHandler) createFile(w http.ResponseWriter, r *http.Request, form *uploadForm) (*domain.File, error) {
	deadline := newTransferDeadline(w, h.idleTimeout)
	h.logDeadlineError(r, deadline.uploadStarted())

	body := &uploadBody{
		part: form.part, reader: form.reader, deadline: deadline, err: nil,
		onDeadlineError: func(err error) { h.logDeadlineError(r, err) },
	}

	file, err := h.manager.Create(r.Context(), form.part.FileName(), body, form.opts)
	if err != nil {
		return nil, body.cause(err)
	}

	h.logDeadlineError(r, deadline.responseStarted())

	return file, nil
}

// writeUploadError answers a failed upload. A client error gets a constant text: the error
// chain can hold a parser error that quotes form values, so it is not sent and not logged.
func (h *FileHandler) writeUploadError(w http.ResponseWriter, r *http.Request, err error) {
	closeAfterResponse(w)

	switch {
	// The server cancels the request context after a read timeout too, so this case is first.
	case errors.Is(err, errUploadIdle):
		response.WriteJSONErrorStatus(w, r, h.log, http.StatusRequestTimeout, errUploadIdle.Error())
	case r.Context().Err() != nil, errors.Is(err, context.Canceled):
		// The client went away; nobody reads a response.
		h.log.DebugContext(r.Context(), "file upload canceled")
	case isMaxBytes(err), errors.Is(err, domain.ErrFileTooLarge):
		response.WriteJSONErrorStatus(w, r, h.log, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("%s (limit %d bytes)", domain.ErrFileTooLarge, h.manager.MaxSize()))
	case errors.Is(err, errUploadMalformed):
		response.WriteJSONErrorStatus(w, r, h.log, http.StatusBadRequest, errUploadMalformed.Error())
	case errors.Is(err, errUploadNotMultipart), errors.Is(err, errUploadField),
		errors.Is(err, errUploadFieldsSize), errors.Is(err, errUploadFieldAfterFile):
		response.WriteJSONErrorStatus(w, r, h.log, http.StatusBadRequest, err.Error())
	default:
		h.respondError(w, r, err)
	}
}

// closeAfterResponse tells the server to close the connection after the response. An upload
// error can leave the rest of a large body unread. Without this header, the server reads up to
// 256 KiB of that body before it sends the response, so a client whose upload stalls does not
// get the error.
func closeAfterResponse(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
}

// isMaxBytes reports whether err comes from http.MaxBytesReader.
func isMaxBytes(err error) bool {
	_, ok := errors.AsType[*http.MaxBytesError](err)

	return ok
}

// uploadForm is the upload form up to the file part. The caller reads the file part and then
// the rest of the form from reader.
type uploadForm struct {
	reader *multipart.Reader
	part   *multipart.Part
	opts   files.CreateOptions
}

// readUploadForm reads the text fields and stops at the file part, which the caller streams.
// Before the file part, the body has the small maxFormFieldsSize limit. When the file part
// starts, only the file limit of Upload applies.
func (h *FileHandler) readUploadForm(r *http.Request) (*uploadForm, error) {
	fieldsLimit := &io.LimitedReader{R: r.Body, N: maxFormFieldsSize}
	r.Body = limitedBody{Reader: fieldsLimit, Closer: r.Body}

	reader, err := r.MultipartReader()
	if err != nil {
		return nil, errUploadNotMultipart
	}

	fields, part, err := readUploadFields(reader)

	// The multipart reader keeps the first read error for all later reads. A limit that is not
	// used up thus never gave an error, and the file data can follow. A used-up limit also
	// refuses a form that is just under it, but real clients send much less.
	if fieldsLimit.N <= 0 {
		return nil, errUploadFieldsSize
	}

	if err != nil {
		return nil, err
	}

	fieldsLimit.N = math.MaxInt64

	opts, err := h.uploadOptions(r, fields)
	if err != nil {
		return nil, err
	}

	return &uploadForm{reader: reader, part: part, opts: opts}, nil
}

// limitedBody puts the limit of the upload fields on the request body and keeps its Close.
type limitedBody struct {
	io.Reader
	io.Closer
}

// readUploadFields reads the text parts up to the file part. Each known field can occur once,
// so a form has a maximum of uploadFieldCount parts before the file part.
func readUploadFields(reader *multipart.Reader) (map[string]string, *multipart.Part, error) {
	fields := make(map[string]string, uploadFieldCount)

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, nil, errUploadNotMultipart
		}

		if err != nil {
			return nil, nil, classifyBodyError(err)
		}

		name := part.FormName()
		if name == fileFormField {
			return fields, part, nil
		}

		value, err := readUploadField(part, name, fields)
		if err != nil {
			return nil, nil, err
		}

		fields[name] = value
	}
}

// readUploadField reads the value of the text part name. The error text names only known
// fields: an unknown name is client input and does not go back to the client.
func readUploadField(part *multipart.Part, name string, fields map[string]string) (string, error) {
	if !isUploadField(name) {
		return "", fmt.Errorf("%w: unknown field", errUploadField)
	}

	if _, seen := fields[name]; seen {
		return "", fmt.Errorf("%w: %s is repeated", errUploadField, name)
	}

	value, err := io.ReadAll(io.LimitReader(part, maxFormFieldSize+1))
	if err != nil {
		return "", classifyBodyError(err)
	}

	if len(value) > maxFormFieldSize {
		return "", fmt.Errorf("%w: %s is too long", errUploadField, name)
	}

	return string(value), nil
}

// isUploadField reports whether name is a text field of the upload form.
func isUploadField(name string) bool {
	switch name {
	case uploadFieldTTL, uploadFieldPrivacy, uploadFieldBurn, uploadFieldSlug, uploadFieldEditCode:
		return true
	default:
		return false
	}
}

func (h *FileHandler) uploadOptions(r *http.Request, fields map[string]string) (files.CreateOptions, error) {
	opts := files.CreateOptions{
		ExpiresAt:         nil,
		Privacy:           nil,
		OwnerID:           middleware.OwnerIDFromCtx(r.Context()),
		CustomSlug:        fields[uploadFieldSlug],
		EditCode:          fields[uploadFieldEditCode],
		BurnAfterDownload: false,
	}

	if raw := fields[uploadFieldTTL]; raw != "" {
		ttl, err := strconv.ParseInt(raw, decimalBase, int64Bits)
		if err != nil || ttl < 0 || ttl > maxTTLSeconds {
			return opts, fmt.Errorf("%w: ttl must be 0..%d seconds", errUploadField, maxTTLSeconds)
		}

		expiresAt := time.Now().UTC().Add(time.Duration(ttl) * time.Second)
		opts.ExpiresAt = &expiresAt
	}

	if raw := fields[uploadFieldPrivacy]; raw != "" {
		privacy := domain.Privacy(raw)
		opts.Privacy = &privacy
	}

	if raw := fields[uploadFieldBurn]; raw != "" {
		burn, err := strconv.ParseBool(raw)
		if err != nil {
			return opts, fmt.Errorf("%w: burn_after_download must be true or false", errUploadField)
		}

		opts.BurnAfterDownload = burn
	}

	err := requirePrivacySupport(h.accounts, opts.Privacy)
	if err != nil {
		return opts, err
	}

	return opts, nil
}

// isFileUpload reports whether r is the file upload. The general body limit does not apply to
// it; Upload applies --max-file-size.
func isFileUpload(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/files"
}
