//go:generate go run go.uber.org/mock/mockgen@latest -source=file_handler.go -destination=file_handler_mocks_test.go -package=web_test

package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
	"github.com/partyzanex/padmark/internal/adapters/web/response"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

const (
	// fileTransferTimeout is the total limit of one upload or download. The idle timeout
	// (FileTransferLimits.IdleTimeout) stops a stalled transfer before it.
	fileTransferTimeout = time.Hour
	// multipartOverhead is the space for the form fields and part headers above the file limit.
	multipartOverhead = 1 << 20
	// maxFormFieldSize limits each text field of the upload form.
	maxFormFieldSize = 1 << 10
	// maxFormFieldsSize limits all bytes before the file data: the preamble, the text fields,
	// the part headers and the read-ahead of the multipart reader (4 KiB). The five fields and a
	// file name of 255 bytes need less than 8 KiB; without this limit, part headers could hold
	// the full upload limit in memory.
	maxFormFieldsSize = 64 << 10
	// maxTTLSeconds keeps now+ttl inside time.Duration, as in openapi.yaml.
	maxTTLSeconds = 9223372036
	fileFormField = "file"
	mimeBinary    = "application/octet-stream"
	decimalBase   = 10
	int64Bits     = 64
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
	errFileUpdateField    = errors.New("invalid file update field")
	errFileUpdateTrailing = errors.New("data after the JSON value")
)

// FileManager is the file usecase as the HTTP layer uses it.
type FileManager interface {
	Create(ctx context.Context, name string, body io.Reader, opts files.CreateOptions) (*domain.File, error)
	Peek(ctx context.Context, slug string) (*domain.File, error)
	Info(ctx context.Context, slug string, opts files.AccessOptions) (*domain.File, error)
	Download(ctx context.Context, slug string, opts files.AccessOptions) (*files.Download, error)
	Delete(ctx context.Context, slug, editCode string, callerID uuid.UUID) error
	Update(
		ctx context.Context, slug, editCode string, callerID uuid.UUID, opts files.UpdateOptions,
	) (*domain.File, error)
	MaxSize() int64
}

// FileHandler serves file upload, metadata, download and delete. Files are an optional
// feature: without a FileManager (no object storage configured) the routes answer 503.
type FileHandler struct {
	*common

	manager FileManager
	lockout *middleware.FailLockoutCache
	// pages holds the templates of the browser pages (web_file.go).
	pages *filePages
	// uploads limits the uploads in progress (file_transfer.go).
	uploads *uploadLimiter
	// idleTimeout stops a transfer without progress (FileTransferLimits.IdleTimeout).
	idleTimeout time.Duration
}

func newFileHandler(c *common, lockout *middleware.FailLockoutCache) *FileHandler {
	return &FileHandler{
		common: c, manager: nil, lockout: lockout, pages: newFilePages(),
		uploads:     newUploadLimiter(DefaultMaxUploads, DefaultMaxUploadsPerClient),
		idleTimeout: DefaultFileIdleTimeout,
	}
}

// WithFileManager turns on the file routes. Called by the composition root before the server
// starts; do not call after the server starts.
func (h *Handler) WithFileManager(manager FileManager) *Handler {
	h.files.manager = manager
	// The header and the success page of every concern handler need to know about files.
	h.fileMgr = manager

	return h
}

// RegisterRoutes registers the file routes. PUT and DELETE have the same edit-code lockout and
// same-origin check as the note and link mutations. POST is multipart, which a cross-site form
// can send without a CORS preflight, so it also has the same-origin check. POST
// /files/{id}/download is the download form of the file page; like the reveal form of a burn
// note, it has the CSRF guard.
func (h *FileHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /files", h.CreateFilePage)
	mux.HandleFunc("GET /files/{id}/edit", h.EditFilePage)
	mux.Handle("POST /files", middleware.RequireSameOrigin(h.log, http.HandlerFunc(h.Upload)))
	mux.HandleFunc("GET /files/{id}", h.Metadata)
	mux.HandleFunc("GET /files/{id}/download", h.DownloadFile)
	mux.Handle("POST /files/{id}/download", middleware.RequireSameOrigin(h.log, h.guard(h.DownloadFileForm)))
	mux.Handle("PUT /files/{id}", middleware.RequireSameOrigin(h.log,
		middleware.WithFailLockout(h.lockout, h.trustedProxies, h.log, http.HandlerFunc(h.UpdateFile))))
	mux.Handle("DELETE /files/{id}", middleware.RequireSameOrigin(h.log,
		middleware.WithFailLockout(h.lockout, h.trustedProxies, h.log, http.HandlerFunc(h.Delete))))
}

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

	body := h.toJSON(r, file)
	body.EditCode = file.EditCode

	w.Header().Set("Location", "/"+file.ID)
	h.writeJSON(w, r, http.StatusCreated, body)
}

