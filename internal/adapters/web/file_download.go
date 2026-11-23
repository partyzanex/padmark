package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/partyzanex/padmark/internal/domain"
)

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

	reader := h.reader(r)
	reader.AllowBurn = true

	h.sendFile(w, r, r.PathValue("id"), reader)
}

// serveFile sends the content of the file slug to a GET or HEAD request.
func (h *FileHandler) serveFile(w http.ResponseWriter, r *http.Request, slug string) {
	reader := h.reader(r)
	reader.AllowBurn = explicitDownload(r)

	h.sendFile(w, r, slug, reader)
}

// sendFile sends the content of the file slug, or the error in the format of the request.
func (h *FileHandler) sendFile(w http.ResponseWriter, r *http.Request, slug string, reader domain.Reader) {
	if r.Method == http.MethodHead {
		file, err := h.manager.Inspect(r.Context(), slug, reader)
		if err != nil {
			h.respondError(w, r, err)

			return
		}

		writeDownloadHeaders(w, file)

		return
	}

	deadline := newTransferDeadline(w, h.idleTimeout)
	h.logDeadlineError(r, deadline.downloadStarted())

	download, err := h.manager.Download(r.Context(), slug, reader)
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
	h.respondError(w, r, fmt.Errorf("download file: %w", errors.Join(domain.ErrStorageUnavailable, err)))
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
