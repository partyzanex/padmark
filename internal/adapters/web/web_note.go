package web

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
	"github.com/partyzanex/padmark/internal/adapters/web/ogenapi"
	"github.com/partyzanex/padmark/internal/adapters/web/response"
	"github.com/partyzanex/padmark/internal/domain"
)

//go:embed templates/note.html
var noteTmplSrc string

// NoteHandler serves note CRUD, view, edit, and burn-after-reading reveal endpoints.
type NoteHandler struct {
	*common

	manager     NoteManager
	revealStore RevealTokenStore
	noteTmpl    *template.Template
	indexTmpl   *template.Template // shared with PageHandler.IndexPage; EditPage reuses the editor template.

	// lockout tracks consecutive wrong-edit-code failures per bucket — see failLockoutKey: an
	// authenticated user's sessions and API tokens share one bucket per resource, and anonymous
	// callers get one bucket per resource and client IP. It is shared with
	// the ogen-served single-segment PUT/DELETE /notes/{id} routes (see registerOgenRoutes in
	// router.go) so fail-lockout tracking is consistent for a note regardless of which route
	// variant a client hits.
	lockout *middleware.FailLockoutCache
}

func newNoteHandler(c *common, manager NoteManager, noteTmpl, indexTmpl *template.Template) *NoteHandler {
	return &NoteHandler{
		common: c, manager: manager, noteTmpl: noteTmpl, indexTmpl: indexTmpl,
		lockout: middleware.NewFailLockoutCache(),
		// revealStore zero = nil — Handler.WithRevealStore attaches one at router build; nil-checks
		// in the burn routes short-circuit so tests/CLI builds that skip the option still work.
		revealStore: nil,
	}
}

// RegisterRoutes registers note view/edit/update/delete and burn-after-reading reveal routes.
//
// {id...} wildcards let a note slug span multiple path segments (e.g. project/GUIDE.md). The
// ogen PUT/DELETE /notes/{id} routes (registerOgenRoutes) stay single-segment (generated router
// limitation); these native {id...} handlers bridge update/delete for path-like slugs, so by
// ServeMux specificity single-segment IDs keep going to ogen and multi-segment to these.
// GET /{id...} and GET /edit/{id...} are registered by Handler.GetNoteOrLink and
// Handler.EditPage, because a bare slug can also name a short link or a file.
func (h *NoteHandler) RegisterRoutes(mux *http.ServeMux) {
	locked := func(next http.HandlerFunc) http.Handler {
		return middleware.RequireSameOrigin(h.log, middleware.WithFailLockout(h.lockout, h.trustedProxies, h.log, next))
	}

	mux.Handle("PUT /notes/{id...}", locked(h.UpdateNoteByPath))
	mux.Handle("DELETE /notes/{id...}", locked(h.DeleteNoteByPath))
	mux.HandleFunc("GET /notes/{id...}", h.GetNote)
	mux.HandleFunc("POST /notes/{id...}", h.guard(h.HandleReveal))
	mux.HandleFunc("POST /{id...}", h.guard(h.HandleReveal))
}

type noteViewData struct {
	Nonce             string
	Body              template.HTML
	CSRFToken         string
	ID                string
	CreatedAt         string
	CreatedISO        string
	ExpiresLabel      string
	Title             string
	ExpiresISO        string
	ConfirmToken      string
	Privacy           string
	RawContent        string
	Header            headerView
	Views             int
	CanEdit           bool
	NeedsConfirmation bool
	ShowContents      bool
	// HasMath loads KaTeX on the page; only notes with a formula pay for it.
	HasMath bool
}

// mathClassMarker is in the rendered HTML of a note that has a TeX formula: the renderer
// (internal/infra/render/math.go) writes each formula into a math-inline or math-display element.
const mathClassMarker = `class="math-`

// Layouts of the dates on the reader pages of notes, short links and files. The page script
// replaces each date with the local time of the reader.
const (
	readerDateLayout   = "Jan 2, 2006, 3:04 PM"
	readerExpiryLayout = "Jan 2, 2006"
)

