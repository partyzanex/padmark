//go:generate go run go.uber.org/mock/mockgen@latest -source=file_handler.go -destination=file_handler_mocks_test.go -package=web_test

package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
	"github.com/partyzanex/padmark/internal/adapters/web/ogenapi"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

// The constants that the upload, the update and the download of a file share.
const (
	// maxTTLSeconds keeps now+ttl inside time.Duration, as in openapi.yaml.
	maxTTLSeconds = 9223372036
	mimeBinary    = "application/octet-stream"
	decimalBase   = 10
	int64Bits     = 64
)

// FileManager is the file usecase as the HTTP layer uses it.
type FileManager interface {
	Create(ctx context.Context, name string, body io.Reader, opts files.CreateOptions) (*domain.File, error)
	// Inspect applies privacy and expiry without a download: pages and GET /files/{id} use it.
	Inspect(ctx context.Context, slug string, reader domain.Reader) (*domain.File, error)
	// Settings applies privacy and expiry without any side effect: the settings editor uses it,
	// so that it does not delete an expired file before the file page reports it.
	Settings(ctx context.Context, slug string, reader domain.Reader) (*domain.File, error)
	Download(ctx context.Context, slug string, reader domain.Reader) (*files.Download, error)
	Delete(ctx context.Context, slug, editCode string, reader domain.Reader) error
	Update(
		ctx context.Context, slug, editCode string, reader domain.Reader, opts files.UpdateOptions,
	) (*domain.File, error)
	MaxSize() int64
}

// fileMode tells whether file publishing is on: HandlerConfig.Files is set.
type fileMode bool

// Values of fileMode.
const (
	filesOff fileMode = false
	filesOn  fileMode = true
)

// fileSystem is the file publishing of a Handler: the configured FileManager, or disabledFiles
// when files are off, and the mode that tells the two apart.
type fileSystem struct {
	manager FileManager
	mode    fileMode
}

// newFileSystem converts the optional HandlerConfig.Files into a value once, so no handler needs
// a nil check.
func newFileSystem(manager FileManager) fileSystem {
	if manager == nil {
		return fileSystem{manager: disabledFiles{}, mode: filesOff}
	}

	return fileSystem{manager: manager, mode: filesOn}
}

// disabledFiles is the file usecase of a server without object storage (no --s3-endpoint). Each
// method returns domain.ErrFeatureNotSupported, so a handler that calls it gets the "files are
// off" answer and never a nil dereference. MaxSize returns 0: the error page then gives no limit.
type disabledFiles struct{}

func (disabledFiles) Create(context.Context, string, io.Reader, files.CreateOptions) (*domain.File, error) {
	return nil, domain.ErrFeatureNotSupported
}

func (disabledFiles) Inspect(context.Context, string, domain.Reader) (*domain.File, error) {
	return nil, domain.ErrFeatureNotSupported
}

func (disabledFiles) Settings(context.Context, string, domain.Reader) (*domain.File, error) {
	return nil, domain.ErrFeatureNotSupported
}

func (disabledFiles) Download(context.Context, string, domain.Reader) (*files.Download, error) {
	return nil, domain.ErrFeatureNotSupported
}

func (disabledFiles) Delete(context.Context, string, string, domain.Reader) error {
	return domain.ErrFeatureNotSupported
}

func (disabledFiles) Update(
	context.Context, string, string, domain.Reader, files.UpdateOptions,
) (*domain.File, error) {
	return nil, domain.ErrFeatureNotSupported
}

func (disabledFiles) MaxSize() int64 { return 0 }

// FileHandler serves file upload, metadata, download and delete. Files are an optional
// feature: without object storage (HandlerConfig.Files is nil) RegisterRoutes gives each file
// route the "files are off" answer: 503 in JSON for the API routes and the error page for the
// browser pages.
type FileHandler struct {
	*common

	// manager is the file usecase, or disabledFiles when files are off (common.fileMode).
	manager FileManager
	lockout *middleware.FailLockoutCache
	// pages holds the templates of the browser pages (web_file.go).
	pages *filePages
	// uploads limits the uploads in progress (file_transfer.go).
	uploads *uploadLimiter
	// idleTimeout stops a transfer without progress (FileTransferLimits.IdleTimeout).
	idleTimeout time.Duration
}

// newFileHandler builds the FileHandler. manager is disabledFiles when files are off. A zero
// field of limits selects its default.
func newFileHandler(
	c *common, manager FileManager, lockout *middleware.FailLockoutCache, limits FileTransferLimits,
) *FileHandler {
	return &FileHandler{
		common: c, manager: manager, lockout: lockout, pages: newFilePages(),
		uploads:     newUploadLimiter(limits.MaxUploads, limits.MaxUploadsPerClient),
		idleTimeout: limits.idleTimeoutOrDefault(),
	}
}

