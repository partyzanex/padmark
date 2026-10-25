package middleware

import (
	"context"
	_ "embed"
	"net/http"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

const SessionCookieName = "padmark_session"

func ExtractSessionID(r *http.Request) string {
	value, ok := SingleCookieValue(r, SessionCookieName)
	if !ok {
		return ""
	}

	return value
}

// SingleCookieValue returns the value of the named cookie only when exactly one such cookie was
// sent. A sibling subdomain can plant a same-name cookie (Domain=parent, longer Path) that the
// browser orders before the legitimate host-only one, and Request.Cookie would silently pick the
// first; several same-name cookies are therefore treated as ambiguous and yield no value.
func SingleCookieValue(r *http.Request, name string) (string, bool) {
	var (
		value string
		found int
	)

	for _, cookie := range r.Cookies() {
		if cookie.Name == name {
			value = cookie.Value
			found++
		}
	}

	if found != 1 {
		return "", false
	}

	return value, true
}

func UserFromContext(r *http.Request) *domain.User {
	return UserFromCtx(r.Context())
}

// UserFromCtx is the context-only counterpart of UserFromContext, for ogen handlers (OgenHandler
// methods receive only a context.Context, not *http.Request) — see NewRouter for why the
// auth-middleware-enriched context still reaches them: ogen dispatches using r.Context().
func UserFromCtx(ctx context.Context) *domain.User {
	usr, _ := ctx.Value(keyUser).(*domain.User)

	return usr
}

// APITokenHashFromCtx returns the public ID (hash) of the bearer API token the request
// authenticated with, or "" for a browser session or an anonymous caller. Credential-issuing
// operations use it to demand a step-up from a bearer and to record it as the new key's issuer.
func APITokenHashFromCtx(ctx context.Context) string {
	hash, _ := ctx.Value(keyAPIToken).(string)

	return hash
}

// OwnerIDFromCtx returns the authenticated caller's user ID for stamping domain.Note.OwnerID at
// creation, or nil for an anonymous caller (note stays unowned, exactly like before this field
// existed).
func OwnerIDFromCtx(ctx context.Context) *uuid.UUID {
	usr := UserFromCtx(ctx)
	if usr == nil {
		return nil
	}

	return &usr.ID
}

// CallerIDFromCtx returns the authenticated caller's user ID for the owner-bypass check in
// notes.Manager.Update/Delete, or uuid.Nil for an anonymous caller (never matches an owner).
func CallerIDFromCtx(ctx context.Context) uuid.UUID {
	usr := UserFromCtx(ctx)
	if usr == nil {
		return uuid.Nil
	}

	return usr.ID
}

// AuthenticatedFromCtx reports whether the request resolved to a TOTP session or API-token user
// — the context-only counterpart of common.isAuthenticated's context check, for ogen handlers
// (see UserFromCtx). Unlike common.isAuthenticated, it does NOT special-case "no auth configured
// at all" (context has no way to know that). Callers that need that must use
// common.isAuthenticated instead; this exists for handlers that only see a context.Context,
// e.g. OgenHandler.GetLink.
func AuthenticatedFromCtx(ctx context.Context) bool {
	return UserFromCtx(ctx) != nil
}
