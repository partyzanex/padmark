package web

import (
	"net/http"
	"time"

	"github.com/partyzanex/padmark/internal/adapters/web/middleware"
	"github.com/partyzanex/padmark/internal/domain"
)

// EditPage handles GET /edit/{id} and renders the note editor pre-filled with existing content.
// Gated by the same privacy check as GetNote (handlePrivateAuth) — without it, an authenticated
// caller who is not the note's owner could read a privacy=owner note's plaintext content here,
// bypassing the read restriction that GET /notes/{id} enforces. The editor never burns, so it
// must not become a side door around the read policy either: an expired note is deleted and
// answered 410, and a burn-after-reading note's content is withheld (saving replaces it).
func (h *NoteHandler) EditPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	preloaded, handled := h.handlePrivateAuth(w, r, id)
	if handled {
		return
	}

	note := preloaded
	if note == nil {
		var err error

		note, err = h.manager.Peek(r.Context(), id)
		if err != nil {
			h.writeErrorPage(w, r, err)

			return
		}
	}

	err := h.manager.EnforceExpiry(r.Context(), id, note)
	if err != nil {
		h.writeErrorPage(w, r, err)

		return
	}

	content := note.Content
	if note.BurnAfterReading {
		content = ""
	}

	ttl := editorTTL(note)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	usr := middleware.UserFromContext(r)
	isOwner := usr != nil && note.OwnedBy(usr.ID)

	err = h.indexTmpl.Execute(w, editorViewData{
		ID:               note.ID,
		Title:            note.Title,
		Content:          content,
		ContentWithheld:  note.BurnAfterReading,
		TTLOptions:       burnTTLOptions(ttl),
		EditMode:         true,
		BurnAfterReading: note.BurnAfterReading || note.BurnTTL > 0,
		Privacy:          string(note.EffectivePrivacy()),
		PrivacyOptions:   privacyOptions(h.authMgr != nil, h.signedIn(r), note.EffectivePrivacy()),
		Nonce:            middleware.NonceFromContext(r.Context()),
		IsOwner:          isOwner,
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "render edit template", "id", domain.HashSlug(id), "err", err)
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
