//go:generate go run go.uber.org/mock/mockgen@latest -source=file_handler.go -destination=file_handler_mocks_test.go -package=web_test

package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
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
	// fileTransferTimeout replaces the server read and write timeouts for one upload or
	// download. The server timeouts (30 and 60 seconds by default) are for pages and JSON; a
	// large file on a slow connection needs more time.
	fileTransferTimeout = time.Hour
	// multipartOverhead is the space for the form fields and part headers above the file limit.
	multipartOverhead = 1 << 20
	// maxFormFieldSize limits each text field of the upload form.
	maxFormFieldSize = 1 << 10
	// maxTTLSeconds keeps now+ttl inside time.Duration, as in openapi.yaml.
	maxTTLSeconds = 9223372036
	fileFormField = "file"
	mimeBinary    = "application/octet-stream"
	decimalBase   = 10
	int64Bits     = 64
)

var (
	errUploadNotMultipart = errors.New("upload must be multipart/form-data with a \"file\" part")
	errUploadField        = errors.New("invalid upload form field")
	errFileUpdateField    = errors.New("invalid file update field")
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
}

func newFileHandler(c *common, lockout *middleware.FailLockoutCache) *FileHandler {
	return &FileHandler{common: c, manager: nil, lockout: lockout, pages: newFilePages()}
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
// can send without a CORS preflight, so it also has the same-origin check.
func (h *FileHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /files", h.CreateFilePage)
	mux.HandleFunc("GET /files/{id}/edit", h.EditFilePage)
	mux.Handle("POST /files", middleware.RequireSameOrigin(h.log, http.HandlerFunc(h.Upload)))
	mux.HandleFunc("GET /files/{id}", h.Metadata)
	mux.HandleFunc("GET /files/{id}/download", h.DownloadFile)
	mux.Handle("PUT /files/{id}", middleware.RequireSameOrigin(h.log,
		middleware.WithFailLockout(h.lockout, h.trustedProxies, h.log, http.HandlerFunc(h.UpdateFile))))
	mux.Handle("DELETE /files/{id}", middleware.RequireSameOrigin(h.log,
		middleware.WithFailLockout(h.lockout, h.trustedProxies, h.log, http.HandlerFunc(h.Delete))))
}

// Upload handles POST /files. The body is multipart/form-data: optional text fields ttl
// (seconds), privacy, burn_after_download, slug and edit_code, then the "file" part. The file
// goes to object storage as it arrives, so the handler reads only the fields before the file.
func (h *FileHandler) Upload(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		h.writeError(w, r, domain.ErrFeatureNotSupported)

		return
	}

	h.extendDeadlines(w, r)
	r.Body = http.MaxBytesReader(w, r.Body, h.manager.MaxSize()+multipartOverhead)

	part, opts, err := h.readUploadForm(r)
	if err != nil {
		h.writeUploadError(w, r, err)

		return
	}

	file, err := h.manager.Create(r.Context(), part.FileName(), part, opts)
	if err != nil {
		h.writeUploadError(w, r, err)

		return
	}

	body := h.toJSON(r, file)
	body.EditCode = file.EditCode

	w.Header().Set("Location", "/"+file.ID)
	h.writeJSON(w, r, http.StatusCreated, body)
}

