package http

import (
	_ "embed"
	"net/http"
	"time"

	"github.com/partyzanex/padmark/internal/adapters/http/middleware"
	"github.com/partyzanex/padmark/internal/domain"
)

//go:embed templates/link_editor.html
var linkEditorTmplSrc string

type linkEditorViewData struct {
	ID             string
	TargetURL      string
	Nonce          string
	DirectRedirect string
	ExpiresLabel   string
	PrivacyOptions []privacyOption
	EditMode       bool
	IsOwner        bool
	BurnAfterClick bool
}

// CreateLinkPage renders the short-link form without following any link.
func (h *LinkHandler) CreateLinkPage(w http.ResponseWriter, r *http.Request) {
	data := linkEditorViewData{
		ID: "", TargetURL: "", Nonce: middleware.NonceFromContext(r.Context()),
		DirectRedirect: "inherit", ExpiresLabel: "", EditMode: false, IsOwner: false, BurnAfterClick: false,
		PrivacyOptions: privacyOptions(h.authMgr != nil, h.signedIn(r), domain.PrivacyPublic),
	}
	h.renderLinkEditor(w, r, &data)
}

// EditLinkPage reads metadata only: opening an editor must never consume a burn link.
func (h *LinkHandler) EditLinkPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	link, err := h.manager.Peek(r.Context(), id)
	if err != nil {
		h.writeErrorPage(w, r, err)

		return
	}

	if h.authMgr != nil && !link.VisibleTo(middleware.CallerIDFromCtx(r.Context()), h.isAuthenticated(r)) {
		h.denyPrivate(w, r)

		return
	}

	if link.Expired(time.Now()) {
		h.writeErrorPage(w, r, domain.ErrExpired)

		return
	}

	data := linkEditorViewData{
		ID: id, TargetURL: link.TargetURL, Nonce: middleware.NonceFromContext(r.Context()),
		DirectRedirect: "inherit", ExpiresLabel: "Never expires", EditMode: true,
		IsOwner: link.OwnedBy(middleware.CallerIDFromCtx(r.Context())), BurnAfterClick: link.BurnAfterClick,
		PrivacyOptions: privacyOptions(h.authMgr != nil, h.signedIn(r), link.EffectivePrivacy()),
	}
	if link.DirectRedirect != nil {
		data.DirectRedirect = "false"
		if *link.DirectRedirect {
			data.DirectRedirect = "true"
		}
	}

	if link.ExpiresAt != nil {
		data.ExpiresLabel = "Expires " + link.ExpiresAt.UTC().Format(time.RFC3339)
	}

	h.renderLinkEditor(w, r, &data)
}

func (h *LinkHandler) renderLinkEditor(w http.ResponseWriter, r *http.Request, data *linkEditorViewData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	err := h.editor.Execute(w, data)
	if err != nil {
		h.log.ErrorContext(r.Context(), "render link editor", "err", err)
	}
}
