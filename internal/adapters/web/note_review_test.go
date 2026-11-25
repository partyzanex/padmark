package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

func (s *HandlerSuite) TestNoteTTLBoundaryValidation() {
	paths := []string{"/notes", "/notes/" + testID, "/notes/project/GUIDE.md", "/links", "/links/" + testLinkID}
	for _, path := range paths {
		for _, ttl := range []string{"-1", "9223372037", "9223372036854775807"} {
			method, content := http.MethodPut, `"content":"body"`
			if path == "/notes" || path == "/links" {
				method = http.MethodPost
			}

			if strings.HasPrefix(path, "/links") {
				content = `"target_url":"https://example.com"`
			}

			r := httptest.NewRequest(method, path, strings.NewReader(`{`+content+`,"ttl":`+ttl+`}`))
			r.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, r)
			s.Equal(http.StatusBadRequest, w.Code, path+" ttl="+ttl+" "+w.Body.String())
		}
	}
}

func (s *HandlerSuite) TestNativePutRequiresJSONContentType() {
	for _, contentType := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		r := httptest.NewRequest(http.MethodPut, "/notes/project/GUIDE.md", strings.NewReader(`{"content":"valid json"}`))
		r.Header.Set("Content-Type", contentType)

		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, r)
		s.Equal(http.StatusUnsupportedMediaType, w.Code)
		s.Contains(w.Header().Get("Content-Type"), "application/json")
	}
}

func (s *HandlerSuite) TestUpdateNotePlainTTL() {
	for _, id := range []string{testID, "project/GUIDE.md"} {
		for _, ttl := range []string{"", `,"ttl":null`, `,"ttl":3600`, `,"ttl":0`, `,"ttl":9223372036`} {
			s.manager.EXPECT().Update(gomock.Any(), id, "code", noCaller(), gomock.Any()).
				DoAndReturn(func(_ context.Context, _, _ string, _ domain.Reader, note *domain.Note) (*domain.Note, error) {
					s.Zero(note.BurnTTL)
					s.Equal(ttl == `,"ttl":null`, note.ClearExpiry)

					if ttl == "" || ttl == `,"ttl":null` {
						s.Nil(note.ExpiresAt)
					} else {
						s.Require().NotNil(note.ExpiresAt)

						switch ttl {
						case `,"ttl":3600`:
							s.WithinDuration(time.Now().Add(time.Hour), *note.ExpiresAt, time.Second)
						case `,"ttl":0`:
							s.WithinDuration(time.Now(), *note.ExpiresAt, time.Second)
						default:
							s.True(note.ExpiresAt.After(time.Now()), "largest supported TTL must not overflow")
						}
					}

					return newTestNote("updated", "body"), nil
				})
			r := httptest.NewRequest(http.MethodPut, "/notes/"+id,
				strings.NewReader(`{"content":"body","edit_code":"code"`+ttl+`}`))
			r.Header.Set("Content-Type", "application/json; charset=utf-8")

			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, r)
			s.Equal(http.StatusOK, w.Code, w.Body.String())
		}
	}
}

// TestUpdateNoteBurnTTLNullEqualsOmittedAndZero pins the UpdateNoteRequest.ttl contract with
// burn_after_reading=true: omitted, null and 0 all mean immediate burn, so they must reach the
// usecase as the same payload — in particular null must not set ClearExpiry and silently drop an
// existing hard expiry that omitted/0 keep.
func (s *HandlerSuite) TestUpdateNoteBurnTTLNullEqualsOmittedAndZero() {
	for _, id := range []string{testID, "project/GUIDE.md"} {
		for _, ttl := range []string{"", `,"ttl":null`, `,"ttl":0`} {
			s.manager.EXPECT().Update(gomock.Any(), id, "code", noCaller(), gomock.Any()).
				DoAndReturn(func(_ context.Context, _, _ string, _ domain.Reader, note *domain.Note) (*domain.Note, error) {
					s.True(note.BurnAfterReading, "ttl=%q", ttl)
					s.Zero(note.BurnTTL, "ttl=%q", ttl)
					s.False(note.ClearExpiry, "ttl=%q", ttl)
					s.Nil(note.ExpiresAt, "ttl=%q", ttl)

					return newTestNote("updated", "body"), nil
				})
			r := httptest.NewRequest(http.MethodPut, "/notes/"+id,
				strings.NewReader(`{"content":"body","edit_code":"code","burn_after_reading":true`+ttl+`}`))
			r.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, r)
			s.Equal(http.StatusOK, w.Code, w.Body.String())
		}
	}
}

func (s *HandlerSuite) TestCrossOriginMutationsRejectedBeforeUsecase() {
	for _, path := range []string{"/notes/" + testID, "/notes/project/GUIDE.md", "/links/" + testLinkID} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			body := strings.NewReader(`{"content":"body","target_url":"https://example.com"}`)
			req := httptest.NewRequest(method, path, body)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "https://evil.example")

			rec := httptest.NewRecorder()
			s.router.ServeHTTP(rec, req)
			s.Equal(http.StatusForbidden, rec.Code)
			s.Contains(rec.Body.String(), "cross-origin request rejected")
		}
	}
}
