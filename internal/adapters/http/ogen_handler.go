package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/adapters/http/ogenapi"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/auth"
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
	// authMgr is optional (WithAuthManager): when unset, POST /api/tokens and
	// POST /api/invites answer 503 (feature not enabled).
	authMgr AuthManager
	log     *slog.Logger
}

// NewOgenHandler creates an OgenHandler with required dependencies.
func NewOgenHandler(manager NoteManager, linkManager LinkManager, pinger Pinger, log *slog.Logger) *OgenHandler {
	return &OgenHandler{
		UnimplementedHandler: ogenapi.UnimplementedHandler{},
		manager:              manager,
		linkManager:          linkManager,
		pinger:               pinger,
		authMgr:              nil, // optional (WithAuthManager); /api/tokens and /api/invites answer 503 until attached
		log:                  log,
	}
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
		ClearExpiry: false,
		// CreateNote fills CreatedAt/UpdatedAt; ExpiresAt is server-derived (--ttl on the CLI)
		// and Views starts at 0. Privacy/OwnerID/EditCode/ContentType carry through from the request.
		CreatedAt:        time.Time{},
		UpdatedAt:        time.Time{},
		ExpiresAt:        nil,
		Privacy:          optCreatePrivacyPtr(req.Privacy),
		OwnerID:          ownerIDFromCtx(ctx),
		ID:               req.Slug.Or(""),
		Title:            req.Title.Or(""),
		Content:          req.Content,
		ContentType:      optCreateContentTypePtr(req.ContentType),
		EditCode:         req.EditCode.Or(""),
		Views:            0,
		BurnTTL:          burnTTL,
		BurnAfterReading: burnAfterReading,
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
// The native route handles content negotiation; this JSON implementation also enforces
// privacy before invoking any view-count or burn-after-reading side effect.
func (h *OgenHandler) GetNote(
	ctx context.Context, params ogenapi.GetNoteParams,
) (ogenapi.GetNoteRes, error) {
	note, err := h.manager.Peek(ctx, params.ID)
	if err != nil {
		return mapGetError(err, h.log), nil
	}

	if !note.VisibleTo(callerIDFromCtx(ctx), authenticatedFromCtx(ctx)) {
		v := ogenapi.GetNoteUnauthorized(errorResponse(msgUnauthorized))

		return &v, nil
	}

	note, err = h.manager.ViewPreloaded(ctx, params.ID, note)
	if err != nil {
		return mapGetError(err, h.log), nil
	}

	return domainToResponse(note), nil
}

// UpdateNote implements ogenapi.Handler.
func (h *OgenHandler) UpdateNote(
	ctx context.Context, req *ogenapi.UpdateNoteRequest, params ogenapi.UpdateNoteParams,
) (ogenapi.UpdateNoteRes, error) {
	if !h.anonymousNoteMutationAllowed(ctx, params.ID) {
		v := ogenapi.UpdateNoteUnauthorized{}

		return &v, nil
	}

	note, err := h.manager.Update(ctx, params.ID, req.EditCode.Or(""), callerIDFromCtx(ctx), noteUpdatePayload(req))
	if err != nil {
		return mapUpdateError(err, h.log), nil
	}

	return domainToResponse(note), nil
}

// noteUpdatePayload is shared by ogen and native multi-segment PUT routes.
func noteUpdatePayload(req *ogenapi.UpdateNoteRequest) *domain.Note {
	burnAfterReading := req.BurnAfterReading.Or(false)

	var burnTTL int64

	var expiresAt *time.Time

	if req.TTL.IsSet() && !req.TTL.IsNull() {
		if burnAfterReading {
			burnTTL = req.TTL.Value
		} else {
			expiresAt = ttlToExpiresAt(ogenapi.NewOptInt64(req.TTL.Value))
		}
	}

	return &domain.Note{
		// UpdatePath/Update semantics: CreatedAt/UpdatedAt/Views are not caller-set; OwnerID/ID are
		// pinned from the existing note inside the usecase; EditCode is the caller-supplied credential.
		CreatedAt:        time.Time{},
		UpdatedAt:        time.Time{},
		ExpiresAt:        expiresAt,
		Privacy:          optUpdatePrivacyPtr(req.Privacy),
		OwnerID:          nil,
		ID:               "",
		Title:            req.Title.Or(""),
		Content:          req.Content,
		ContentType:      optUpdateContentTypePtr(req.ContentType),
		EditCode:         "",
		Views:            0,
		BurnTTL:          burnTTL,
		BurnAfterReading: burnAfterReading,
		// null removes the hard expiry only when burn is off. With burn_after_reading the spec makes
		// omitted, null and 0 equivalent (immediate burn), so null must not also drop an existing
		// hard expiry that omitted/0 keep.
		ClearExpiry: !burnAfterReading && req.TTL.IsSet() && req.TTL.IsNull(),
	}
}

// DeleteNote implements ogenapi.Handler.
//
//nolint:dupl // ogen's per-operation response-union types make the PUT/DELETE bodies textually near-identical
func (h *OgenHandler) DeleteNote(
	ctx context.Context, params ogenapi.DeleteNoteParams,
) (ogenapi.DeleteNoteRes, error) {
	if !h.anonymousNoteMutationAllowed(ctx, params.ID) {
		return &ogenapi.DeleteNoteUnauthorized{}, nil
	}

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
		v := ogenapi.GetLinkUnauthorized(errorResponse(msgUnauthorized))

		return &v, nil
	}

	// Peek does not apply expiry policy, so this path has to. Checked after the privacy gate,
	// matching LinkHandler.Redirect's order, so an expired private link answers a stranger with
	// 401 rather than confirming it once existed. Unlike the redirect path this only reports the
	// expiry and does not delete the row: GET /links/{id} is documented as a side-effect-free
	// metadata read, and the expired link is cleaned up on its next GET /r/{id}.
	if link.Expired(time.Now()) {
		return mapGetLinkError(domain.ErrExpired, h.log), nil
	}

	return domainLinkToResponse(link, h.log), nil
}