func toNoteViewData(note *domain.Note, rendered string) noteViewData {
	created := note.CreatedAt.Format(readerDateLayout)
	createdISO := note.CreatedAt.UTC().Format(time.RFC3339)

	expires := "Never expires"
	expiresISO := ""

	if note.ExpiresAt != nil {
		expires = "Expires " + note.ExpiresAt.Format(readerExpiryLayout)
		expiresISO = note.ExpiresAt.UTC().Format(time.RFC3339)
	}

	return noteViewData{
		Header:       noHeader(),
		ID:           note.ID,
		Title:        note.Title,
		Body:         template.HTML(rendered), //nolint:gosec // content is bluemonday-sanitized
		RawContent:   note.Content,
		CreatedAt:    created,
		CreatedISO:   createdISO,
		Views:        note.Views,
		ExpiresLabel: expires,
		ExpiresISO:   expiresISO,
		Privacy:      string(note.EffectivePrivacy()),
		// CSRFToken populated by the wrap middleware at render time; Nonce from the request scope.
		// CanEdit/NeedsConfirmation false here (regular view path, not burn interstitial).
		CSRFToken:         "",
		Nonce:             "",
		ConfirmToken:      "",
		CanEdit:           false,
		NeedsConfirmation: false,
		ShowContents:      note.ContentsSidebar(),
		HasMath:           strings.Contains(rendered, mathClassMarker),
	}
}

func toBurnInterstitialViewData(note *domain.Note, tok string) noteViewData {
	return noteViewData{
		Header:            noHeader(),
		ID:                note.ID,
		Title:             note.Title,
		CreatedAt:         note.CreatedAt.Format(readerDateLayout),
		CreatedISO:        note.CreatedAt.UTC().Format(time.RFC3339),
		Views:             note.Views,
		ExpiresLabel:      "Burns after reading",
		NeedsConfirmation: true,
		ConfirmToken:      tok,
		Privacy:           string(domain.PrivacyPublic),
		// CSRFToken/Nonce populated by the wrap middleware; Body/RawContent/ExpiresISO not used on the
		// interstitial (confirmation page, not the read view). CanEdit false (no edit affordance).
		CSRFToken:  "",
		Nonce:      "",
		Body:       "",
		RawContent: "",
		ExpiresISO: "",
		CanEdit:    false,
		// The interstitial does not show the note, so it has no headings to list and no formulas.
		ShowContents: false,
		HasMath:      false,
	}
}

// GetNote handles GET /notes/{id} with content negotiation via Accept header.
func (h *NoteHandler) GetNote(w http.ResponseWriter, r *http.Request) {
	h.serveNote(w, r, r.PathValue("id"), nil)
}

// UpdateNoteByPath handles PUT /notes/{id...} for notes whose slug spans multiple path segments
// (e.g. project/GUIDE.md). Single-segment IDs keep going to the ogen route PUT /notes/{id}; this
// native handler mirrors that contract — same JSON body, 200 + note JSON, same domain-error
// mapping — for the multi-segment case the generated single-segment router cannot match.
func (h *NoteHandler) UpdateNoteByPath(w http.ResponseWriter, r *http.Request) {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		response.WriteJSONErrorStatus(w, r, h.log, http.StatusUnsupportedMediaType, unsupportedMediaTypeMessage)

		return
	}

	id := r.PathValue("id")

	var body ogenapi.UpdateNoteRequest

	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil {
		h.writeDecodeError(w, r, err)

		return
	}

	err = body.Validate()
	if err != nil {
		h.writeDecodeError(w, r, err)

		return
	}

	payload, err := noteUpdatePayload(&body)
	if err != nil {
		response.WriteJSONErrorStatus(w, r, h.log, http.StatusBadRequest, err.Error())

		return
	}

	err = requirePrivacySupport(h.accounts, optUpdatePrivacyPtr(body.Privacy))
	if err != nil {
		h.writeError(w, r, err)

		return
	}

	note, err := h.manager.Update(r.Context(), id, body.EditCode.Or(""), h.reader(r), payload)
	if err != nil {
		h.writeMutationError(w, r, err)

		return
	}

	w.Header().Set("Content-Type", mimeJSON)

	encErr := json.NewEncoder(w).Encode(domainToResponse(note))
	if encErr != nil {
		h.log.ErrorContext(r.Context(), "encode update response", "id", domain.HashSlug(id), "err", encErr)
	}
}