// Metadata handles GET /files/{id}: the file description in JSON with the download count. It
// does not count as a download and does not consume a burn-after-download file.
func (h *FileHandler) Metadata(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		h.writeError(w, r, domain.ErrFeatureNotSupported)

		return
	}

	file, err := h.manager.Info(r.Context(), r.PathValue("id"), h.pageAccess(r))
	if err != nil {
		if errors.Is(err, domain.ErrForbidden) {
			middleware.WriteUnauthorized(w)

			return
		}

		h.writeAPIError(w, r, err)

		return
	}

	h.writeJSON(w, r, http.StatusOK, h.toJSON(r, file))
}

// DownloadFile handles GET /files/{id}/download.
func (h *FileHandler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		h.writeError(w, r, domain.ErrFeatureNotSupported)

		return
	}

	h.serveFile(w, r, r.PathValue("id"))
}

// DownloadFileForm handles POST /files/{id}/download, the download button of the file page for
// a burn-after-download file. The CSRF guard and the same-origin check prove that the user
// clicked the button on a padmark page, so the request may consume the file. A GET from a
// browser never does (explicitDownload), as a GET of a burn note shows the reveal form.
func (h *FileHandler) DownloadFileForm(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		h.writeFileErrorPage(w, r, domain.ErrFeatureNotSupported)

		return
	}

	opts := h.pageAccess(r)
	opts.AllowBurn = true

	h.sendFile(w, r, r.PathValue("id"), opts)
}