// DeleteLink implements ogenapi.Handler.
//
//nolint:dupl // ogen's per-operation response-union types make the PUT/DELETE bodies textually near-identical
func (h *OgenHandler) DeleteLink(
	ctx context.Context, params ogenapi.DeleteLinkParams,
) (ogenapi.DeleteLinkRes, error) {
	if !h.anonymousLinkMutationAllowed(ctx, params.ID) {
		return &ogenapi.DeleteLinkUnauthorized{}, nil
	}

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
			// Successful fields stay zero — invariant: never both link fields and Error in the same item.
			ID:             ogenapi.OptString{Value: "", Set: false},
			EditCode:       ogenapi.OptString{Value: "", Set: false},
			Views:          ogenapi.OptInt{Value: 0, Set: false},
			BurnAfterClick: ogenapi.OptBool{Value: false, Set: false},
			Privacy:        ogenapi.OptBulkLinkResultPrivacy{Value: "", Set: false},
			ExpiresAt:      ogenapi.OptNilDateTime{Value: time.Time{}, Set: false, Null: false},
			DirectRedirect: ogenapi.OptNilBool{Value: false, Set: false, Null: false},
			CreatedAt:      ogenapi.OptDateTime{Value: time.Time{}, Set: false},
		}
	}

	result := ogenapi.BulkLinkResult{
		TargetURL:      item.TargetURL,
		ID:             ogenapi.NewOptString(link.ID),
		EditCode:       ogenapi.NewOptString(link.EditCode),
		Views:          ogenapi.NewOptInt(link.Views),
		BurnAfterClick: ogenapi.NewOptBool(link.BurnAfterClick),
		Privacy:        ogenapi.NewOptBulkLinkResultPrivacy(ogenapi.BulkLinkResultPrivacy(link.EffectivePrivacy())),
		ExpiresAt:      ogenapi.OptNilDateTime{Value: time.Time{}, Set: false, Null: false},
		DirectRedirect: ogenapi.OptNilBool{Value: false, Set: false, Null: false},
		CreatedAt:      ogenapi.NewOptDateTime(link.CreatedAt),
		// Error is unset on success — invariant: never both link fields and Error in the same item.
		Error: ogenapi.OptString{Value: "", Set: false},
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
		response := errorResponse("db unavailable")

		return &response, nil //nolint:nilerr // intentional: map ping error to 503
	}

	return &ogenapi.ReadyzOK{}, nil
}

