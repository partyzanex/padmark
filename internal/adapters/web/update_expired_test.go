package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// expiredBody is the JSON error body of an update of an expired record.
const expiredBody = `{"message":"expired"}`

// TestUpdate_ExpiredRecordIsGone: the managers refuse an update of an expired note or link with
// domain.ErrExpired. Each update route answers 410 with the usual JSON error body: the ogen routes
// PUT /notes/{id} and PUT /links/{id}, and the native route PUT /notes/{id...}.
func (s *HandlerSuite) TestUpdate_ExpiredRecordIsGone() {
	for _, testCase := range []struct {
		name, path, body string
		expect           func()
	}{
		{
			name: "note", path: "/notes/" + testID,
			body: `{"title":"t","content":"c","edit_code":"code"}`,
			expect: func() {
				s.manager.EXPECT().Update(gomock.Any(), testID, "code", noCaller(), gomock.Any()).
					Return(nil, domain.ErrExpired)
			},
		},
		{
			name: "multi-segment note", path: "/notes/project/GUIDE.md",
			body: `{"title":"t","content":"c","edit_code":"code"}`,
			expect: func() {
				s.manager.EXPECT().Update(gomock.Any(), "project/GUIDE.md", "code", noCaller(), gomock.Any()).
					Return(nil, domain.ErrExpired)
			},
		},
		{
			name: "link", path: "/links/" + testLinkID,
			body: `{"target_url":"https://example.com/new","edit_code":"code"}`,
			expect: func() {
				s.linkManager.EXPECT().Update(gomock.Any(), testLinkID, "code", noCaller(), gomock.Any(), gomock.Any()).
					Return(nil, domain.ErrExpired)
			},
		},
	} {
		s.Run(testCase.name, func() {
			testCase.expect()

			r := httptest.NewRequest(http.MethodPut, testCase.path, strings.NewReader(testCase.body))
			r.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, r)

			s.Equal(http.StatusGone, w.Code)
			s.Contains(w.Header().Get("Content-Type"), "application/json")
			s.JSONEq(expiredBody, w.Body.String())
		})
	}
}

// TestUpdate_ExpiredFileIsGone: the native route PUT /files/{id} answers 410 with the usual JSON
// error body when files.Manager refuses an update of an expired file.
func (s *FileHandlerSuite) TestUpdate_ExpiredFileIsGone() {
	s.files.EXPECT().Update(gomock.Any(), "abc", "code-1234", access(false), gomock.Any()).
		Return(nil, domain.ErrExpired)

	w := s.serve(updateRequest(`{"ttl":null}`, "code-1234"))

	s.Equal(http.StatusGone, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")
	s.JSONEq(expiredBody, w.Body.String())
}
