package http

import (
	"context"
	"fmt"

	"github.com/partyzanex/padmark/internal/adapters/http/middleware"
	"github.com/partyzanex/padmark/internal/domain"
)

// WithAuthManager attaches the auth seam, enabling ogenapi's POST /api/tokens and
// POST /api/invites (implemented in ogen_handler.go, next to the other ogen operations).
// Unset, both operations answer 503 (feature not enabled) — mirrors Handler.WithAuthManager
// and keeps the existing NewOgenHandler call sites intact.
func (h *OgenHandler) WithAuthManager(mgr AuthManager) *OgenHandler {
	h.authMgr = mgr

	return h
}

// apiTokenEnvelope packs a plain bearer key into the pmk_ envelope carrying the server's own
// base URL — the same shape the admin panel returns (web_admin.go AdminCreateKeyHandler).
func (*OgenHandler) apiTokenEnvelope(ctx context.Context, plain string) (string, error) {
	base, err := requestBaseURL(ctx)
	if err != nil {
		return "", fmt.Errorf("request base url: %w", err)
	}

	envelope, encErr := domain.EncodeAPITokenEnvelope(base, plain)
	if encErr != nil {
		return "", fmt.Errorf("build api token envelope: %w", encErr)
	}

	return envelope, nil
}

// requestBaseURL returns `<scheme>://<host>` of the incoming request, taken from the
// WithRequestOrigin middleware (ogen handlers receive only a context). Unreachable when the
// middleware is installed; failing beats handing out a link with an unusable base URL.
func requestBaseURL(ctx context.Context) (string, error) {
	origin := middleware.OriginFromCtx(ctx)
	if origin.Host == "" {
		return "", errRequestOriginUnavailable
	}

	return origin.Scheme + "://" + origin.Host, nil
}
