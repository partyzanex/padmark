package middleware

import (
	"context"
	"net/http"
)

type legacyNoteEditCodeKey struct{}

// WithLegacyNoteEditCode captures the v0.1.6 query credential at the HTTP boundary.
// Register it only for note deletion. The handler still checks privacy before using it,
// gives X-Edit-Code precedence, and logs legacy use without exposing the credential.
func WithLegacyNoteEditCode(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Edit-Code") == "" {
			if code := r.URL.Query().Get("edit_code"); code != "" {
				ctx := context.WithValue(r.Context(), legacyNoteEditCodeKey{}, code)
				r = r.WithContext(ctx)
			}
		}

		next.ServeHTTP(w, r)
	})
}

// LegacyNoteEditCodeFromCtx returns a query credential captured by WithLegacyNoteEditCode.
func LegacyNoteEditCodeFromCtx(ctx context.Context) string {
	code, _ := ctx.Value(legacyNoteEditCodeKey{}).(string)

	return code
}
