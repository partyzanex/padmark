package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/partyzanex/padmark/internal/adapters/web/ogenapi"
	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/links"
)

// UpdateLink implements ogenapi.Handler with the privacy and edit-code rules of UpdateNote, which
// links.Manager.Update applies. It never consumes a burn link or adds views.
//
//nolint:ireturn // ogen response unions are interfaces by design
func (h *OgenHandler) UpdateLink(
	ctx context.Context, req *ogenapi.UpdateLinkRequest, params ogenapi.UpdateLinkParams,
) (ogenapi.UpdateLinkRes, error) {
	var requested *domain.Privacy

	if req.Privacy.IsSet() {
		privacy := domain.Privacy(req.Privacy.Value)
		requested = &privacy
	}

	err := requirePrivacySupport(h.accountsEnabled(), requested)
	if err != nil {
		return mapUpdateLinkError(err, h.log), nil
	}

	opts := links.UpdateOptions{
		ExpiresAt:           nil,
		Privacy:             nil,
		ClearExpiry:         req.TTL.IsSet() && req.TTL.IsNull(),
		ResetDirectRedirect: req.DirectRedirect.IsSet() && req.DirectRedirect.IsNull(),
		DirectRedirect:      optDirectRedirectPtr(req.DirectRedirect),
	}
	if req.TTL.IsSet() && !req.TTL.IsNull() {
		expiresAt := time.Now().UTC().Add(time.Duration(req.TTL.Value) * time.Second)
		opts.ExpiresAt = &expiresAt
	}

	opts.Privacy = requested

	link, err := h.linkManager.Update(ctx, params.ID, req.EditCode.Or(""), h.reader(ctx),
		requestTargetURL(&req.TargetURL), opts)
	if errors.Is(err, domain.ErrForbidden) {
		// A caller that cannot see the link gets 401, as for a read; see common.writeMutationError.
		return &ogenapi.UpdateLinkUnauthorized{}, nil
	}

	if err != nil {
		return mapUpdateLinkError(err, h.log), nil
	}

	return domainLinkToResponse(link, h.log), nil
}

// mapUpdateLinkError translates an UpdateLink failure into the generated response union. 410 is
// an expired link (domain.ErrExpired).
//
//nolint:ireturn // ogen response unions are interfaces by design
func mapUpdateLinkError(err error, log *slog.Logger) ogenapi.UpdateLinkRes {
	r := errResp(err)
	switch status, _ := domainErrStatus(err); status {
	case http.StatusBadRequest:
		v := ogenapi.UpdateLinkBadRequest(r)

		return &v

	case http.StatusForbidden:
		v := ogenapi.UpdateLinkForbidden(r)

		return &v
	case http.StatusNotFound:
		v := ogenapi.UpdateLinkNotFound(r)

		return &v
	case http.StatusUnprocessableEntity:
		v := ogenapi.UpdateLinkUnprocessableEntity(r)

		return &v
	case http.StatusGone:
		// An expired link cannot be changed. The answer is the 410 of a read.
		v := ogenapi.UpdateLinkGone(r)

		return &v
	default:
		log.Error("update link failed", slog.String("error", err.Error()))

		v := ogenapi.UpdateLinkInternalServerError(errorResponse(internalErrorMessage))

		return &v
	}
}
