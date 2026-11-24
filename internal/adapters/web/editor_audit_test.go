package web_test

import (
	"net/http"
	"net/http/httptest"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// TestEditPage_ContentType: the editor gives the stored format to the preview, so a text/plain
// note shows as plain text there, as on its page.
func (s *HandlerSuite) TestEditPage_ContentType() {
	for _, contentType := range []domain.ContentType{domain.ContentTypePlain, domain.ContentTypeMarkdown} {
		note := newTestNote("edit me", "# Plain heading")
		note.ContentType = new(contentType)

		s.manager.EXPECT().Inspect(gomock.Any(), testID, gomock.Any()).Return(note, nil)

		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil))

		s.Require().Equal(http.StatusOK, w.Code)
		s.Contains(w.Body.String(), `data-content-type="`+string(contentType)+`"`)
	}
}

// TestEditPage_LeadingNewline: the HTML parser removes one newline after <textarea>, so the
// template puts one there. Without it, a save removes the first newline of the content.
func (s *HandlerSuite) TestEditPage_LeadingNewline() {
	note := newTestNote("edit me", "\n    code\n")

	s.manager.EXPECT().Inspect(gomock.Any(), testID, gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil))

	s.Require().Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "spellcheck=\"false\">\n\n    code\n</textarea>")
}

// TestSuccessPage_BurnDelay: a note that burns some time after its first read has no expiry
// date yet. The success page shows the delay, not an immediate burn.
func (s *HandlerSuite) TestSuccessPage_BurnDelay() {
	for query, label := range map[string]string{
		"burn_ttl=300":                     "burns 5 minutes after the first read",
		"burn_ttl=86400":                   "burns 24 hours after the first read",
		"burn_ttl=123":                     "burns 123 seconds after the first read",
		"burn_ttl=0":                       "never expires",
		"burn_ttl=-5":                      "never expires",
		"burn_ttl=bad":                     "never expires",
		"expires=immediately&burn_ttl=300": "burns immediately after reading",
	} {
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/success?id=abc&burn=1&"+query, nil))

		s.Require().Equal(http.StatusOK, w.Code, query)
		s.Contains(w.Body.String(), label, query)
	}
}
