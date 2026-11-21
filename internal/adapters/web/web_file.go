package web

import (
	_ "embed"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/files"
)

//go:embed templates/file_editor.html
var fileEditorTmplSrc string

//go:embed templates/file.html
var fileTmplSrc string

//go:embed templates/file_edit.html
var fileEditTmplSrc string

// Units of the size labels. The labels use binary units, as the --max-file-size flag does.
const (
	sizeUnitStep    = 1024
	sizeLabelDigits = 1
	// fileDateLayout formats the expiry of a file on its pages.
	fileDateLayout = "Jan 2, 2006 15:04 UTC"
	// neverExpiresLabel is the expiry text of a file without a lifetime.
	neverExpiresLabel = "Never expires"
)

// filePages holds the templates of the browser pages for files.
type filePages struct {
	editor *template.Template
	view   *template.Template
	edit   *template.Template
}

func newFilePages() *filePages {
	return &filePages{
		editor: parseTmpl("file_editor", fileEditorTmplSrc),
		view:   parseTmpl("file", fileTmplSrc),
		edit:   parseTmpl("file_edit", fileEditTmplSrc),
	}
}

// fileEditorViewData is the data of the upload page.
type fileEditorViewData struct {
	Header         headerView
	Nonce          string
	MaxSizeLabel   string
	ExpiresLabel   string
	PrivacyOptions []privacyOption
	MaxSize        int64
}

// fileViewData is the data of the file page that a browser gets on GET /{id}.
type fileViewData struct {
	ID           string
	Name         string
	URL          string
	DownloadURL  string
	SizeLabel    string
	CreatedAt    string
	CreatedISO   string
	ExpiresLabel string
	ExpiresISO   string
	Privacy      string
	Nonce        string
	// CSRFToken is the token of the download form of a burn-after-download file.
	CSRFToken string
	Header    headerView
	Downloads int
	Burn      bool
	// CanEdit shows the Edit action, with the rule of the note page (renderNoteHTML).
	CanEdit bool
}

// fileEditViewData is the data of the page that changes the settings of a file.
type fileEditViewData struct {
	Header         headerView
	ID             string
	Name           string
	URL            string
	SizeLabel      string
	ExpiresLabel   string
	Nonce          string
	PrivacyOptions []privacyOption
	Burn           bool
	IsOwner        bool
}

// CreateFilePage handles GET /files: the upload form. Without object storage the page tells
// the user that files are off.
func (h *FileHandler) CreateFilePage(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		h.writeFileErrorPage(w, r, domain.ErrFeatureNotSupported)

		return
	}

	header := h.header(r, headerKindFile)
	data := fileEditorViewData{
		Header:         header,
		Nonce:          middleware.NonceFromContext(r.Context()),
		MaxSize:        h.manager.MaxSize(),
		MaxSizeLabel:   formatSize(h.manager.MaxSize()),
		ExpiresLabel:   "Counted from the upload.",
		PrivacyOptions: privacyOptions(h.authMgr != nil, header.SignedIn(), domain.PrivacyPublic),
	}

	h.render(w, r, h.pages.editor, &data)
}

// FilePage renders the page of the file slug. It reads only the metadata, so it never
// consumes a burn-after-download file; the download button does that.
func (h *FileHandler) FilePage(w http.ResponseWriter, r *http.Request, slug string) {
	file, err := h.manager.Info(r.Context(), slug, h.pageAccess(r))
	if err != nil {
		h.writeFilePageError(w, r, err)

		return
	}

	pageURL := h.scheme(r) + "://" + r.Host + "/" + slug
	data := fileViewData{
		Header:       h.header(r, headerKindView).forRecord(recordFile),
		ID:           slug,
		Name:         file.Name,
		URL:          pageURL,
		DownloadURL:  "/files/" + slug + "/download",
		SizeLabel:    formatSize(file.Size),
		CreatedAt:    file.CreatedAt.Format(readerDateLayout),
		CreatedISO:   file.CreatedAt.UTC().Format(time.RFC3339),
		ExpiresLabel: neverExpiresLabel,
		ExpiresISO:   "",
		Privacy:      string(file.EffectivePrivacy()),
		Nonce:        middleware.NonceFromContext(r.Context()),
		CSRFToken:    middleware.CSRFFromContext(r.Context()),
		Downloads:    file.Downloads,
		Burn:         file.BurnAfterDownload,
		// Info already applied the privacy check, so each reader of the page can open the editor;
		// the save needs the edit code or the owner, as for a note.
		CanEdit: true,
	}

	if file.ExpiresAt != nil {
		data.ExpiresLabel = "Expires " + file.ExpiresAt.Format(readerExpiryLayout)
		data.ExpiresISO = file.ExpiresAt.UTC().Format(time.RFC3339)
	}

	h.render(w, r, h.pages.view, &data)
}

// EditFilePage handles the old editor URL GET /files/{id}/edit. The editor is at /edit/{id}, as
// for notes and short links. Without object storage the page tells the user that files are off.
func (h *FileHandler) EditFilePage(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		h.writeFileErrorPage(w, r, domain.ErrFeatureNotSupported)

		return
	}

	redirectToEditor(w, r)
}

// editFileIfExists renders the editor of the file slug and returns true. It returns false when
// files are off or no file has the slug, so that Handler.EditPage shows the note error.
func (h *FileHandler) editFileIfExists(w http.ResponseWriter, r *http.Request, slug string) bool {
	if h.manager == nil {
		return false
	}

	file, err := h.manager.Peek(r.Context(), slug)
	if errors.Is(err, domain.ErrNotFound) {
		return false
	}

	h.editFile(w, r, slug, file, err)

	return true
}

