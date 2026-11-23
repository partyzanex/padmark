package web_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// ── GetNote ──.

func (s *HandlerSuite) TestGetNote_JSON() {
	note := newTestNote("my title", "**bold**")
	note.Views = 3
	s.manager.EXPECT().Read(gomock.Any(), testID, gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal("my title", resp.Title)
	s.Equal("**bold**", resp.Content)
	s.Equal(3, resp.Views)
	s.Empty(resp.EditCode, "edit_code must not be exposed in GET")
}

func (s *HandlerSuite) TestGetNote_HTML() {
	note := newTestNote("HTML note", "# Heading\n\n**bold**")
	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, gomock.Any()).
		Return(note, "<h1>Heading</h1>\n<strong>bold</strong>", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")

	body := w.Body.String()
	s.Contains(body, "HTML note")
	s.Contains(body, "<h1>Heading</h1>")
	s.Contains(body, "<strong>bold</strong>")
	s.NotContains(body, "/static/katex/", "a note without formulas must not load KaTeX")
	s.NotContains(body, "/static/math.js")
}

// TestGetNote_HTML_LoadsKaTeXForFormulas checks that a note with a formula loads KaTeX from this
// server: the stylesheet in the head and the scripts with the page nonce.
func (s *HandlerSuite) TestGetNote_HTML_LoadsKaTeXForFormulas() {
	note := newTestNote("Mean", "$$\\bar x$$")
	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, gomock.Any()).
		Return(note, `<div class="math-display">\bar x</div>`, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Require().Equal(http.StatusOK, w.Code)

	body := w.Body.String()
	s.Contains(body, `<div class="math-display">\bar x</div>`)
	s.Regexp(`<link rel="stylesheet" href="/static/katex/katex\.min\.css\?p=[0-9a-f]+">`, body)
	s.Regexp(`<script src="/static/katex/katex\.min\.js\?p=[0-9a-f]+" nonce="[^"]+" defer>`, body)
	s.Regexp(`<script src="/static/math\.js\?p=[0-9a-f]+" nonce="[^"]+" defer`, body)
	s.Contains(body, `data-math-root="main"`, "math.js draws the note and its contents sidebar")
}

func (s *HandlerSuite) TestGetNote_HTML_WithExpiry() {
	note := newTestNote("expiring", "body")

	future := time.Now().Add(24 * time.Hour)
	note.ExpiresAt = &future

	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, gomock.Any()).
		Return(note, "<p>body</p>", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "Expires")
}

// TestGetNote_HTML_TimestampsUseSameISOFormat verifies that both the created and the
// expiry timestamps are emitted as UTC RFC 3339 data attributes, so the client formats
// them in one consistent timezone instead of mixing server-local and browser-local.
func (s *HandlerSuite) TestGetNote_HTML_TimestampsUseSameISOFormat() {
	note := newTestNote("tz", "body")
	note.CreatedAt = time.Date(2026, 6, 4, 3, 30, 0, 0, time.UTC)
	expires := time.Date(2026, 6, 4, 3, 35, 37, 0, time.UTC)
	note.ExpiresAt = &expires

	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, gomock.Any()).Return(note, "<p>body</p>", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	body := w.Body.String()
	s.Contains(body, `data-created="2026-06-04T03:30:00Z"`, "created must be emitted as UTC RFC3339")
	s.Contains(body, `data-expires="2026-06-04T03:35:37Z"`, "expires must be emitted as UTC RFC3339")
}

func (s *HandlerSuite) TestGetNote_HTML_NotFound() {
	s.manager.EXPECT().ReadRendered(gomock.Any(), "missing", gomock.Any()).Return(nil, "", domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/missing", nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")
	s.Contains(w.Body.String(), "not found")
}

func (s *HandlerSuite) TestGetNote_HTML_Expired() {
	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, gomock.Any()).Return(nil, "", domain.ErrExpired)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusGone, w.Code)
	s.Contains(w.Body.String(), "expired")
}

// TestGetNote_HTML_Forbidden: a privacy refusal of the usecase (ErrForbidden) gets the privacy
// answer of common.denyPrivate. Without accounts the request counts as signed in, so the page is
// the 403 "private to another account" page, not the sign-in redirect.
func (s *HandlerSuite) TestGetNote_HTML_Forbidden() {
	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, gomock.Any()).Return(nil, "", domain.ErrForbidden)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
	s.Contains(w.Body.String(), "Forbidden")
}

