package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// fragmentTarget is the target URL from the bug report: "#" must not become "%23".
const fragmentTarget = "https://www.hostinger.com/pricing/vps-hosting#plan-selector"

// fragmentTargets returns target URLs that ogen decodes with url.ParseRequestURI. The handler
// must give each one to links.Manager in the form that the client sent.
func fragmentTargets() []string {
	return []string{
		fragmentTarget,
		"https://example.com/#top",
		"https://example.com/a?x=1#frag",
		"https://example.com/a#b?c",
		"https://example.com/p%20q#f",
		"https://example.com/a%23b",
		"https://example.com/plain",
	}
}

func (s *HandlerSuite) TestCreateLink_KeepsFragment() {
	for _, target := range fragmentTargets() {
		s.Run(target, func() {
			s.linkManager.EXPECT().Create(gomock.Any(), target, gomock.Any()).Return(newTestLink(), nil)

			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/links",
				strings.NewReader(`{"target_url":"`+target+`"}`))
			r.Header.Set("Content-Type", "application/json")

			s.router.ServeHTTP(w, r)

			s.Equal(http.StatusCreated, w.Code)
		})
	}
}

// TestCreateLinksBulk_KeepsFragment checks that the bulk handler gives each target to
// links.Manager in the form that the client sent, and echoes that form in the result, also for
// an item that fails.
func (s *HandlerSuite) TestCreateLinksBulk_KeepsFragment() {
	for _, target := range fragmentTargets() {
		for name, createErr := range map[string]error{"created": nil, "failed": domain.ErrSlugConflict} {
			s.Run(name+" "+target, func() {
				if createErr == nil {
					s.linkManager.EXPECT().Create(gomock.Any(), target, gomock.Any()).Return(newTestLink(), nil)
				} else {
					s.linkManager.EXPECT().Create(gomock.Any(), target, gomock.Any()).Return(nil, createErr)
				}

				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodPost, "/links/bulk",
					strings.NewReader(`{"links":[{"target_url":"`+target+`"}]}`))
				r.Header.Set("Content-Type", "application/json")

				s.router.ServeHTTP(w, r)

				s.Equal(http.StatusOK, w.Code)
				s.Contains(w.Body.String(), `"target_url":"`+target+`"`, "the echo keeps the client form")
			})
		}
	}
}

func (s *HandlerSuite) TestUpdateLink_KeepsFragment() {
	target := fragmentTarget

	s.linkManager.EXPECT().
		Update(gomock.Any(), testLinkID, "original", openReader(), target, gomock.Any()).
		Return(newTestLink(), nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/links/"+testLinkID,
		strings.NewReader(`{"target_url":"`+target+`","edit_code":"original"}`))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}
