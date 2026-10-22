package http

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	_ "embed"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/adapters/http/ogenapi"
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
	lockout *failLockoutCache
}

func newNoteHandler(c *common, manager NoteManager, noteTmpl, indexTmpl *template.Template) *NoteHandler {
	return &NoteHandler{
		common: c, manager: manager, noteTmpl: noteTmpl, indexTmpl: indexTmpl,
		lockout: newFailLockoutCache(),
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
func (h *NoteHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("PUT /notes/{id...}",
		requireSameOrigin(h.log, withFailLockout(h.lockout, h.trustedProxies, h.log, http.HandlerFunc(h.UpdateNoteByPath))))
	mux.Handle("DELETE /notes/{id...}",
		requireSameOrigin(h.log, withFailLockout(h.lockout, h.trustedProxies, h.log, http.HandlerFunc(h.DeleteNoteByPath))))
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
		// CSRFToken populated by the wrap middleware at render time; Nonce from the request scope.
		// CanEdit/NeedsConfirmation false here (regular view path, not burn interstitial).
		CSRFToken:         "",
		Nonce:             "",
		ConfirmToken:      "",
		CanEdit:           false,
		NeedsConfirmation: false,
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
	Privacy          domain.Privacy     `json:"privacy"`
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
		Privacy:          note.EffectivePrivacy(),
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
		// CSRFToken/Nonce populated by the wrap middleware; Body/RawContent/ExpiresISO not used on the
		// interstitial (confirmation page, not the read view). CanEdit false (no edit affordance).
		CSRFToken:  "",
		Nonce:      "",
		Body:       "",
		RawContent: "",
		ExpiresISO: "",
		CanEdit:    false,
	}
}

// GetNote handles GET /notes/{id} with content negotiation via Accept header.
func (h *NoteHandler) GetNote(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Check auth for private notes before any side effects (view count, burn).
	// When auth is configured, preloaded holds the already-fetched note so the
	// subsequent View/GetRendered can skip a second SELECT.
	preloaded, handled := h.gateNoteRead(w, r, id)
	if handled {
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
			h.log.ErrorContext(r.Context(), "write plain content", "id", domain.HashSlug(id), "err", err)
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

// UpdateNoteByPath handles PUT /notes/{id...} for notes whose slug spans multiple path segments
// (e.g. project/GUIDE.md). Single-segment IDs keep going to the ogen route PUT /notes/{id}; this
// native handler mirrors that contract — same JSON body, 200 + note JSON, same domain-error
// mapping — for the multi-segment case the generated single-segment router cannot match.
func (h *NoteHandler) UpdateNoteByPath(w http.ResponseWriter, r *http.Request) {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeJSONErrorStatus(w, r, h.log, http.StatusUnsupportedMediaType, unsupportedMediaTypeMessage)

		return
	}

	id := r.PathValue("id")

	if h.gateAnonymousMutation(w, r, id) {
		return
	}

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

	note, err := h.manager.Update(
		r.Context(), id, body.EditCode.Or(""), callerIDFromCtx(r.Context()), noteUpdatePayload(&body),
	)
	if err != nil {
		h.writeError(w, r, err)

		return
	}

	w.Header().Set("Content-Type", mimeJSON)

	encErr := json.NewEncoder(w).Encode(toNoteJSON(note))
	if encErr != nil {
		h.log.ErrorContext(r.Context(), "encode update response", "id", domain.HashSlug(id), "err", encErr)
	}
}

// DeleteNoteByPath handles DELETE /notes/{id...} for path-like slugs, mirroring the ogen
// DELETE /notes/{id} contract: the edit code comes from the X-Edit-Code header only (never the
// query string, which leaks into proxy logs and Referer), and a successful delete returns 204.
func (h *NoteHandler) DeleteNoteByPath(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if h.gateAnonymousMutation(w, r, id) {
		return
	}

	editCode := r.Header.Get("X-Edit-Code")

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

// gateAnonymousMutation enforces the note's read-level privacy for anonymous write callers
// (PUT/DELETE): someone who could not read the note may not mutate it via edit code either —
// the mutation counterpart of handlePrivateAuth, applied by UpdateNoteByPath and
// DeleteNoteByPath. Authenticated callers always proceed; their ownership still decides inside
// the usecase (OwnedBy / ValidateOwnership). A failed Peek defers to the real mutation so the
// caller sees the same error (404) a read would produce. Reports whether the response has been
// written (gate refused the request).
func (h *NoteHandler) gateAnonymousMutation(w http.ResponseWriter, r *http.Request, id string) bool {
	if h.authMgr == nil {
		return false
	}

	if callerIDFromCtx(r.Context()) != uuid.Nil || h.isAuthenticated(r) {
		return false
	}

	note, err := h.manager.Peek(r.Context(), id)
	if err != nil {
		return false
	}

	if note.VisibleTo(uuid.Nil, false) {
		return false
	}

	writeUnauthorized(w)

	return true
}

// handlePrivateAuth checks the note's privacy level against the caller (see Note.VisibleTo).
// Returns the preloaded note (when auth is configured) and false when the request may proceed.
// Returns nil and true when the response has been written (auth failed or note not found).
// A denial is handed to common.denyPrivate, shared with the link gate, which decides between the
// login redirect and 403 by whether the caller is already signed in.
func (h *NoteHandler) handlePrivateAuth(w http.ResponseWriter, r *http.Request, id string) (*domain.Note, bool) {
	// When there is no auth at all, every request is implicitly authorised.
	if h.authMgr == nil {
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

	h.denyPrivate(w, r)

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
	// Also gated on VisibleTo: without it, a privacy=owner note would show the Edit link (and
	// let EditPage render its plaintext, see EditPage's doc comment) to any authenticated
	// caller, not just its owner.
	authenticated := h.isAuthenticated(r)
	data.CanEdit = authenticated && note.VisibleTo(callerIDFromCtx(r.Context()), authenticated)

	err = h.noteTmpl.Execute(w, data)
	if err != nil {
		h.log.ErrorContext(r.Context(), "render note template", "id", domain.HashSlug(id), "err", err)
	}
}

// gateNoteRead runs every check that may answer a note read before the read policy applies, in
// order: privacy, the implicit-read refusal for burn notes, and the HTML burn interstitial. It
// returns the note already loaded on the way (nil if none was needed) and whether the response
// has been written.
func (h *NoteHandler) gateNoteRead(w http.ResponseWriter, r *http.Request, id string) (*domain.Note, bool) {
	preloaded, handled := h.handlePrivateAuth(w, r, id)
	if handled {
		return nil, true
	}

	preloaded, handled = h.refuseImplicitBurnRead(w, r, id, preloaded)
	if handled {
		return nil, true
	}

	if h.handleBurnInterstitial(w, r, id, preloaded) {
		return nil, true
	}

	return preloaded, false
}

// burnReadNotExplicitCode is the stable `code` of the 409 refusing an implicit burn read.
const burnReadNotExplicitCode = "burn_read_not_explicit"

// refuseImplicitBurnRead keeps reads without explicit intent (see explicitRead) from burning a
// note: a burn-after-reading note — immediate or with a grace-period timer not yet started — is
// answered 409 and stays intact. Otherwise it returns the note it had to load as the preloaded
// snapshot, so the caller applies the read policy to exactly the note that was checked, with
// no second SELECT. Explicit reads pass through untouched.
func (h *NoteHandler) refuseImplicitBurnRead(
	w http.ResponseWriter, r *http.Request, id string, preloaded *domain.Note,
) (*domain.Note, bool) {
	if explicitRead(r) {
		return preloaded, false
	}

	note := preloaded
	if note == nil {
		var err error

		note, err = h.manager.Peek(r.Context(), id)
		if err != nil {
			h.writeError(w, r, err)

			return nil, true
		}
	}

	if !note.BurnAfterReading {
		return note, false
	}

	writeBurnReadNotExplicit(w, r, h.log)

	return nil, true
}

// writeBurnReadNotExplicit answers an access to a burn resource (burn-after-reading note,
// burn_after_click link) that lacked deliberate intent: 409 with a stable code; nothing burned.
func writeBurnReadNotExplicit(w http.ResponseWriter, r *http.Request, log *slog.Logger) {
	writeJSONErrorCode(w, r, log, http.StatusConflict,
		"burn resource: accessing it destroys it, so the request must be a deliberate one — a browser "+
			"navigation, or an Accept naming application/json, text/markdown, text/plain or text/html",
		burnReadNotExplicitCode)
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
		h.log.ErrorContext(r.Context(), "render burn interstitial", "id", domain.HashSlug(id), "err", execErr)
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