// Delete handles DELETE /files/{id} with the X-Edit-Code header, or by the owner.
func (h *FileHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		h.writeError(w, r, domain.ErrFeatureNotSupported)

		return
	}

	slug := r.PathValue("id")
	if !h.mutationAllowed(r, slug) {
		middleware.WriteUnauthorized(w)

		return
	}

	err := h.manager.Delete(r.Context(), slug, editCodeHeader(r), middleware.CallerIDFromCtx(r.Context()))
	if err != nil {
		h.writeAPIError(w, r, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// UpdateFile handles PUT /files/{id}. The JSON body can change ttl (seconds, or null to remove
// the expiry), privacy and burn_after_download; an omitted field keeps its value. The edit code
// comes only from the X-Edit-Code header, as for Delete. The response is the GET /files/{id} body.
func (h *FileHandler) UpdateFile(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		h.writeError(w, r, domain.ErrFeatureNotSupported)

		return
	}

	var body fileUpdateJSON

	decoder := json.NewDecoder(r.Body)
	// An unknown field is an error, so that a client does not send edit_code in the body and
	// think that it was used.
	decoder.DisallowUnknownFields()

	err := decoder.Decode(&body)
	if err == nil {
		err = requireJSONEnd(decoder)
	}

	if err != nil {
		h.writeDecodeError(w, r, err)

		return
	}

	opts, err := h.updateOptions(body)
	if err != nil {
		h.writeUpdateError(w, r, err)

		return
	}

	slug := r.PathValue("id")
	if !h.mutationAllowed(r, slug) {
		middleware.WriteUnauthorized(w)

		return
	}

	callerID := middleware.CallerIDFromCtx(r.Context())

	file, err := h.manager.Update(r.Context(), slug, editCodeHeader(r), callerID, opts)
	if err != nil {
		h.writeUpdateError(w, r, err)

		return
	}

	h.writeJSON(w, r, http.StatusOK, h.toJSON(r, file))
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

// logDeadlineError logs a failure to move a transfer deadline. Only the server timeouts then
// apply, so the record has debug level.
func (h *FileHandler) logDeadlineError(r *http.Request, err error) {
	if err != nil {
		h.log.DebugContext(r.Context(), "set file transfer deadline", "err", err)
	}
}

// mutationAllowed reports whether the caller of r can try to change the file slug. With
// accounts on, a caller that cannot see a private file gets 401, as for links. The edit-code
// check then does not tell a stranger that the file exists.
func (h *FileHandler) mutationAllowed(r *http.Request, slug string) bool {
	if h.authMgr == nil {
		return true
	}

	file, err := h.manager.Peek(r.Context(), slug)
	if err != nil {
		return true
	}

	return file.VisibleTo(middleware.CallerIDFromCtx(r.Context()), middleware.AuthenticatedFromCtx(r.Context()))
}

// updateOptions converts the PUT body into files.UpdateOptions. It checks the values before
// the usecase call, so that an invalid request does not read the record.
func (h *FileHandler) updateOptions(body fileUpdateJSON) (files.UpdateOptions, error) {
	opts := files.UpdateOptions{
		ExpiresAt: nil, Privacy: nil, BurnAfterDownload: body.BurnAfterDownload, ClearExpiry: false,
	}

	switch {
	case len(body.TTL) == 0:
		// An omitted ttl keeps the current expiry.
	case string(body.TTL) == "null":
		opts.ClearExpiry = true
	default:
		var ttl int64

		err := json.Unmarshal(body.TTL, &ttl)
		if err != nil || ttl < 0 || ttl > maxTTLSeconds {
			return opts, fmt.Errorf("%w: ttl must be null or 0..%d seconds", errFileUpdateField, maxTTLSeconds)
		}

		expiresAt := time.Now().UTC().Add(time.Duration(ttl) * time.Second)
		opts.ExpiresAt = &expiresAt
	}

	if body.Privacy != nil {
		privacy := domain.Privacy(*body.Privacy)
		if !privacy.Valid() {
			return opts, domain.ErrInvalidPrivacy
		}

		opts.Privacy = &privacy
	}

	err := requirePrivacySupport(accountMode(h.authMgr != nil), opts.Privacy)
	if err != nil {
		return opts, err
	}

	return opts, nil
}

// requireJSONEnd makes sure that only white space follows the JSON value. Without this check
// the decoder ignores the data after the value, and the client does not know that the server
// did not use it.
func requireJSONEnd(decoder *json.Decoder) error {
	_, err := decoder.Token()
	if errors.Is(err, io.EOF) {
		return nil
	}

	return errFileUpdateTrailing
}

func (h *FileHandler) writeUpdateError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errFileUpdateField) {
		response.WriteJSONErrorStatus(w, r, h.log, http.StatusUnprocessableEntity, err.Error())

		return
	}

	if status, _ := domainErrStatus(err); status == http.StatusInternalServerError {
		h.log.ErrorContext(r.Context(), "update file failed", "err", err)
	}

	h.writeError(w, r, err)
}

// writeAPIError writes err as JSON. Unknown errors are logged, because the client sees only a
// 500, as in respondError.
func (h *FileHandler) writeAPIError(w http.ResponseWriter, r *http.Request, err error) {
	if status, _ := domainErrStatus(err); status == http.StatusInternalServerError {
		h.log.ErrorContext(r.Context(), "file request failed", "err", err)
	}

	h.writeError(w, r, err)
}

// editCodeHeader returns the edit code of a file mutation. Files take it only from the header;
// a query string can go into server and proxy logs.
func editCodeHeader(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-Edit-Code"))
}

func (h *FileHandler) toJSON(r *http.Request, file *domain.File) fileJSON {
	return fileJSON{
		CreatedAt:         file.CreatedAt,
		ExpiresAt:         file.ExpiresAt,
		ID:                file.ID,
		URL:               h.scheme(r) + "://" + r.Host + "/" + file.ID,
		Name:              file.Name,
		Privacy:           string(file.EffectivePrivacy()),
		EditCode:          "",
		Size:              file.Size,
		Downloads:         file.Downloads,
		BurnAfterDownload: file.BurnAfterDownload,
	}
}

func (h *FileHandler) writeJSON(w http.ResponseWriter, r *http.Request, status int, body any) {
	w.Header().Set("Content-Type", mimeJSON)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	err := json.NewEncoder(w).Encode(body)
	if err != nil {
		h.log.ErrorContext(r.Context(), "write file response", "err", err)
	}
}