// ── account endpoints (POST /api/tokens, POST /api/invites) ──

// CreateAPIToken implements ogenapi.Handler — POST /api/tokens.
func (h *OgenHandler) CreateAPIToken(
	ctx context.Context, req ogenapi.OptCreateAPITokenRequest,
) (ogenapi.CreateAPITokenRes, error) {
	if h.authMgr == nil {
		return mapCreateAPITokenError(domain.ErrFeatureNotSupported, h.log), nil
	}

	// Unreachable per routing (the path requires a credential in the auth middleware), but
	// guarded defensively: the handler must never mint a token for an unknown user.
	usr := userFromCtx(ctx)
	if usr == nil {
		v := ogenapi.CreateAPITokenUnauthorized(errorResponse(msgUnauthorized))

		return &v, nil
	}

	body := req.Or(ogenapi.CreateAPITokenRequest{
		TTLDays:  ogenapi.OptInt{Value: 0, Set: false},
		Password: ogenapi.OptString{Value: "", Set: false},
		TotpCode: ogenapi.OptString{Value: "", Set: false},
	})
	issuer := issuerFromCtx(ctx, body.Password.Or(""), body.TotpCode.Or(""))

	plain, err := h.authMgr.CreateAPIToken(ctx, usr.ID, issuer, body.TTLDays.Or(0))
	if err != nil {
		return mapCreateAPITokenError(err, h.log), nil
	}

	envelope, encErr := h.apiTokenEnvelope(ctx, plain)
	if encErr != nil {
		return mapCreateAPITokenError(encErr, h.log), nil
	}

	return &ogenapi.CreateAPITokenResponse{Token: envelope}, nil
}

// CreateInvite implements ogenapi.Handler — POST /api/invites.
func (h *OgenHandler) CreateInvite(ctx context.Context, req ogenapi.OptStepUpRequest) (ogenapi.CreateInviteRes, error) {
	if h.authMgr == nil {
		return mapCreateInviteError(domain.ErrFeatureNotSupported, h.log), nil
	}

	// The gate mirrors requireAdmin (web_admin.go): admin-only issuance. GenerateInvite
	// re-checks admin in the usecase, so a context carrying a stale identity cannot mint invites.
	usr := userFromCtx(ctx)
	if usr == nil || !usr.IsAdmin {
		v := ogenapi.CreateInviteForbidden(errorResponse("forbidden"))

		return &v, nil
	}

	stepUp := req.Or(ogenapi.StepUpRequest{
		Password: ogenapi.OptString{Value: "", Set: false},
		TotpCode: ogenapi.OptString{Value: "", Set: false},
	})

	tok, err := h.authMgr.GenerateInvite(ctx, usr.ID, issuerFromCtx(ctx, stepUp.Password.Or(""), stepUp.TotpCode.Or("")))
	if err != nil {
		return mapCreateInviteError(err, h.log), nil
	}

	base, err := requestBaseURL(ctx)
	if err != nil {
		return mapCreateInviteError(err, h.log), nil
	}

	inviteURL, parseErr := url.Parse(base + "/setup?invite=" + url.QueryEscape(tok))
	if parseErr != nil {
		return mapCreateInviteError(parseErr, h.log), nil
	}

	return &ogenapi.CreateInviteResponse{InviteURL: *inviteURL}, nil
}

