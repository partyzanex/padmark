package http_test

import (
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

//nolint:funlen // same attack and owner recovery across ogen notes, native notes and links
func (s *HandlerSuite) TestFailLockout_AnonymousAttackDoesNotBlockOwner() {
	owner := &domain.User{ID: uuid.New(), Username: "owner"}

	for _, testCase := range []struct {
		resource, id, body string
	}{
		{"notes", testID, `{"content":"updated"}`},
		{"notes", "project/GUIDE.md", `{"content":"updated"}`},
		{"links", testLinkID, `{"target_url":"https://example.com/new"}`},
	} {
		s.Run(testCase.id, func() {
			if testCase.resource == "notes" {
				note := newTestNote("title", "content")
				note.ID, note.OwnerID = testCase.id, &owner.ID
				s.manager.EXPECT().Peek(gomock.Any(), testCase.id).Return(note, nil).Times(10)
				s.manager.EXPECT().Update(gomock.Any(), testCase.id, "wrong", uuid.Nil, gomock.Any()).
					Return(nil, domain.ErrInvalidEditCode).Times(10)
				s.manager.EXPECT().Update(gomock.Any(), testCase.id, "", owner.ID, gomock.Any()).Return(note, nil)
				s.manager.EXPECT().Delete(gomock.Any(), testCase.id, "", owner.ID).Return(nil)
			} else {
				link := newTestLink()
				link.OwnerID = &owner.ID
				s.linkManager.EXPECT().Peek(gomock.Any(), testCase.id).Return(link, nil).Times(10)
				s.linkManager.EXPECT().Update(gomock.Any(), testCase.id, "wrong", uuid.Nil, gomock.Any(), gomock.Any()).
					Return(nil, domain.ErrInvalidEditCode).Times(10)
				s.linkManager.EXPECT().Update(gomock.Any(), testCase.id, "", owner.ID, gomock.Any(), gomock.Any()).
					Return(link, nil)
				s.linkManager.EXPECT().Delete(gomock.Any(), testCase.id, "", owner.ID).Return(nil)
			}

			s.authMgr.EXPECT().GetSession(gomock.Any(), "owner-session").Return(owner, nil)
			s.authMgr.EXPECT().ResolveAPIToken(gomock.Any(), "owner-key").Return(owner, nil)
			router := s.newRouterWithTOTPAuth()
			path := "/" + testCase.resource + "/" + testCase.id
			send := func(method, body, credential string) int {
				r := httptest.NewRequest(method, path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")

				if credential == "owner-session" {
					r.AddCookie(&http.Cookie{Name: "padmark_session", Value: credential}) //nolint:gosec // G124: test cookie
				} else if credential != "" {
					r.Header.Set("Authorization", "Bearer "+credential)
				}

				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)

				return w.Code
			}
			wrongBody := strings.TrimSuffix(testCase.body, "}") + `,"edit_code":"wrong"}`

			for range 10 {
				s.Equal(http.StatusForbidden, send(http.MethodPut, wrongBody, ""))
			}

			s.Equal(http.StatusTooManyRequests, send(http.MethodPut, wrongBody, ""))
			s.Equal(http.StatusTooManyRequests, send(http.MethodDelete, "", ""))
			s.Equal(http.StatusOK, send(http.MethodPut, testCase.body, "owner-session"))
			s.Equal(http.StatusNoContent, send(http.MethodDelete, "", "owner-key"))
			s.Equal(http.StatusTooManyRequests, send(http.MethodPut, wrongBody, ""),
				"owner success must not reset the attacker's counter")
		})
	}
}
