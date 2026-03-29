package http

import (
	"context"
	"log/slog"
	"time"

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
	var expiresAt *time.Time

	if req.TTL.IsSet() && req.TTL.Value > 0 {
		tt := time.Now().Add(time.Duration(req.TTL.Value) * time.Second)
		expiresAt = &tt
	}

	ct := domain.ContentTypeMarkdown
	if req.ContentType.IsSet() {
		ct = domain.ContentType(req.ContentType.Value)
	}

	note, err := h.manager.Create(ctx, &domain.Note{
		ID:               req.Slug.Or(""),
		Title:            req.Title,
		Content:          req.Content,
		ContentType:      ct,
		ExpiresAt:        expiresAt,
		BurnAfterReading: req.BurnAfterReading.Or(false),
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
	var expiresAt *time.Time

	if req.TTL.IsSet() && req.TTL.Value > 0 {
		tt := time.Now().Add(time.Duration(req.TTL.Value) * time.Second)
		expiresAt = &tt
	}

	note, err := h.manager.Update(ctx, params.ID, req.EditCode, &domain.Note{
		Title:            req.Title,
		Content:          req.Content,
		ContentType:      domain.ContentType(req.ContentType.Or("")),
		ExpiresAt:        expiresAt,
		BurnAfterReading: req.BurnAfterReading.Or(false),
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

	err := h.manager.Delete(ctx, params.ID, editCode)
	if err != nil {
		return mapDeleteError(err, h.log), nil
	}

	return &ogenapi.DeleteNoteNoContent{}, nil
}

// Healthz implements ogenapi.Handler.
func (h *OgenHandler) Healthz(_ context.Context) error {
	return nil
}

// Readyz implements ogenapi.Handler.
func (h *OgenHandler) Readyz(ctx context.Context) (ogenapi.ReadyzRes, error) {
	if h.pinger == nil {
		return &ogenapi.ReadyzOK{}, nil
	}

	pingErr := h.pinger.PingContext(ctx)
	if pingErr != nil {
		return &ogenapi.ErrorResponse{Message: "db unavailable"}, nil //nolint:nilerr // intentional: map ping error to 503
	}

	return &ogenapi.ReadyzOK{}, nil
}
