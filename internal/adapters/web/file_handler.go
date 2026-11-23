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

// FileHandler serves file upload, metadata, download and delete. Files are an optional
// feature: without a FileManager (HandlerConfig.Files is nil because no object storage is
// configured) the routes answer 503.
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

// newFileHandler builds the FileHandler. manager is nil when files are off. A zero field of
// limits selects its default.
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

// Metadata handles GET /files/{id}: the file description in JSON with the download count. It
// does not count as a download and does not consume a burn-after-download file.
func (h *FileHandler) Metadata(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		h.writeError(w, r, domain.ErrFeatureNotSupported)

		return
	}

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
	if h.manager == nil {
		h.writeError(w, r, domain.ErrFeatureNotSupported)

		return
	}

	err := h.manager.Delete(r.Context(), r.PathValue("id"), editCodeHeader(r), h.reader(r))
	if err != nil {
		h.writeAPIError(w, r, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
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