func (s *HandlerSuite) TestGetNote_HTML_InternalError() {
	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, gomock.Any()).Return(nil, "", errors.New("boom"))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
	s.Contains(w.Body.String(), "Internal server error")
}

func (s *HandlerSuite) TestGetNote_Plain() {
	note := newTestNote("plain note", "raw content here")
	note.ContentType = new(domain.ContentTypePlain)
	s.manager.EXPECT().Read(gomock.Any(), testID, gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/plain")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/plain")
	s.Equal("raw content here", w.Body.String())
}

func (s *HandlerSuite) TestGetNote_Plain_NotFound() {
	s.manager.EXPECT().Read(gomock.Any(), testID, gomock.Any()).Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/plain")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestGetNote_Markdown() {
	note := newTestNote("md note", "raw **md**")
	s.manager.EXPECT().Read(gomock.Any(), testID, gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/markdown")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/markdown")
	s.Equal("raw **md**", w.Body.String())
}

func (s *HandlerSuite) TestGetNote_RawQuery() {
	note := newTestNote("raw note", "raw content")
	s.manager.EXPECT().Read(gomock.Any(), testID, gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID+"?raw=1", nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/markdown")
	s.Equal("raw content", w.Body.String())
}

func (s *HandlerSuite) TestGetNote_UnknownAccept() {
	note := newTestNote("unknown", "body")
	s.manager.EXPECT().Read(gomock.Any(), testID, gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "image/png")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")
}

func (s *HandlerSuite) TestGetNote_AcceptWildcard() {
	note := newTestNote("wildcard", "body")
	s.manager.EXPECT().Read(gomock.Any(), testID, gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "*/*")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")
}

// TestGetNote_ImplicitBurnRead_Refused: a burn-after-reading note read without explicit intent
// (no Accept, wildcards only, HEAD, an <img> subresource) calls Read without burn intent; Read
// returns domain.ErrBurnIntentRequired and the answer is 409 — the note is neither consumed nor
// its timer started (H-1).
func (s *HandlerSuite) TestGetNote_ImplicitBurnRead_Refused() {
	cases := []struct {
		name    string
		method  string
		headers map[string]string
		burnTTL int64
	}{
		{"no Accept", http.MethodGet, map[string]string{}, 0},
		{"wildcard", http.MethodGet, map[string]string{"Accept": "*/*"}, 0},
		{"partial wildcard", http.MethodGet, map[string]string{"Accept": "text/*"}, 0},
		{"HEAD with explicit Accept", http.MethodHead, map[string]string{"Accept": "application/json"}, 0},
		{"img subresource", http.MethodGet, map[string]string{
			"Accept": "image/avif,image/webp,*/*;q=0.8", "Sec-Fetch-Dest": "image",
		}, 0},
		{"iframe with text/html", http.MethodGet, map[string]string{
			"Accept": "text/html", "Sec-Fetch-Dest": "iframe",
		}, 0},
		{"grace-period timer not started", http.MethodGet, map[string]string{"Accept": "*/*"}, 3600},
	}

	for _, testCase := range cases {
		s.Run(testCase.name, func() {
			// The usecase refuses a burn note to a reader without the burn intent.
			expectNoteReadError(s.manager, testID, withBurn(false), domain.ErrBurnIntentRequired)

			w := httptest.NewRecorder()
			r := httptest.NewRequest(testCase.method, "/"+testID, nil)

			for name, value := range testCase.headers {
				r.Header.Set(name, value)
			}

			s.router.ServeHTTP(w, r)

			s.Equal(http.StatusConflict, w.Code)
			s.NotContains(w.Body.String(), "burn-body")

			if testCase.method == http.MethodGet {
				s.Contains(w.Body.String(), `"code":"burn_read_not_explicit"`)
			}
		})
	}
}

// TestGetNote_ExplicitBurnRead_Consumes: naming the format exactly is the intent a burning read
// needs — the reader of the request has the burn intent, so Read consumes the note.
func (s *HandlerSuite) TestGetNote_ExplicitBurnRead_Consumes() {
	for _, accept := range []string{"application/json", "text/markdown", "application/json, */*;q=0.1"} {
		s.Run(accept, func() {
			note := newTestNote("burn", "burn-body")
			note.BurnAfterReading = true

			s.manager.EXPECT().Read(gomock.Any(), testID, withBurn(true)).Return(note, nil)

			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
			r.Header.Set("Accept", accept)
			r.Header.Set("Sec-Fetch-Dest", "empty")

			s.router.ServeHTTP(w, r)

			s.Equal(http.StatusOK, w.Code)
			s.Contains(w.Body.String(), "burn-body")
		})
	}
}

// TestGetNote_ImplicitRead_NotFound: an implicit read of a missing note gets the not-found
// error of the usecase as is.
func (s *HandlerSuite) TestGetNote_ImplicitRead_NotFound() {
	s.manager.EXPECT().Read(gomock.Any(), "nonexistent", withBurn(false)).Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/nonexistent", nil)
	// No Accept at all on /notes/ is a v0.1.6 API read (legacyAPIRead); */* stays implicit.
	r.Header.Set("Accept", "*/*")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestGetNote_NotFound() {
	s.manager.EXPECT().Read(gomock.Any(), "nonexistent", gomock.Any()).Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/nonexistent", nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestGetNote_Expired() {
	s.manager.EXPECT().Read(gomock.Any(), testID, gomock.Any()).Return(nil, domain.ErrExpired)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusGone, w.Code)
}

func (s *HandlerSuite) TestGetNote_ShortURL() {
	note := newTestNote("short", "body")
	// GET /{id} reads the note once; the same read would tell the kind of a link or a file.
	s.manager.EXPECT().Read(gomock.Any(), testID, gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal("short", resp.Title)
}

func (s *HandlerSuite) TestGetNote_Private_HTML_Unauthorized() {
	// The usecase refuses the anonymous reader; the handler sends the browser to the sign-in page.
	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, anonymousReader()).Return(nil, "", domain.ErrForbidden)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login?next="),
		"should redirect to /login with ?next= param")
}