// Metadata handles GET /files/{id}: the file description in JSON, without a download count.
func (h *FileHandler) Metadata(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		h.writeError(w, r, domain.ErrFeatureNotSupported)

		return
	}

	file, err := h.manager.Info(r.Context(), r.PathValue("id"), accessOptions(r))
	if err != nil {
		if errors.Is(err, domain.ErrForbidden) {
			middleware.WriteUnauthorized(w)

			return
		}

		h.writeError(w, r, err)

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
		h.writeError(w, r, err)

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

// extendDeadlines gives a file transfer more time than the server timeouts. A failure only
// keeps the server timeouts, so it is logged at debug level.
func (h *FileHandler) extendDeadlines(w http.ResponseWriter, r *http.Request) {
	ctrl := http.NewResponseController(w)
	deadline := time.Now().Add(fileTransferTimeout)

	err := errors.Join(ctrl.SetReadDeadline(deadline), ctrl.SetWriteDeadline(deadline))
	if err != nil {
		h.log.DebugContext(r.Context(), "extend file transfer deadline", "err", err)
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

func (h *FileHandler) writeUploadError(w http.ResponseWriter, r *http.Request, err error) {
	var maxBytes *http.MaxBytesError

	switch {
	case errors.As(err, &maxBytes), errors.Is(err, domain.ErrFileTooLarge):
		response.WriteJSONErrorStatus(w, r, h.log, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("%s (limit %d bytes)", domain.ErrFileTooLarge, h.manager.MaxSize()))
	case errors.Is(err, errUploadNotMultipart), errors.Is(err, errUploadField):
		response.WriteJSONErrorStatus(w, r, h.log, http.StatusBadRequest, err.Error())
	default:
		h.respondError(w, r, err)
	}
}

// readUploadForm reads the text fields and stops at the file part, which the caller streams.
func (h *FileHandler) readUploadForm(r *http.Request) (*multipart.Part, files.CreateOptions, error) {
	var opts files.CreateOptions

	reader, err := r.MultipartReader()
	if err != nil {
		return nil, opts, errUploadNotMultipart
	}

	fields := map[string]string{}

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, opts, errUploadNotMultipart
		}

		if err != nil {
			return nil, opts, fmt.Errorf("read upload form: %w", err)
		}

		if part.FormName() == fileFormField {
			opts, err = h.uploadOptions(r, fields)

			return part, opts, err
		}

		value, err := io.ReadAll(io.LimitReader(part, maxFormFieldSize+1))
		if err != nil {
			return nil, opts, fmt.Errorf("read upload field: %w", err)
		}

		if len(value) > maxFormFieldSize {
			return nil, opts, fmt.Errorf("%w: %s is too long", errUploadField, part.FormName())
		}

		fields[part.FormName()] = string(value)
	}
}

func (h *FileHandler) uploadOptions(r *http.Request, fields map[string]string) (files.CreateOptions, error) {
	opts := files.CreateOptions{
		ExpiresAt:         nil,
		Privacy:           nil,
		OwnerID:           middleware.OwnerIDFromCtx(r.Context()),
		CustomSlug:        fields["slug"],
		EditCode:          fields["edit_code"],
		BurnAfterDownload: false,
	}

	if raw := fields["ttl"]; raw != "" {
		ttl, err := strconv.ParseInt(raw, decimalBase, int64Bits)
		if err != nil || ttl < 0 || ttl > maxTTLSeconds {
			return opts, fmt.Errorf("%w: ttl must be 0..%d seconds", errUploadField, maxTTLSeconds)
		}

		expiresAt := time.Now().UTC().Add(time.Duration(ttl) * time.Second)
		opts.ExpiresAt = &expiresAt
	}

	if raw := fields["privacy"]; raw != "" {
		privacy := domain.Privacy(raw)
		opts.Privacy = &privacy
	}

	if raw := fields["burn_after_download"]; raw != "" {
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

// serveFile sends the content of the file slug, or the error in the format of the request.
func (h *FileHandler) serveFile(w http.ResponseWriter, r *http.Request, slug string) {
	opts := accessOptions(r)

	if r.Method == http.MethodHead {
		file, err := h.manager.Info(r.Context(), slug, opts)
		if err != nil {
			h.respondError(w, r, err)

			return
		}

		writeDownloadHeaders(w, file)

		return
	}

	h.extendDeadlines(w, r)

	download, err := h.manager.Download(r.Context(), slug, opts)
	if err != nil {
		h.respondError(w, r, err)

		return
	}

	// The context of a closed connection is canceled; the burn cleanup must still run.
	defer download.Close(context.WithoutCancel(r.Context()))

	writeDownloadHeaders(w, download.File)

	// The status is already sent, so a failure can only stop the stream. The client sees fewer
	// bytes than Content-Length and discards the file.
	err = download.Send(r.Context(), w)
	if err != nil {
		h.log.WarnContext(r.Context(), "send file", "id", domain.HashSlug(slug), "err", err)
	}
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

// explicitDownload reports whether r deliberately downloads the file, so it may consume a
// burn-after-download file. It is explicitRead, or a client that asks for binary content.
func explicitDownload(r *http.Request) bool {
	if explicitRead(r) {
		return true
	}

	if r.Method != http.MethodGet {
		return false
	}

	entries := parseAccept(r.Header.Get("Accept"))
	for idx := range entries {
		entry := &entries[idx]
		if entry.q > 0 && entry.spec == specExact && entry.mime == mimeBinary {
			return true
		}
	}

	return false
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

// accessOptions identifies the reader of r.
func accessOptions(r *http.Request) files.AccessOptions {
	return files.AccessOptions{
		CallerID:      middleware.CallerIDFromCtx(r.Context()),
		Authenticated: middleware.AuthenticatedFromCtx(r.Context()),
		AllowBurn:     explicitDownload(r),
	}
}