// RegisterRoutes registers the file routes. PUT and DELETE have the same edit-code lockout and
// same-origin check as the note and link mutations. POST is multipart, which a cross-site form
// can send without a CORS preflight, so it also has the same-origin check. POST
// /files/{id}/download is the download form of the file page; like the reveal form of a burn
// note, it has the CSRF guard. When files are off, each route keeps its middleware and gets the
// "files are off" answer instead of the handler (apiRoute, pageRoute).
func (h *FileHandler) RegisterRoutes(mux *http.ServeMux) {
	api, page := h.apiRoute, h.pageRoute

	mux.HandleFunc("GET /files", page(h.CreateFilePage))
	mux.HandleFunc("GET /files/{id}/edit", page(h.EditFilePage))
	mux.Handle("POST /files", middleware.RequireSameOrigin(h.log, api(h.Upload)))
	mux.HandleFunc("GET /files/{id}", api(h.Metadata))
	mux.HandleFunc("GET /files/{id}/download", api(h.DownloadFile))
	mux.Handle("POST /files/{id}/download", middleware.RequireSameOrigin(h.log, h.guard(page(h.DownloadFileForm))))
	mux.Handle("PUT /files/{id}", middleware.RequireSameOrigin(h.log,
		middleware.WithFailLockout(h.lockout, h.trustedProxies, h.log, api(h.UpdateFile))))
	mux.Handle("DELETE /files/{id}", middleware.RequireSameOrigin(h.log,
		middleware.WithFailLockout(h.lockout, h.trustedProxies, h.log, api(h.Delete))))
}

// Metadata handles GET /files/{id}: the file description in JSON with the download count. It
// does not count as a download and does not consume a burn-after-download file.
func (h *FileHandler) Metadata(w http.ResponseWriter, r *http.Request) {
	file, err := h.manager.Inspect(r.Context(), r.PathValue("id"), h.reader(r))
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

// Delete handles DELETE /files/{id} with the X-Edit-Code header, or by the owner.
func (h *FileHandler) Delete(w http.ResponseWriter, r *http.Request) {
	err := h.manager.Delete(r.Context(), r.PathValue("id"), editCodeHeader(r), h.reader(r))
	if err != nil {
		h.writeAPIError(w, r, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// apiRoute returns next when files are on. When files are off, it returns a handler that answers
// each request with the JSON 503 of domain.ErrFeatureNotSupported before it reads the body.
func (h *FileHandler) apiRoute(next http.HandlerFunc) http.HandlerFunc {
	if h.fileMode == filesOn {
		return next
	}

	return func(w http.ResponseWriter, r *http.Request) {
		h.writeError(w, r, domain.ErrFeatureNotSupported)
	}
}

// pageRoute is apiRoute for the browser pages: when files are off, the page tells the user that
// files are off.
func (h *FileHandler) pageRoute(next http.HandlerFunc) http.HandlerFunc {
	if h.fileMode == filesOn {
		return next
	}

	return func(w http.ResponseWriter, r *http.Request) {
		h.writeFileErrorPage(w, r, domain.ErrFeatureNotSupported)
	}
}

// logDeadlineError logs a failure to move a transfer deadline. Only the server timeouts then
// apply, so the record has debug level.
func (h *FileHandler) logDeadlineError(r *http.Request, err error) {
	if err != nil {
		h.log.DebugContext(r.Context(), "set file transfer deadline", "err", err)
	}
}

// writeAPIError writes err as JSON. Unknown errors are logged, because the client sees only a
// 500, as in respondError. A caller that cannot see the file gets 401 (writeMutationError).
func (h *FileHandler) writeAPIError(w http.ResponseWriter, r *http.Request, err error) {
	if status, _ := domainErrStatus(err); status == http.StatusInternalServerError {
		h.log.ErrorContext(r.Context(), "file request failed", "err", err)
	}

	h.writeMutationError(w, r, err)
}

// editCodeHeader returns the edit code of a file mutation. Files take it only from the header;
// a query string can go into server and proxy logs.
func editCodeHeader(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-Edit-Code"))
}

// toJSON returns the generated FileInfo model of file (openapi.yaml), as the JSON responses of
// GET and PUT /files/{id} use it. ADR-0002 keeps the generated types for /files; only the
// server routing is by hand. A file without expiry has no expires_at field, as before.
func (h *FileHandler) toJSON(r *http.Request, file *domain.File) *ogenapi.FileInfo {
	info := &ogenapi.FileInfo{
		ID: file.ID,
		URL: url.URL{
			Scheme: h.scheme(r), Host: r.Host, Path: "/" + file.ID,
			Opaque: "", User: nil, RawPath: "", OmitHost: false, ForceQuery: false,
			RawQuery: "", Fragment: "", RawFragment: "",
		},
		Name:              file.Name,
		Size:              file.Size,
		Privacy:           ogenapi.FileInfoPrivacy(file.EffectivePrivacy()),
		Downloads:         file.Downloads,
		BurnAfterDownload: file.BurnAfterDownload,
		ExpiresAt:         ogenapi.OptNilDateTime{Value: time.Time{}, Set: false, Null: false},
		CreatedAt:         file.CreatedAt,
	}

	if file.ExpiresAt != nil {
		info.ExpiresAt = ogenapi.NewOptNilDateTime(*file.ExpiresAt)
	}

	return info
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