func (s *HandlerSuite) TestGetNote_Private_JSON_Unauthorized() {
	s.manager.EXPECT().Read(gomock.Any(), testID, anonymousReader()).Return(nil, domain.ErrForbidden)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

// ── Multi-segment (path-like) slug support ──.

func (s *HandlerSuite) TestGetNote_MultiSegmentSlug_PublicServed() {
	// A path-like slug (project/GUIDE.md) is a public note: served without auth even though the
	// URL contains slashes. Proves the auth middleware treats a non-reserved first segment as a
	// note route rather than a protected one.
	const mid = "project/GUIDE.md"

	note := newTestNote("Guide", "# body")

	s.manager.EXPECT().Read(gomock.Any(), mid, gomock.Any()).Return(note, nil)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+mid, nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestGetNote_MultiSegmentSlug_PrivateRequiresAuth() {
	// Per-note privacy still applies to path-like slugs: a private multi-segment note is not
	// served to an unauthenticated caller.
	const mid = "project/secret.md"

	s.manager.EXPECT().Read(gomock.Any(), mid, anonymousReader()).Return(nil, domain.ErrForbidden)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+mid, nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

// TestEditPage_MultiSegmentPath_GatedByPrivacy verifies the /edit/{id...} route's gating: GET
// /edit/<multi-segment-slug> is admitted by isPublicRoute (the editor's save still requires the
// edit code on the PUT), but per-note privacy is enforced by the usecase (Inspect) — a public
// note renders the form, a privacy=owner note redirects an anonymous caller to /login.
func (s *HandlerSuite) TestEditPage_MultiSegmentPath_GatedByPrivacy() {
	router := s.newRouterWithTOTPAuth()

	public := newTestNote("guide", "# guide")

	s.manager.EXPECT().Inspect(gomock.Any(), "project/GUIDE.md", anonymousReader()).Return(public, nil)
	s.manager.EXPECT().Inspect(gomock.Any(), "project/secret.md", anonymousReader()).Return(nil, domain.ErrForbidden)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/project/GUIDE.md", nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), `const noteID = "abc-123"`)

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/edit/project/secret.md", nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.True(strings.HasPrefix(w.Header().Get("Location"), "/login?next="),
		"privacy=owner note must redirect an anonymous caller to login, not render the editor")
}

func (s *HandlerSuite) TestGetNote_Private_NotFound() {
	s.manager.EXPECT().Read(gomock.Any(), "missing", gomock.Any()).Return(nil, domain.ErrNotFound)

	router := s.newRouterWithTOTPAuth()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/missing", nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

// ── writeError default branch ──.

func (s *HandlerSuite) TestGetNote_Plain_Expired() {
	s.manager.EXPECT().Read(gomock.Any(), testID, gomock.Any()).Return(nil, domain.ErrExpired)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/plain")
	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusGone, w.Code)
}

func (s *HandlerSuite) TestGetNote_Plain_InternalError() {
	s.manager.EXPECT().Read(gomock.Any(), testID, gomock.Any()).Return(nil, errors.New("unexpected db error"))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/plain")
	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
}