// respondError writes err in the format of the request. A browser gets an error page, other
// clients get JSON. Unknown errors are logged, because the client sees only a 500.
func (h *FileHandler) respondError(w http.ResponseWriter, r *http.Request, err error) {
	if status, _ := domainErrStatus(err); status == http.StatusInternalServerError {
		h.log.ErrorContext(r.Context(), "file request failed", "err", err)
	}

	switch {
	case errors.Is(err, domain.ErrForbidden):
		h.denyPrivate(w, r)
	case errors.Is(err, domain.ErrBurnIntentRequired):
		writeBurnReadNotExplicit(w, r, h.log)
	case negotiate(r) == formatHTML:
		h.writeFileErrorPage(w, r, err)
	default:
		h.writeError(w, r, err)
	}
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

	err := requirePrivacySupport(accountMode(h.authMgr != nil), opts.Privacy)
	if err != nil {
		return opts, err
	}

	return opts, nil
}

// serveFile sends the content of the file slug to a GET or HEAD request.
func (h *FileHandler) serveFile(w http.ResponseWriter, r *http.Request, slug string) {
	h.sendFile(w, r, slug, h.accessOptions(r))
}

// sendFile sends the content of the file slug, or the error in the format of the request.
func (h *FileHandler) sendFile(w http.ResponseWriter, r *http.Request, slug string, opts files.AccessOptions) {
	if r.Method == http.MethodHead {
		file, err := h.manager.Info(r.Context(), slug, opts)
		if err != nil {
			h.respondError(w, r, err)

			return
		}

		writeDownloadHeaders(w, file)

		return
	}

	deadline := newTransferDeadline(w, h.idleTimeout)
	h.logDeadlineError(r, deadline.downloadStarted())

	download, err := h.manager.Download(r.Context(), slug, opts)
	if err != nil {
		h.writeDownloadError(w, r, slug, err)

		return
	}

	// The context of a closed connection is canceled; the burn cleanup must still run.
	defer download.Close(context.WithoutCancel(r.Context()))

	writeDownloadHeaders(w, download.File)

	writer := &downloadWriter{
		ResponseWriter: w, deadline: deadline, started: false,
		onDeadlineError: func(err error) { h.logDeadlineError(r, err) },
	}

	err = download.Send(r.Context(), writer)
	if err != nil {
		h.sendFailed(w, r, slug, writer, err)
	}
}

// writeDownloadError answers a download that the usecase refused. A browser that asks for a
// burn-after-download file without the download form goes to the file page, which has the
// burn warning and the form. Other clients get the 409 JSON answer.
func (h *FileHandler) writeDownloadError(w http.ResponseWriter, r *http.Request, slug string, err error) {
	if errors.Is(err, domain.ErrBurnIntentRequired) && negotiate(r) == formatHTML {
		http.Redirect(w, r, "/"+url.PathEscape(slug), http.StatusSeeOther)

		return
	}

	h.respondError(w, r, err)
}

// sendFailed handles a failed Send. Before the first byte, nothing is sent yet: the download
// headers go away and the client gets a normal 503 error. After the first byte, the status is
// sent, so the handler aborts the connection; the client then sees a broken transfer and not a
// short 200 response.
func (h *FileHandler) sendFailed(
	w http.ResponseWriter, r *http.Request, slug string, writer *downloadWriter, err error,
) {
	if r.Context().Err() != nil {
		h.log.DebugContext(r.Context(), "file download canceled", "id", domain.HashSlug(slug))

		return
	}

	h.log.WarnContext(r.Context(), "send file", "id", domain.HashSlug(slug), "started", writer.started, "err", err)

	if writer.started {
		// The abort closes the connection without a flush of the response buffer. Without this
		// flush, a client that got less than one buffer sees an empty reply and no status.
		flushErr := http.NewResponseController(w).Flush()
		if flushErr != nil {
			h.log.DebugContext(r.Context(), "flush aborted download", "err", flushErr)
		}

		panic(http.ErrAbortHandler)
	}

	clearDownloadHeaders(w)
	h.respondError(w, r, fmt.Errorf("send file: %w", errors.Join(domain.ErrStorageUnavailable, err)))
}

// isFileUpload reports whether r is the file upload. The general body limit does not apply to
// it; Upload applies --max-file-size.
func isFileUpload(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.Path == "/files"
}

