package web

import (
	"errors"
	"net/http"
	"time"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
	"github.com/partyzanex/padmark/internal/domain"
)

// editNote renders the note editor pre-filled with existing content; Handler.EditPage calls it
// for GET /edit/{id...}. When a short link or a file has the slug, other opens its editor.
// The editor reads the note with Inspect: the privacy check of a read applies — without it, an
// authenticated caller who is not the note's owner could read a privacy=owner note's plaintext
// content here. The editor never burns, so it must not become a side door around the read
// policy either: an expired note is deleted and answered 410, and a burn-after-reading note's
// content is withheld (saving replaces it).
func (h *NoteHandler) editNote(w http.ResponseWriter, r *http.Request, id string, other otherRecord) {
	note, err := h.manager.Inspect(r.Context(), id, h.reader(r))
	if err != nil {
		h.writeEditorError(w, r, err, other)

		return
	}

	content := note.Content
	if note.BurnAfterReading {
		content = ""
	}

	ttl := editorTTL(note)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	isOwner := note.OwnedBy(middleware.CallerIDFromCtx(r.Context()))

	header := h.header(r, headerKindPlain)

	err = h.indexTmpl.Execute(w, editorViewData{
		Header:           header,
		ID:               note.ID,
		Title:            note.Title,
		Content:          content,
		ContentWithheld:  note.BurnAfterReading,
		TTLOptions:       burnTTLOptions(ttl),
		EditMode:         true,
		BurnAfterReading: note.BurnAfterReading || note.BurnTTL > 0,
		Privacy:          string(note.EffectivePrivacy()),
		Access:           h.privacyChoice(r, header, note.EffectivePrivacy()),
		Nonce:            middleware.NonceFromContext(r.Context()),
		IsOwner:          isOwner,
		ShowContents:     note.ContentsSidebar(),
		ContentType:      string(derefContentType(note.ContentType)),
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render edit template", "id", domain.HashSlug(id), "err", err)
	}
}

// writeEditorError answers an editor that cannot open the note. A slug of a short link or a file
// goes to other. A caller that cannot see the note gets the privacy answer; other errors get the
// error page, because the editor is a page.
func (h *NoteHandler) writeEditorError(w http.ResponseWriter, r *http.Request, err error, other otherRecord) {
	if answerOther(err, other) {
		return
	}

	switch {
	case errors.Is(err, domain.ErrForbidden):
		h.denyPrivate(w, r)
	default:
		h.writeErrorPage(w, r, err)
	}
}

// editorTTL is the TTL the editor pre-selects. For burn-after-reading notes the TTL is stored as
// BurnTTL (a duration set at creation; 0 means immediate burn) — ExpiresAt is only set after the
// first read, so BurnTTL wins when available. Otherwise it is the time left until ExpiresAt.
func editorTTL(note *domain.Note) int64 {
	if note.BurnAfterReading || note.BurnTTL > 0 {
		return note.BurnTTL
	}

	if note.ExpiresAt != nil {
		if remaining := int64(time.Until(*note.ExpiresAt).Seconds()); remaining > 0 {
			return remaining
		}
	}

	return 0
}
