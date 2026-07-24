package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/partyzanex/padmark/internal/adapters/http/ogenapi"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/links"
)

// OgenHandler implements ogenapi.Handler by delegating to the NoteManager and LinkManager
// use-case layers — note operations first, link operations below (kept in one file, not split
// like NoteHandler/LinkHandler on the browser-facing side, so both share this file's ireturn
// lint exclusion: ogen handler methods must return the generated per-operation response-union
// interfaces, which is unavoidable given ogen's codegen shape).
type OgenHandler struct {
	ogenapi.UnimplementedHandler

	manager     NoteManager
	linkManager LinkManager
	pinger      Pinger
	log         *slog.Logger
}

// NewOgenHandler creates an OgenHandler with required dependencies.
func NewOgenHandler(manager NoteManager, linkManager LinkManager, pinger Pinger, log *slog.Logger) *OgenHandler {
	return &OgenHandler{manager: manager, linkManager: linkManager, pinger: pinger, log: log}
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
		Privacy:          optCreatePrivacyPtr(req.Privacy),
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
		Privacy:          optUpdatePrivacyPtr(req.Privacy),
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

// CreateLink implements ogenapi.Handler.
func (h *OgenHandler) CreateLink(
	ctx context.Context, req *ogenapi.CreateLinkRequest,
) (ogenapi.CreateLinkRes, error) {
	link, err := h.linkManager.Create(ctx, req.TargetURL.String(), buildCreateLinkOptions(ctx, req))
	if err != nil {
		return mapCreateLinkError(err, h.log), nil
	}

	return &ogenapi.CreateLinkResponseHeaders{
		Location: ogenapi.NewOptString("/r/" + link.ID),
		Response: domainLinkToCreateResponse(link, h.log),
	}, nil
}

// CreateLinksBulk implements ogenapi.Handler. Best-effort, not transactional: each item is
// created independently via the same links.Manager.Create path as CreateLink, so one item's
// failure (bad scheme, slug conflict, ...) never aborts the rest. req.Links' length is already
// bounded to [1, 100] by ogen's generated Validate() before this method ever runs (see
// BulkCreateLinksRequest's minItems/maxItems in openapi.yaml) — no manual length check needed.
func (h *OgenHandler) CreateLinksBulk(
	ctx context.Context, req *ogenapi.BulkCreateLinksRequest,
) (ogenapi.CreateLinksBulkRes, error) {
	results := make([]ogenapi.BulkLinkResult, len(req.Links))

	for i := range req.Links {
		results[i] = createOneBulkLink(ctx, h.linkManager, h.log, &req.Links[i])
	}

	return &ogenapi.BulkCreateLinksResponse{Results: results}, nil
}

// GetLink implements ogenapi.Handler. This is a metadata-only read (links.Manager.Peek): it does
// not increment views, consume a burn-after-click link, or follow the redirect — see the "Short
// links" section in openapi.yaml.
//
// GET /links/{id} is routed through ogenSrv (unlike GET /notes/{id}, which bypasses it — see
// GetNote's doc comment), so this method is the sole enforcement point for the link's privacy
// level on this path and must check it explicitly, mirroring LinkHandler.handlePrivateAuth.
// authenticatedFromCtx recognizes a TOTP session or an API token (both set the context user via
// the auth middleware's isPublicRoute branch).
func (h *OgenHandler) GetLink(
	ctx context.Context, params ogenapi.GetLinkParams,
) (ogenapi.GetLinkRes, error) {
	link, err := h.linkManager.Peek(ctx, params.ID)
	if err != nil {
		return mapGetLinkError(err, h.log), nil
	}

	if !link.VisibleTo(callerIDFromCtx(ctx), authenticatedFromCtx(ctx)) {
		v := ogenapi.GetLinkUnauthorized(ogenapi.ErrorResponse{Message: "unauthorized"})

		return &v, nil
	}

	return domainLinkToResponse(link, h.log), nil
}

// DeleteLink implements ogenapi.Handler.
func (h *OgenHandler) DeleteLink(
	ctx context.Context, params ogenapi.DeleteLinkParams,
) (ogenapi.DeleteLinkRes, error) {
	editCode := params.XEditCode.Or("")
	if editCode == "" {
		editCode = params.EditCode.Or("")
	}

	err := h.linkManager.Delete(ctx, params.ID, editCode, callerIDFromCtx(ctx))
	if err != nil {
		return mapDeleteLinkError(err, h.log), nil
	}

	return &ogenapi.DeleteLinkNoContent{}, nil
}

// buildCreateLinkOptions converts a CreateLinkRequest into links.CreateOptions — shared by
// CreateLink and CreateLinksBulk (each item of a bulk request has the exact same shape).
func buildCreateLinkOptions(ctx context.Context, req *ogenapi.CreateLinkRequest) links.CreateOptions {
	return links.CreateOptions{
		ExpiresAt:      ttlToExpiresAt(req.TTL),
		Privacy:        optCreateLinkPrivacyPtr(req.Privacy),
		OwnerID:        ownerIDFromCtx(ctx),
		DirectRedirect: optDirectRedirectPtr(req.DirectRedirect),
		CustomHash:     req.Slug.Or(""),
		EditCode:       req.EditCode.Or(""),
		BurnAfterClick: req.BurnAfterClick.Or(false),
	}
}

// createOneBulkLink creates a single item of a CreateLinksBulk request and maps the outcome
// (success or failure) into its BulkLinkResult — never both a link's fields and Error, per the
// invariant documented on BulkLinkResult in openapi.yaml. On failure the message goes through
// errorStatusMessage, the same safe mapping mapCreateLinkError uses for the single-item CreateLink
// path — never the raw err.Error(), which could otherwise leak internal error detail (e.g. a
// wrapped DB error string) straight to the client for an unmapped error. An unmapped (500-class)
// error is logged here too, mirroring mapCreateLinkError's default branch — otherwise a bulk-item
// failure with no known cause would go unlogged, unlike the single-item path.
func createOneBulkLink(
	ctx context.Context, manager LinkManager, log *slog.Logger, item *ogenapi.CreateLinkRequest,
) ogenapi.BulkLinkResult {
	link, err := manager.Create(ctx, item.TargetURL.String(), buildCreateLinkOptions(ctx, item))
	if err != nil {
		status, message := errorStatusMessage(err)
		if status == http.StatusInternalServerError {
			log.Error("create link failed (bulk)", slog.String("error", err.Error()))
		}

		return ogenapi.BulkLinkResult{
			TargetURL: item.TargetURL,
			Error:     ogenapi.NewOptString(message),
		}
	}

	result := ogenapi.BulkLinkResult{
		TargetURL:      item.TargetURL,
		ID:             ogenapi.NewOptString(link.ID),
		EditCode:       ogenapi.NewOptString(link.EditCode),
		Views:          ogenapi.NewOptInt(link.Views),
		BurnAfterClick: ogenapi.NewOptBool(link.BurnAfterClick),
		Privacy:        ogenapi.NewOptBulkLinkResultPrivacy(ogenapi.BulkLinkResultPrivacy(link.EffectivePrivacy())),
		CreatedAt:      ogenapi.NewOptDateTime(link.CreatedAt),
	}

	if link.ExpiresAt != nil {
		result.ExpiresAt = ogenapi.NewOptNilDateTime(*link.ExpiresAt)
	}

	if link.DirectRedirect != nil {
		result.DirectRedirect = ogenapi.NewOptNilBool(*link.DirectRedirect)
	}

	return result
}

// ttlToExpiresAt converts CreateLinkRequest.ttl (seconds from now) to an absolute expiry
// timestamp. nil (omitted) means the link never expires.
func ttlToExpiresAt(ttl ogenapi.OptInt64) *time.Time {
	if !ttl.IsSet() {
		return nil
	}

	expiresAt := time.Now().UTC().Add(time.Duration(ttl.Value) * time.Second)

	return &expiresAt
}

// optCreateLinkPrivacyPtr converts an optional create-request privacy level to *domain.Privacy.
// nil means "not set" — links.Manager.Create will default to public.
func optCreateLinkPrivacyPtr(o ogenapi.OptCreateLinkRequestPrivacy) *domain.Privacy {
	if !o.IsSet() {
		return nil
	}

	p := domain.Privacy(o.Value)

	return &p
}

// optDirectRedirectPtr converts the optional-nullable create-request direct_redirect field to
// *bool. Both "omitted" and an explicit JSON null collapse to nil — links.Manager.Create will
// leave the link to inherit the server-wide --direct-redirect default (see
// domain.Link.EffectiveDirectRedirect); only an explicit true/false becomes a per-link override.
func optDirectRedirectPtr(o ogenapi.OptNilBool) *bool {
	if !o.IsSet() || o.IsNull() {
		return nil
	}

	v := o.Value

	return &v
}

// derefContentType dereferences *ContentType; returns markdown as the safe default when nil.
func derefContentType(ct *domain.ContentType) domain.ContentType {
	if ct == nil {
		return domain.ContentTypeMarkdown
	}

	return *ct
}

// optCreatePrivacyPtr converts an optional create-request privacy level to *domain.Privacy.
// nil means "not set" — manager.Create will default to public.
func optCreatePrivacyPtr(o ogenapi.OptCreateNoteRequestPrivacy) *domain.Privacy {
	if !o.IsSet() {
		return nil
	}

	p := domain.Privacy(o.Value)

	return &p
}

// optUpdatePrivacyPtr converts an optional update-request privacy level to *domain.Privacy.
// nil means "not set" — storage.Update will COALESCE to keep the existing DB value.
func optUpdatePrivacyPtr(o ogenapi.OptUpdateNoteRequestPrivacy) *domain.Privacy {
	if !o.IsSet() {
		return nil
	}

	p := domain.Privacy(o.Value)

	return &p
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
