package http

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"time"

	_ "embed"

	"github.com/partyzanex/padmark/internal/domain"
)

// slugHash returns sha256(slug) as hex — used as the reveal_tokens.note_id
// so the plaintext slug is not persisted in any DB column.
func slugHash(slug string) string {
	sum := sha256.Sum256([]byte(slug))

	return hex.EncodeToString(sum[:])
}

//go:embed templates/note.html
var noteTmplSrc string

type noteViewData struct {
	Body              template.HTML
	Title             string
	ID                string
	CreatedAt         string
	CreatedISO        string // RFC 3339 timestamp for JS client-side (browser-local) formatting
	ExpiresLabel      string
	ExpiresISO        string // RFC 3339 timestamp for JS client-side formatting; empty when never expires
	RawContent        string
	Nonce             string
	ConfirmToken      string
	CSRFToken         string
	Views             int
	Private           bool
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
		Private:      note.Private != nil && *note.Private,
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
	Views            int                `json:"views"`
	BurnAfterReading bool               `json:"burn_after_reading"`
	Private          bool               `json:"private"`
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
		Private:          note.Private != nil && *note.Private,
	}
}

// handlePrivateAuth checks whether a note is private and the caller is authenticated.
// Returns the preloaded note (when auth is configured) and false when the request may proceed.
// Returns nil and true when the response has been written (auth failed or note not found).
func (h *Handler) handlePrivateAuth(w http.ResponseWriter, r *http.Request, id string) (*domain.Note, bool) {
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

	if (note.Private == nil || !*note.Private) || h.isAuthenticated(r) {
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

func (h *Handler) renderNoteHTML(w http.ResponseWriter, r *http.Request, id string, preloaded *domain.Note) {
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

// GetNote handles GET /notes/{id} with content negotiation via Accept header.
func (h *Handler) GetNote(w http.ResponseWriter, r *http.Request) {
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
// depend on it (title, content_type, private).
type updateNoteByPathBody struct {
	Title            *string `json:"title"`
	ContentType      *string `json:"content_type"`
	TTL              *int64  `json:"ttl"`
	Private          *bool   `json:"private"`
	Content          string  `json:"content"`
	EditCode         string  `json:"edit_code"`
	BurnAfterReading bool    `json:"burn_after_reading"`
}

// UpdateNoteByPath handles PUT /notes/{id...} for notes whose slug spans multiple path segments
// (e.g. project/GUIDE.md). Single-segment IDs keep going to the ogen route PUT /notes/{id}; this
// native handler mirrors that contract — same JSON body, 200 + note JSON, same domain-error
// mapping — for the multi-segment case the generated single-segment router cannot match.
func (h *Handler) UpdateNoteByPath(w http.ResponseWriter, r *http.Request) {
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

	note, err := h.manager.Update(r.Context(), id, body.EditCode, &domain.Note{
		Title:            title,
		Content:          body.Content,
		ContentType:      contentType,
		BurnTTL:          burnTTL,
		BurnAfterReading: body.BurnAfterReading,
		Private:          body.Private,
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
func (h *Handler) DeleteNoteByPath(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	editCode := r.Header.Get("X-Edit-Code")
	if editCode == "" {
		editCode = r.URL.Query().Get("edit_code")
	}

	err := h.manager.Delete(r.Context(), id, editCode)
	if err != nil {
		h.writeError(w, r, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleBurnInterstitial renders the burn confirmation state via noteTmpl for browser
// requests to immediate-burn notes (burn_after_reading=true with no TTL). Notes that
// burn after a grace period are served directly; the timer starts on the first read.
// Returns true if the response was written.
func (h *Handler) handleBurnInterstitial(
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

	tok, err := h.revealStore.Issue(r.Context(), slugHash(note.ID))
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
	}
}

// HandleReveal handles POST /{id} for burn-after-reading confirmation.
// It validates the one-time token issued by the GET interstitial, then burns and renders the note.
func (h *Handler) HandleReveal(w http.ResponseWriter, r *http.Request) {
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

	if !h.revealStore.Consume(r.Context(), tok, slugHash(id)) {
		h.writeErrorPage(w, r, domain.ErrForbidden)

		return
	}

	h.renderNoteHTML(w, r, id, preloaded)
}

func (h *Handler) viewNote(r *http.Request, id string, preloaded *domain.Note) (*domain.Note, error) {
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
