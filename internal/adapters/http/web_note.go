package http

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"time"

	_ "embed"

	"github.com/bluele/gcache"

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

	// lockout tracks consecutive wrong-edit-code failures per note ID. It is shared with the
	// ogen-served single-segment PUT/DELETE /notes/{id} routes (see registerOgenRoutes in
	// router.go) so fail-lockout tracking is consistent for a note regardless of which route
	// variant a client hits.
	lockout gcache.Cache
}

func newNoteHandler(c *common, manager NoteManager, noteTmpl, indexTmpl *template.Template) *NoteHandler {
	return &NoteHandler{
		common: c, manager: manager, noteTmpl: noteTmpl, indexTmpl: indexTmpl,
		lockout: newFailLockoutCache(),
	}
}

// RegisterRoutes registers note view/edit/update/delete and burn-after-reading reveal routes.
//
// {id...} wildcards let a note slug span multiple path segments (e.g. project/GUIDE.md). The
// ogen PUT/DELETE /notes/{id} routes (registerOgenRoutes) stay single-segment (generated router
// limitation); these native {id...} handlers bridge update/delete for path-like slugs, so by
// ServeMux specificity single-segment IDs keep going to ogen and multi-segment to these.
func (h *NoteHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("PUT /notes/{id...}", withFailLockout(h.lockout, http.HandlerFunc(h.UpdateNoteByPath)))
	mux.Handle("DELETE /notes/{id...}", withFailLockout(h.lockout, http.HandlerFunc(h.DeleteNoteByPath)))
	mux.HandleFunc("GET /notes/{id...}", h.GetNote)
	mux.HandleFunc("POST /notes/{id...}", h.guard(h.HandleReveal))
	mux.HandleFunc("GET /edit/{id...}", h.EditPage)
	mux.HandleFunc("GET /{id...}", h.GetNote)
	mux.HandleFunc("POST /{id...}", h.guard(h.HandleReveal))
}

type noteViewData struct {
	ExpiresISO        string
	CSRFToken         string
	ID                string
	CreatedAt         string
	CreatedISO        string
	ExpiresLabel      string
	Title             string
	Nonce             string
	Body              template.HTML
	ConfirmToken      string
	RawContent        string
	Privacy           string
	Views             int
	CanEdit           bool
	NeedsConfirmation bool
}

func toNoteViewData(note *domain.Note, rendered string) noteViewData {
	created := note.CreatedAt.Format("Jan 2, 2006, 3:04 PM")
	createdISO := note.CreatedAt.UTC().Format(time.RFC3339)

	expires := "Never expires"
	expiresISO := ""

	if note.ExpiresAt != nil {
		expires = "Expires " + note.ExpiresAt.Format("Jan 2, 2006")
		expiresISO = note.ExpiresAt.UTC().Format(time.RFC3339)
	}

	return noteViewData{
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
	}
}

type noteJSON struct {
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
	ExpiresAt        *time.Time         `json:"expires_at"`
	ID               string             `json:"id"`
	Title            string             `json:"title"`
	Content          string             `json:"content"`
	ContentType      domain.ContentType `json:"content_type"`
	Privacy          string             `json:"privacy"`
	Views            int                `json:"views"`
	BurnAfterReading bool               `json:"burn_after_reading"`
}

func toNoteJSON(note *domain.Note) noteJSON {
	return noteJSON{
		ID:               note.ID,
		Title:            note.Title,
		Content:          note.Content,
		ContentType:      derefContentType(note.ContentType),
		CreatedAt:        note.CreatedAt,
		UpdatedAt:        note.UpdatedAt,
		ExpiresAt:        note.ExpiresAt,
		Views:            note.Views,
		BurnAfterReading: note.BurnAfterReading,
		Privacy:          string(note.EffectivePrivacy()),
	}
}

func toBurnInterstitialViewData(note *domain.Note, tok string) noteViewData {
	return noteViewData{
		ID:                note.ID,
		Title:             note.Title,
		CreatedAt:         note.CreatedAt.Format("Jan 2, 2006, 3:04 PM"),
		CreatedISO:        note.CreatedAt.UTC().Format(time.RFC3339),
		Views:             note.Views,
		ExpiresLabel:      "Burns after reading",
		NeedsConfirmation: true,
		ConfirmToken:      tok,
		Privacy:           string(domain.PrivacyPublic),
	}
}