// fileJSON is the JSON form of a file. EditCode is set only in the upload response.
type fileJSON struct {
	CreatedAt         time.Time  `json:"created_at"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	ID                string     `json:"id"`
	URL               string     `json:"url"`
	Name              string     `json:"name"`
	Privacy           string     `json:"privacy"`
	EditCode          string     `json:"edit_code,omitempty"`
	Size              int64      `json:"size"`
	Downloads         int        `json:"downloads"`
	BurnAfterDownload bool       `json:"burn_after_download"`
}

// fileUpdateJSON is the body of PUT /files/{id}. A nil field keeps the current value. TTL stays
// raw, because an omitted ttl and a null ttl have different meanings.
type fileUpdateJSON struct {
	Privacy           *string         `json:"privacy"`
	BurnAfterDownload *bool           `json:"burn_after_download"`
	TTL               json.RawMessage `json:"ttl"`
}

// explicitDownload reports whether a GET deliberately downloads the file, so it may consume a
// burn-after-download file. Only a client without browser signals counts: the CLI, MCP and
// curl with an exact Accept: application/octet-stream, or an Accept that explicitRead accepts
// and that does not prefer HTML.
//
// A browser never consumes a file with a GET. A prefetch, a link in another site, a link in a
// note or a typed URL all send a GET, and Sec-Fetch-Site alone does not show a user action on
// the file page. A browser uses the download form instead (DownloadFileForm), which has the
// CSRF guard, as the reveal form of a burn note has. A browser without fetch metadata (plain
// HTTP) asks for HTML, so the HTML rule excludes it too.
func explicitDownload(r *http.Request) bool {
	if r.Method != http.MethodGet || isPrefetch(r) || r.Header.Get("Sec-Fetch-Site") != "" {
		return false
	}

	entries := parseAccept(r.Header.Get("Accept"))
	for idx := range entries {
		entry := &entries[idx]
		if entry.q > 0 && entry.spec == specExact && entry.mime == mimeBinary {
			return true
		}
	}

	return negotiate(r) != formatHTML && explicitRead(r)
}

// writeDownloadHeaders sends the file as an attachment of an opaque type. The browser saves the
// file and never shows it as a page, so an uploaded HTML or SVG file cannot run script.
func writeDownloadHeaders(w http.ResponseWriter, file *domain.File) {
	header := w.Header()
	header.Set("Content-Type", mimeBinary)
	header.Set("Content-Length", strconv.FormatInt(file.Size, decimalBase))
	header.Set("Content-Disposition", attachmentDisposition(file.Name))
	header.Set("Cache-Control", "private, no-store")
	header.Set("X-Content-Type-Options", "nosniff")
}

// attachmentDisposition returns the Content-Disposition value for name, as RFC 6266 recommends:
// an ASCII filename for old clients, then filename* with the full UTF-8 name. Clients that know
// filename* use it. Both values contain only safe characters, so a name with a quote, a
// semicolon, a backslash or a line break cannot add a parameter or a header.
func attachmentDisposition(name string) string {
	return `attachment; filename="` + asciiFileName(name) + `"; filename*=UTF-8''` + encodeExtValue(name)
}

// asciiFileName replaces each character that is not printable ASCII, and each quote and
// backslash, with an underscore. The result is safe in a quoted-string without escapes.
func asciiFileName(name string) string {
	return strings.Map(func(char rune) rune {
		if char < ' ' || char > '~' || char == '"' || char == '\\' {
			return '_'
		}

		return char
	}, name)
}

// encodeExtValue percent-encodes each byte of value that is not an attr-char of RFC 8187.
func encodeExtValue(value string) string {
	const (
		hexDigits  = "0123456789ABCDEF"
		nibbleBits = 4
		nibbleMask = 0x0f
	)

	var out strings.Builder

	for idx := range len(value) {
		char := value[idx]
		if isAttrChar(char) {
			out.WriteByte(char)

			continue
		}

		out.WriteByte('%')
		out.WriteByte(hexDigits[char>>nibbleBits])
		out.WriteByte(hexDigits[char&nibbleMask])
	}

	return out.String()
}

// isAttrChar reports whether char is an attr-char of RFC 8187: a letter, a digit or one of
// "!#$&+-.^_`|~". Each other byte must be percent-encoded.
func isAttrChar(char byte) bool {
	switch {
	case 'a' <= char && char <= 'z', 'A' <= char && char <= 'Z', '0' <= char && char <= '9':
		return true
	default:
		return strings.IndexByte("!#$&+-.^_`|~", char) >= 0
	}
}

// accessOptions identifies the reader of a download. With accounts off, the privacy check
// does not apply, as for notes and links.
func (h *FileHandler) accessOptions(r *http.Request) files.AccessOptions {
	opts := h.pageAccess(r)
	opts.AllowBurn = explicitDownload(r)

	return opts
}
