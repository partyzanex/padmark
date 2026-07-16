package http

import (
	"context"
	"log/slog"

	"github.com/partyzanex/padmark/internal/adapters/http/ogenapi"
	"github.com/partyzanex/padmark/internal/domain"
)

// OgenHandler implements ogenapi.Handler by delegating to the NoteManager use-case layer.
type OgenHandler struct {
	ogenapi.UnimplementedHandler

	manager NoteManager
	pinger  Pinger
	log     *slog.Logger
}

// NewOgenHandler creates an OgenHandler with required dependencies.
func NewOgenHandler(manager NoteManager, pinger Pinger, log *slog.Logger) *OgenHandler {
	return &OgenHandler{manager: manager, pinger: pinger, log: log}
}

// CreateNote implements ogenapi.Handler.
func (h *OgenHandler) CreateNote(
	ctx context.Context, req *ogenapi.CreateNoteRequest,
) (ogenapi.CreateNoteRes, error) {
	burnAfterReading := req.BurnAfterReading.Or(false)

	var burnTTL int64
	if burnAfterReading && req.TTL.IsSet() && req.TTL.Value >= 0 {
		burnTTL = req.TTL.Value
	}

	note, err := h.manager.Create(ctx, &domain.Note{
		ID:               req.Slug.Or(""),
		Title:            req.Title.Or(""),
		Content:          req.Content,
		ContentType:      optCreateContentTypePtr(req.ContentType),
		EditCode:         req.EditCode.Or(""),
		BurnTTL:          burnTTL,
		BurnAfterReading: burnAfterReading,
		Private:          optBoolPtr(req.Private),
		OwnerID:          ownerIDFromCtx(ctx),
	})
	if err != nil {
		return mapCreateError(err, h.log), nil
	}

	return &ogenapi.CreateNoteResponseHeaders{
		Location: ogenapi.NewOptString("/" + note.ID),
		Response: domainToCreateResponse(note),
	}, nil
}

// GetNote implements ogenapi.Handler.
// NOTE: GET /notes/{id} is intentionally NOT routed to ogenSrv in NewRouter — it is handled
// by Handler.GetNote, which enforces per-note private auth and HTML/browser redirects.
// If this route is ever added to ogenSrv, private notes will be publicly accessible via
// the JSON API without authentication. Implement an auth check here before doing so.
func (h *OgenHandler) GetNote(
	ctx context.Context, params ogenapi.GetNoteParams,
) (ogenapi.GetNoteRes, error) {
	note, err := h.manager.View(ctx, params.ID)
	if err != nil {
		return mapGetError(err, h.log), nil
	}

	return domainToResponse(note), nil
}

// UpdateNote implements ogenapi.Handler.
func (h *OgenHandler) UpdateNote(
	ctx context.Context, req *ogenapi.UpdateNoteRequest, params ogenapi.UpdateNoteParams,
) (ogenapi.UpdateNoteRes, error) {
	burnAfterReading := req.BurnAfterReading.Or(false)

	var burnTTL int64
	if burnAfterReading && req.TTL.IsSet() && req.TTL.Value >= 0 {
		burnTTL = req.TTL.Value
	}

	note, err := h.manager.Update(ctx, params.ID, req.EditCode.Or(""), callerIDFromCtx(ctx), &domain.Note{
		Title:            req.Title.Or(""),
		Content:          req.Content,
		ContentType:      optUpdateContentTypePtr(req.ContentType),
		BurnTTL:          burnTTL,
		BurnAfterReading: burnAfterReading,
		Private:          optBoolPtr(req.Private),
	})
	if err != nil {
		return mapUpdateError(err, h.log), nil
	}

	return domainToResponse(note), nil
}

// DeleteNote implements ogenapi.Handler.
func (h *OgenHandler) DeleteNote(
	ctx context.Context, params ogenapi.DeleteNoteParams,
) (ogenapi.DeleteNoteRes, error) {
	editCode := params.XEditCode.Or("")
	if editCode == "" {
		editCode = params.EditCode.Or("")
	}

	err := h.manager.Delete(ctx, params.ID, editCode, callerIDFromCtx(ctx))
	if err != nil {
		return mapDeleteError(err, h.log), nil
	}

	return &ogenapi.DeleteNoteNoContent{}, nil
}

// derefContentType dereferences *ContentType; returns markdown as the safe default when nil.
func derefContentType(ct *domain.ContentType) domain.ContentType {
	if ct == nil {
		return domain.ContentTypeMarkdown
	}

	return *ct
}

// optBoolPtr converts an OptBool to *bool: nil when not set, pointer to value otherwise.
// Passing nil to storage.Update causes COALESCE(NULL, private) to keep the existing DB value.
func optBoolPtr(o ogenapi.OptBool) *bool {
	if !o.IsSet() {
		return nil
	}

	v := o.Value

	return &v
}

// optCreateContentTypePtr converts an optional create-request content type to *domain.ContentType.
// nil means "not set" — manager.Create will default to markdown.
func optCreateContentTypePtr(o ogenapi.OptCreateNoteRequestContentType) *domain.ContentType {
	if !o.IsSet() {
		return nil
	}

	ct := domain.ContentType(o.Value)

	return &ct
}

// optUpdateContentTypePtr converts an optional update-request content type to *domain.ContentType.
// nil means "not set" — storage.Update will COALESCE to keep the existing DB value.
func optUpdateContentTypePtr(o ogenapi.OptUpdateNoteRequestContentType) *domain.ContentType {
	if !o.IsSet() {
		return nil
	}

	ct := domain.ContentType(o.Value)

	return &ct
}

// Healthz implements ogenapi.Handler.
func (h *OgenHandler) Healthz(_ context.Context) error {
	return nil
}

// Readyz implements ogenapi.Handler.
func (h *OgenHandler) Readyz(ctx context.Context) (ogenapi.ReadyzRes, error) {
	pingErr := h.pinger.PingContext(ctx)
	if pingErr != nil {
		return &ogenapi.ErrorResponse{Message: "db unavailable"}, nil //nolint:nilerr // intentional: map ping error to 503
	}

	return &ogenapi.ReadyzOK{}, nil
}