// GetNote handles GET /notes/{id} with content negotiation via Accept header.
func (h *NoteHandler) GetNote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Check auth for private notes before any side effects (view count, burn).
	// When auth is configured, preloaded holds the already-fetched note so the
	// subsequent View/GetRendered can skip a second SELECT.
	preloaded, handled := h.handlePrivateAuth(w, r, id)
	if handled {
		return
	}

	if h.handleBurnInterstitial(w, r, id, preloaded) {
		return
	}

	switch negotiate(r) {
	case formatHTML:
		h.renderNoteHTML(w, r, id, preloaded)

	case formatPlain:
		note, err := h.viewNote(r, id, preloaded)
		if err != nil {
			h.writeError(w, r, err)

			return
		}

		contentType := "text/plain; charset=utf-8"

		if note.ContentType == nil || *note.ContentType == domain.ContentTypeMarkdown {
			contentType = "text/markdown; charset=utf-8"
		}

		hdr := w.Header()
		hdr.Set("Content-Type", contentType)

		_, err = fmt.Fprint(w, note.Content) // raw content is intentional for text/plain and text/markdown
		if err != nil {
			h.log.ErrorContext(r.Context(), "write plain content", "id", id, "err", err)
		}

	default:
		note, err := h.viewNote(r, id, preloaded)
		if err != nil {
			h.writeError(w, r, err)

			return
		}

		w.Header().Set("Content-Type", mimeJSON)

		err = json.NewEncoder(w).Encode(toNoteJSON(note))
		if err != nil {
			h.log.ErrorContext(r.Context(), "encode get response", "err", err)
		}
	}
}

// updateNoteByPathBody mirrors ogenapi.UpdateNoteRequest for the native multi-segment update
// route. Pointer fields distinguish an omitted value from a zero one where the update semantics
// depend on it (title, content_type, privacy). The remaining fields stay plain values because
// omitted-vs-zero carries no extra meaning for them: Content always fully replaces the stored
// body (there is no partial update), EditCode need not be a pointer since an omitted or wrong
// code are handled identically by Manager.Update (both fail verification, unless the caller is
// the note's owner — see manager.Update's callerID bypass), and BurnAfterReading is a plain
// on/off toggle with no "leave as is" state.
type updateNoteByPathBody struct {
	Title            *string `json:"title"`
	ContentType      *string `json:"content_type"`
	TTL              *int64  `json:"ttl"`
	Privacy          *string `json:"privacy"`
	Content          string  `json:"content"`
	EditCode         string  `json:"edit_code"`
	BurnAfterReading bool    `json:"burn_after_reading"`
}

// UpdateNoteByPath handles PUT /notes/{id...} for notes whose slug spans multiple path segments
// (e.g. project/GUIDE.md). Single-segment IDs keep going to the ogen route PUT /notes/{id}; this
// native handler mirrors that contract — same JSON body, 200 + note JSON, same domain-error
// mapping — for the multi-segment case the generated single-segment router cannot match.
func (h *NoteHandler) UpdateNoteByPath(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var body updateNoteByPathBody

	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil {
		h.writeDecodeError(w, r, err)

		return
	}

	var burnTTL int64
	if body.BurnAfterReading && body.TTL != nil && *body.TTL >= 0 {
		burnTTL = *body.TTL
	}

	var contentType *domain.ContentType

	if body.ContentType != nil {
		ct := domain.ContentType(*body.ContentType)
		contentType = &ct
	}

	title := ""
	if body.Title != nil {
		title = *body.Title
	}

	var privacy *domain.Privacy

	if body.Privacy != nil {
		p := domain.Privacy(*body.Privacy)
		privacy = &p
	}

	note, err := h.manager.Update(r.Context(), id, body.EditCode, callerIDFromCtx(r.Context()), &domain.Note{
		Title:            title,
		Content:          body.Content,
		ContentType:      contentType,
		BurnTTL:          burnTTL,
		BurnAfterReading: body.BurnAfterReading,
		Privacy:          privacy,
	})
	if err != nil {
		h.writeError(w, r, err)

		return
	}

	w.Header().Set("Content-Type", mimeJSON)

	encErr := json.NewEncoder(w).Encode(toNoteJSON(note))
	if encErr != nil {
		h.log.ErrorContext(r.Context(), "encode update response", "id", id, "err", encErr)
	}
}