// mapCreateAPITokenError translates a CreateAPIToken failure into the generated response
// union. 429 is the per-user token cap (domain.ErrAPITokenLimit), 503 the not-enabled case,
// 403 a missing/failed step-up (with its code), 422 an out-of-range lifetime.
//
//nolint:dupl // ogen's per-operation response-union types make the status mappers textually near-identical
func mapCreateAPITokenError(err error, log *slog.Logger) ogenapi.CreateAPITokenRes {
	r := stepUpErrResp(err)

	switch status, _ := domainErrStatus(err); status {
	case http.StatusServiceUnavailable:
		v := ogenapi.CreateAPITokenServiceUnavailable(r)

		return &v
	case http.StatusForbidden:
		v := ogenapi.CreateAPITokenForbidden(r)

		return &v
	case http.StatusUnprocessableEntity:
		v := ogenapi.CreateAPITokenUnprocessableEntity(r)

		return &v
	case http.StatusTooManyRequests:
		v := ogenapi.CreateAPITokenTooManyRequests(r)

		return &v
	default:
		log.Error("create api token failed", slog.String("error", err.Error()))

		v := ogenapi.CreateAPITokenInternalServerError(errorResponse(internalErrorMessage))

		return &v
	}
}

// issuerFromCtx describes how the caller of a credential-issuing operation authenticated: the
// bearer API token the auth middleware resolved (none for a browser session) plus the step-up
// the request body carried. The usecase decides whether the step-up is needed and valid.
func issuerFromCtx(ctx context.Context, password, totpCode string) auth.Issuer {
	return auth.Issuer{
		APITokenHash: apiTokenHashFromCtx(ctx),
		StepUp:       auth.StepUp{Password: password, TOTPCode: totpCode},
	}
}

// Stable `code` values of the step-up refusals on the issuing endpoints.
const (
	stepUpRequiredCode = "step_up_required"
	stepUpFailedCode   = "step_up_failed"
)

// stepUpErrResp is errResp plus the machine-readable code a client branches on for a step-up
// refusal (prompt for the password/TOTP code vs. report a wrong one).
func stepUpErrResp(err error) ogenapi.ErrorResponse {
	resp := errResp(err)

	switch {
	case errors.Is(err, domain.ErrStepUpRequired):
		resp.Code = ogenapi.NewOptString(stepUpRequiredCode)
	case errors.Is(err, domain.ErrStepUpFailed):
		resp.Code = ogenapi.NewOptString(stepUpFailedCode)
	}

	return resp
}

// mapCreateInviteError translates a CreateInvite failure into the generated response union.
func mapCreateInviteError(err error, log *slog.Logger) ogenapi.CreateInviteRes {
	r := stepUpErrResp(err)

	switch status, _ := domainErrStatus(err); status {
	case http.StatusServiceUnavailable:
		v := ogenapi.CreateInviteServiceUnavailable(r)

		return &v
	case http.StatusForbidden:
		v := ogenapi.CreateInviteForbidden(r)

		return &v
	default:
		log.Error("create invite failed", slog.String("error", err.Error()))

		v := ogenapi.CreateInviteInternalServerError(errorResponse(internalErrorMessage))

		return &v
	}
}

// anonymousNoteMutationAllowed reports whether an anonymous caller may attempt to mutate a
// note (PUT/DELETE): the note's read-level privacy must admit anonymous callers, mirroring
// the read gate (handlePrivateAuth → Note.VisibleTo) — someone who could not read the note
// may not edit it via edit code either. Authenticated callers always pass; their ownership
// still decides inside the usecase (OwnedBy / ValidateOwnership). A failed Peek defers to the
// real mutation so the caller sees the same error (404) a read would produce, unchanged.
func (h *OgenHandler) anonymousNoteMutationAllowed(ctx context.Context, id string) bool {
	if callerIDFromCtx(ctx) != uuid.Nil {
		return true
	}

	note, err := h.manager.Peek(ctx, id)
	if err != nil {
		return true
	}

	return note.VisibleTo(uuid.Nil, false)
}

// anonymousLinkMutationAllowed is the links counterpart of anonymousNoteMutationAllowed
// (see GetLink's VisibleTo check for the read-path symmetry).
func (h *OgenHandler) anonymousLinkMutationAllowed(ctx context.Context, id string) bool {
	if callerIDFromCtx(ctx) != uuid.Nil {
		return true
	}

	link, err := h.linkManager.Peek(ctx, id)
	if err != nil {
		return true
	}

	return link.VisibleTo(uuid.Nil, false)
}