// editFile renders the page that changes the settings of a file. Like the short-link editor it
// reads metadata only: opening the page must never consume a burn-after-download file. peekErr
// is the error of the Peek that loaded file.
func (h *FileHandler) editFile(w http.ResponseWriter, r *http.Request, slug string, file *domain.File, peekErr error) {
	if peekErr != nil {
		h.writeFileErrorPage(w, r, peekErr)

		return
	}

	// Without accounts there is no privacy check, as for notes and links (pageAccess).
	callerID := middleware.CallerIDFromCtx(r.Context())
	if h.authMgr != nil && !file.VisibleTo(callerID, middleware.AuthenticatedFromCtx(r.Context())) {
		h.denyPrivate(w, r)

		return
	}

	if file.Expired(time.Now()) {
		h.writeFileErrorPage(w, r, domain.ErrExpired)

		return
	}

	header := h.header(r, headerKindPlain)
	data := fileEditViewData{
		Header:         header,
		ID:             slug,
		Name:           file.Name,
		URL:            h.scheme(r) + "://" + r.Host + "/" + slug,
		SizeLabel:      formatSize(file.Size),
		ExpiresLabel:   fileExpiresLabel(file.ExpiresAt),
		Nonce:          middleware.NonceFromContext(r.Context()),
		PrivacyOptions: privacyOptions(h.authMgr != nil, header.SignedIn(), file.EffectivePrivacy()),
		Burn:           file.BurnAfterDownload,
		IsOwner:        file.OwnedBy(callerID),
	}

	h.render(w, r, h.pages.edit, &data)
}

// writeFilePageError answers a failed file page. A private file sends an anonymous browser to
// the sign-in page, as for notes and links; other errors get the file error page.
func (h *FileHandler) writeFilePageError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, domain.ErrForbidden) {
		h.denyPrivate(w, r)

		return
	}

	h.writeFileErrorPage(w, r, err)
}

// writeFileErrorPage renders the error page with the texts for files. Unknown errors are logged,
// because the page shows only a general message.
func (h *FileHandler) writeFileErrorPage(w http.ResponseWriter, r *http.Request, err error) {
	var maxSize int64
	if h.manager != nil {
		maxSize = h.manager.MaxSize()
	}

	data := fileErrToPageData(err, maxSize)
	if data.Code == http.StatusInternalServerError {
		h.log.ErrorContext(r.Context(), "file page failed", "err", err)
	}

	h.writeErrorPageData(w, r, &data)
}

func (h *FileHandler) render(w http.ResponseWriter, r *http.Request, tmpl *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// A file page can show the name of a private file; a shared cache must not keep it.
	w.Header().Set("Cache-Control", "private, no-store")

	err := tmpl.Execute(w, data)
	if err != nil {
		h.log.ErrorContext(r.Context(), "render file page", "template", tmpl.Name(), "err", err)
	}
}

// fileSuccess adds the file details to the success page after an upload. A failed lookup only
// removes the details (fail-soft): the link and the edit code are still on the page, and the
// settings stay as the query gives them (see successViewData.SettingsKnown).
func (c *common) fileSuccess(r *http.Request, slug string, data *successViewData) {
	if c.fileMgr == nil {
		return
	}

	file, err := c.fileMgr.Info(r.Context(), slug, c.pageAccess(r))
	if err != nil {
		c.log.DebugContext(r.Context(), "read file for success page", "err", err)

		return
	}

	data.FileName = file.Name
	data.SizeLabel = formatSize(file.Size)
	data.Privacy = string(file.EffectivePrivacy())
	data.BurnAfterReading = file.BurnAfterDownload
	data.SettingsKnown = true

	data.ExpiresLabel = "never expires"
	if file.ExpiresAt != nil {
		data.ExpiresLabel = "expires " + file.ExpiresAt.UTC().Format(fileDateLayout)
	}
}

// pageAccess identifies the reader of a page. AllowBurn is always false: a page shows only the
// metadata, so it must not count as a download. With accounts off, notes, links and files have
// no privacy check (the owner decision of R-23), so SkipPrivacy is on.
func (c *common) pageAccess(r *http.Request) files.AccessOptions {
	return files.AccessOptions{
		CallerID:      middleware.CallerIDFromCtx(r.Context()),
		Authenticated: middleware.AuthenticatedFromCtx(r.Context()),
		AllowBurn:     false,
		SkipPrivacy:   c.authMgr == nil,
	}
}

// fileExpiresLabel is the expiry text of the file pages.
func fileExpiresLabel(expiresAt *time.Time) string {
	if expiresAt == nil {
		return neverExpiresLabel
	}

	return "Expires " + expiresAt.UTC().Format(fileDateLayout)
}

// formatSize returns size as a short text in binary units, for example "512 B" or "1.5 MiB".
func formatSize(size int64) string {
	if size < sizeUnitStep {
		return strconv.FormatInt(size, decimalBase) + " B"
	}

	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(size) / sizeUnitStep
	unit := 0

	for value >= sizeUnitStep && unit < len(units)-1 {
		value /= sizeUnitStep
		unit++
	}

	text := strconv.FormatFloat(value, 'f', sizeLabelDigits, float64BitSize)

	return strings.TrimSuffix(text, ".0") + " " + units[unit]
}