// DeleteNoteByPath handles DELETE /notes/{id...} for path-like slugs, mirroring the ogen
// DELETE /notes/{id} contract: the edit code comes from the X-Edit-Code header or the edit_code
// query parameter, and a successful delete returns 204 No Content.
func (h *NoteHandler) DeleteNoteByPath(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// The query-param fallback mirrors the documented ogen DELETE /notes/{id} contract (see
	// openapi.yaml); dropping it here only would break parity between the two routes. Our own
	// access logging never records the query string (withLogging in router.go logs r.URL.Path
	// only, and only at debug level), so the residual leak surface — reverse-proxy/CDN logs,
	// Referer on same-origin follow-up requests — is inherent to any query-param secret and
	// outside this handler's control.
	editCode := r.Header.Get("X-Edit-Code")
	if editCode == "" {
		editCode = r.URL.Query().Get("edit_code")
	}

	err := h.manager.Delete(r.Context(), id, editCode, callerIDFromCtx(r.Context()))
	if err != nil {
		h.writeError(w, r, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleReveal handles POST /{id} for burn-after-reading confirmation.
// It validates the one-time token issued by the GET interstitial, then burns and renders the note.
func (h *NoteHandler) HandleReveal(w http.ResponseWriter, r *http.Request) {
	if h.revealStore == nil {
		h.writeErrorPage(w, r, domain.ErrForbidden)

		return
	}

	id := r.PathValue("id")

	// Auth check must happen before consuming the token to prevent token-based
	// bypass of private note access control.
	preloaded, handled := h.handlePrivateAuth(w, r, id)
	if handled {
		return
	}

	const maxRevealBody = 1024

	r.Body = http.MaxBytesReader(w, r.Body, maxRevealBody)

	err := r.ParseForm()
	if err != nil {
		h.writeErrorPage(w, r, domain.ErrForbidden)

		return
	}

	tok := r.FormValue("token")

	if !h.revealStore.Consume(r.Context(), tok, domain.HashSlug(id)) {
		h.writeErrorPage(w, r, domain.ErrForbidden)

		return
	}

	h.renderNoteHTML(w, r, id, preloaded)
}

// handlePrivateAuth checks the note's privacy level against the caller (see Note.VisibleTo).
// Returns the preloaded note (when auth is configured) and false when the request may proceed.
// Returns nil and true when the response has been written (auth failed or note not found).
func (h *NoteHandler) handlePrivateAuth(w http.ResponseWriter, r *http.Request, id string) (*domain.Note, bool) {
	// When there is no auth at all, every request is implicitly authorised.
	// In TOTP-only mode (authMgr set, allowedTokens nil) we must still check
	// the session before deciding to serve a private note.
	if h.allowedTokens == nil && h.authMgr == nil {
		return nil, false
	}

	note, err := h.manager.Peek(r.Context(), id)
	if err != nil {
		h.writeNoteError(w, r, err)

		return nil, true
	}

	if note.VisibleTo(callerIDFromCtx(r.Context()), h.isAuthenticated(r)) {
		return note, false
	}

	if negotiate(r) == formatHTML {
		loginURL := "/login?next=" + url.QueryEscape(r.URL.RequestURI())
		http.Redirect(w, r, loginURL, http.StatusSeeOther)

		return nil, true
	}

	http.Error(w, "unauthorized", http.StatusUnauthorized)

	return nil, true
}

func (h *NoteHandler) renderNoteHTML(w http.ResponseWriter, r *http.Request, id string, preloaded *domain.Note) {
	var (
		note     *domain.Note
		rendered string
		err      error
	)

	if preloaded != nil {
		note, rendered, err = h.manager.GetRenderedPreloaded(r.Context(), id, preloaded)
	} else {
		note, rendered, err = h.manager.GetRendered(r.Context(), id)
	}

	if err != nil {
		h.writeErrorPage(w, r, err)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := toNoteViewData(note, rendered)
	data.Nonce = nonceFromContext(r.Context())
	// isAuthenticated already returns true when neither allowedTokens nor authMgr
	// is configured, so the old "allowedTokens == nil" short-circuit is redundant
	// and harmful: it made CanEdit always true in TOTP-only deployments.
	data.CanEdit = h.isAuthenticated(r)

	err = h.noteTmpl.Execute(w, data)
	if err != nil {
		h.log.ErrorContext(r.Context(), "render note template", "id", id, "err", err)
	}
}

// handleBurnInterstitial renders the burn confirmation state via noteTmpl for browser
// requests to immediate-burn notes (burn_after_reading=true with no TTL). Notes that
// burn after a grace period are served directly; the timer starts on the first read.
// Returns true if the response was written.
func (h *NoteHandler) handleBurnInterstitial(
	w http.ResponseWriter, r *http.Request, id string, preloaded *domain.Note,
) bool {
	if h.revealStore == nil || negotiate(r) != formatHTML {
		return false
	}

	note := preloaded
	if note == nil {
		var err error

		note, err = h.manager.Peek(r.Context(), id)
		if err != nil {
			h.writeErrorPage(w, r, err)

			return true
		}
	}

	// Interstitial is only for immediate burn. Grace-period notes keep the old behaviour.
	if !note.BurnAfterReading || note.BurnTTL > 0 {
		return false
	}

	tok, err := h.revealStore.Issue(r.Context(), domain.HashSlug(note.ID))
	if err != nil {
		h.writeErrorPage(w, r, err)

		return true
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	data := toBurnInterstitialViewData(note, tok)
	data.Nonce = nonceFromContext(r.Context())
	data.CSRFToken = csrfFromContext(r.Context())

	execErr := h.noteTmpl.Execute(w, data)
	if execErr != nil {
		h.log.ErrorContext(r.Context(), "render burn interstitial", "id", id, "err", execErr)
	}

	return true
}

func (h *NoteHandler) viewNote(r *http.Request, id string, preloaded *domain.Note) (*domain.Note, error) {
	if preloaded != nil {
		note, err := h.manager.ViewPreloaded(r.Context(), id, preloaded)
		if err != nil {
			return nil, fmt.Errorf("view note: %w", err)
		}

		return note, nil
	}

	note, err := h.manager.View(r.Context(), id)
	if err != nil {
		return nil, fmt.Errorf("view note: %w", err)
	}

	return note, nil
}