// DeleteNoteByPath handles DELETE /notes/{id...} for path-like slugs, mirroring the ogen
// DELETE /notes/{id} contract: the edit code comes from the X-Edit-Code header, or from the
// deprecated edit_code query parameter (see deleteEditCode), and a successful delete returns 204.
func (h *NoteHandler) DeleteNoteByPath(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	editCode := deleteEditCode(r.Context(), h.log, id, r.Header.Get("X-Edit-Code"), r.URL.Query().Get("edit_code"))

	err := h.manager.Delete(r.Context(), id, editCode, h.reader(r))
	if err != nil {
		h.writeMutationError(w, r, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// deleteEditCode returns the X-Edit-Code header value or, when the header is absent, the
// deprecated edit_code query value that v0.1.6 clients send (openapi.yaml, deleteNote). The query
// form stays for backward compatibility although proxy/CDN logs and Referer can record it; each
// use logs a warning with the hashed note ID only, never the code.
func deleteEditCode(ctx context.Context, log *slog.Logger, id, header, query string) string {
	if header != "" || query == "" {
		return header
	}

	log.WarnContext(ctx, "deprecated edit_code query parameter used; send the X-Edit-Code header instead",
		"id", domain.HashSlug(id))

	return query
}

// HandleReveal handles POST /{id} for burn-after-reading confirmation.
// It validates the one-time token issued by the GET interstitial, then burns and renders the note.
func (h *NoteHandler) HandleReveal(w http.ResponseWriter, r *http.Request) {
	if h.revealStore == nil {
		h.writeErrorPage(w, r, domain.ErrForbidden)

		return
	}

	id := r.PathValue("id")
	reader := h.reader(r)

	// The privacy check comes before the token is used: a caller that cannot see the note gets
	// the privacy answer, and a token of the note stays valid for its reader.
	_, err := h.manager.Inspect(r.Context(), id, reader)
	if err != nil {
		h.writeReadError(w, r, err, nil)

		return
	}

	const maxRevealBody = 1024

	r.Body = http.MaxBytesReader(w, r.Body, maxRevealBody)

	err = r.ParseForm()
	if err != nil {
		h.writeErrorPage(w, r, domain.ErrForbidden)

		return
	}

	consumed, err := h.revealStore.Consume(r.Context(), r.FormValue("token"), domain.HashSlug(id))
	if err != nil {
		// A store failure is not a wrong token: the reader gets the server error page and can
		// send the same token again. The log has the note hash and the error, never the token.
		h.log.ErrorContext(r.Context(), "consume reveal token", "id", domain.HashSlug(id), "err", err)
		h.writeErrorPage(w, r, err)

		return
	}

	if !consumed {
		h.writeErrorPage(w, r, domain.ErrForbidden)

		return
	}

	// The confirmed form is the deliberate access that may burn the note.
	reader.AllowBurn = true

	h.renderNoteHTML(w, r, id, reader)
}

// serveNote answers a note read in the format of the request. The usecase applies privacy,
// expiry, the burn intent and the burn itself (see notes.Manager.Read); this handler only
// selects the format and translates the errors. missing runs when no note has the slug and
// returns true when it answered the request instead (GetNoteOrLink tries a short link and a
// file); nil writes the note error.
func (h *NoteHandler) serveNote(w http.ResponseWriter, r *http.Request, id string, missing func() bool) {
	reader := h.reader(r)
	// A browser navigation or a client that names the format is a deliberate read; v0.1.6 API
	// reads keep their old behavior (see legacyAPIRead).
	reader.AllowBurn = explicitRead(r) || legacyAPIRead(r)

	switch negotiate(r) {
	case formatHTML:
		h.serveNoteHTML(w, r, id, reader, missing)

	case formatPlain:
		h.writePlainNote(w, r, id, reader, missing)

	case formatJSON:
		note, err := h.readNote(r, id, reader)
		if err != nil {
			h.writeReadError(w, r, err, missing)

			return
		}

		w.Header().Set("Content-Type", mimeJSON)

		err = json.NewEncoder(w).Encode(domainToResponse(note))
		if err != nil {
			h.log.ErrorContext(r.Context(), "encode get response", "err", err)
		}
	default:
		// negotiate returns only the formats above.
	}
}

// serveNoteHTML renders the note page. With a reveal store, an immediate burn note does not
// burn on the GET: the first read asks the usecase without the burn intent, and a burn note then
// gets the confirmation form (handleBurnInterstitial). A note with a burn TTL is served directly;
// its timer starts on this read.
func (h *NoteHandler) serveNoteHTML(
	w http.ResponseWriter, r *http.Request, id string, reader domain.Reader, missing func() bool,
) {
	if h.revealStore == nil || !reader.AllowBurn {
		h.renderNoteHTMLOr(w, r, id, reader, missing)

		return
	}

	held := reader
	held.AllowBurn = false

	note, rendered, err := h.manager.ReadRendered(r.Context(), id, held)
	if errors.Is(err, domain.ErrBurnIntentRequired) {
		h.handleBurnInterstitial(w, r, id, reader)

		return
	}

	if err != nil {
		h.writeReadError(w, r, err, missing)

		return
	}

	h.writeNotePage(w, r, id, reader, note, rendered)
}

// writePlainNote reads the note and writes the raw content as text/plain or text/markdown.
func (h *NoteHandler) writePlainNote(
	w http.ResponseWriter, r *http.Request, id string, reader domain.Reader, missing func() bool,
) {
	note, err := h.readNote(r, id, reader)
	if err != nil {
		h.writeReadError(w, r, err, missing)

		return
	}

	contentType := "text/plain; charset=utf-8"

	if note.ContentType == nil || *note.ContentType == domain.ContentTypeMarkdown {
		contentType = "text/markdown; charset=utf-8"
	}

	hdr := w.Header()
	hdr.Set("Content-Type", contentType)

	// The raw content is intentional: the type is text/plain or text/markdown, and the security
	// headers send nosniff, so a browser does not run it as HTML.
	_, err = io.WriteString(w, note.Content)
	if err != nil {
		h.log.ErrorContext(r.Context(), "write plain content", "id", domain.HashSlug(note.ID), "err", err)
	}
}

// readNote reads the note for reader with all read rules (notes.Manager.Read).
func (h *NoteHandler) readNote(r *http.Request, id string, reader domain.Reader) (*domain.Note, error) {
	note, err := h.manager.Read(r.Context(), id, reader)
	if err != nil {
		return nil, fmt.Errorf("read note: %w", err)
	}

	return note, nil
}

// writeReadError answers a failed note read. A missing note first goes to missing, when it is
// set. A caller that cannot see the note gets the privacy answer of common.denyPrivate, shared
// with the link and file pages. A burn note without a deliberate access gets the 409 of
// writeBurnReadNotExplicit and stays. Other errors get the error in the format of the request.
func (h *NoteHandler) writeReadError(w http.ResponseWriter, r *http.Request, err error, missing func() bool) {
	switch {
	case errors.Is(err, domain.ErrNotFound) && missing != nil && missing():
		return
	case errors.Is(err, domain.ErrForbidden):
		h.denyPrivate(w, r)
	case errors.Is(err, domain.ErrBurnIntentRequired):
		writeBurnReadNotExplicit(w, r, h.log)
	default:
		h.writeNoteError(w, r, err)
	}
}

// renderNoteHTML reads the note for reader and renders the note page.
func (h *NoteHandler) renderNoteHTML(w http.ResponseWriter, r *http.Request, id string, reader domain.Reader) {
	h.renderNoteHTMLOr(w, r, id, reader, nil)
}

// renderNoteHTMLOr is renderNoteHTML with the missing fallback of serveNote.
func (h *NoteHandler) renderNoteHTMLOr(
	w http.ResponseWriter, r *http.Request, id string, reader domain.Reader, missing func() bool,
) {
	note, rendered, err := h.manager.ReadRendered(r.Context(), id, reader)
	if err != nil {
		h.writeReadError(w, r, err, missing)

		return
	}

	h.writeNotePage(w, r, id, reader, note, rendered)
}

// writeNotePage writes the note page of note with its rendered content.
func (h *NoteHandler) writeNotePage(
	w http.ResponseWriter, r *http.Request, id string, reader domain.Reader, note *domain.Note, rendered string,
) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := toNoteViewData(note, rendered)
	data.Nonce = middleware.NonceFromContext(r.Context())
	data.Header = h.header(r, headerKindView)
	// Every reader sees the Edit link: the author of an anonymous note edits it with the edit
	// code, and PUT checks that code on the server. VisibleTo hides the link of a privacy=owner
	// note from a caller who is not its owner (see editNote's doc comment).
	data.CanEdit = note.VisibleTo(reader)

	err := h.noteTmpl.Execute(w, data)
	if err != nil {
		h.log.ErrorContext(r.Context(), "render note template", "id", domain.HashSlug(id), "err", err)
	}
}

// burnReadNotExplicitCode is the stable `code` of the 409 refusing an implicit burn read.
const burnReadNotExplicitCode = "burn_read_not_explicit"

// writeBurnReadNotExplicit answers an access to a burn resource (burn-after-reading note,
// burn_after_click link, burn-after-download file) that lacked deliberate intent: 409 with a
// stable code; nothing burned. The text names each Accept type that counts as intent: the file
// download also accepts application/octet-stream (explicitDownload).
func writeBurnReadNotExplicit(w http.ResponseWriter, r *http.Request, log *slog.Logger) {
	response.WriteJSONErrorCode(w, r, log, http.StatusConflict,
		"burn resource: accessing it destroys it, so the request must be a deliberate one — a browser "+
			"navigation (for a file download: a click on the file page), or an Accept naming "+
			"application/json, text/markdown, text/plain, text/html or, for a file, application/octet-stream",
		burnReadNotExplicitCode)
}

// handleBurnInterstitial answers a deliberate browser read of a burn note when the server has a
// reveal store. An immediate burn note (burn_after_reading=true with no TTL) gets the
// confirmation form with a one-time token; POST /{id} burns it (HandleReveal). A note that burns
// after a grace period is read now, and its timer starts on this read.
func (h *NoteHandler) handleBurnInterstitial(w http.ResponseWriter, r *http.Request, id string, reader domain.Reader) {
	note, err := h.manager.Inspect(r.Context(), id, reader)
	if err != nil {
		h.writeReadError(w, r, err, nil)

		return
	}

	if !note.BurnAfterReading || note.BurnTTL > 0 {
		h.renderNoteHTML(w, r, id, reader)

		return
	}

	tok, err := h.revealStore.Issue(r.Context(), domain.HashSlug(note.ID))
	if err != nil {
		h.writeErrorPage(w, r, err)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := toBurnInterstitialViewData(note, tok)
	data.Nonce = middleware.NonceFromContext(r.Context())
	data.Header = h.header(r, headerKindView)
	data.CSRFToken = middleware.CSRFFromContext(r.Context())

	execErr := h.noteTmpl.Execute(w, data)
	if execErr != nil {
		h.log.ErrorContext(r.Context(), "render burn interstitial", "id", domain.HashSlug(id), "err", execErr)
	}
}
