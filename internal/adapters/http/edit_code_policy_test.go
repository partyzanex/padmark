package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// postJSON sends body to path as application/json through the suite router and returns the
// status and the decoded ErrorResponse message ("" when the body is not an error envelope).
func (s *HandlerSuite) postJSON(path, body string) (status int, message string) {
	s.T().Helper()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	var decoded struct {
		Message string `json:"message"`
	}

	_ = json.NewDecoder(w.Body).Decode(&decoded)

	return w.Code, decoded.Message
}

// TestCreateNote_ShortEditCodeRejected pins finding M-8 at the API contract: the schema's
// minLength on CreateNoteRequest.edit_code makes ogen reject a caller-chosen code shorter than
// domain.MinEditCodeLength with 400 and the standard ErrorResponse naming the field, before
// the manager is ever reached (no EXPECT — gomock fails the test on an unexpected call).
func (s *HandlerSuite) TestCreateNote_ShortEditCodeRejected() {
	for _, code := range []string{"1", "1234567"} {
		status, message := s.postJSON("/notes", `{"title":"t","content":"c","edit_code":"`+code+`"}`)

		s.Equal(http.StatusBadRequest, status, "code %q", code)
		s.True(strings.HasPrefix(message, "invalid request body: edit_code"), "message %q", message)
	}
}

// TestCreateNote_MinLengthEditCodeAccepted is the boundary control for the schema rule.
func (s *HandlerSuite) TestCreateNote_MinLengthEditCodeAccepted() {
	note := newTestNote("t", "c")
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(note, nil)

	status, _ := s.postJSON("/notes", `{"title":"t","content":"c","edit_code":"12345678"}`)

	s.Equal(http.StatusCreated, status)
}

// TestCreateNote_EditCodePolicyErrorIs422 pins the status for the usecase-level sentinels
// (whitespace-padded codes pass the schema's length check but not domain.ValidateEditCode):
// 422, like every other domain validation error on create (title too long, invalid slug).
func (s *HandlerSuite) TestCreateNote_EditCodePolicyErrorIs422() {
	for _, sentinel := range []error{domain.ErrEditCodeTooShort, domain.ErrEditCodeMalformed} {
		s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, sentinel)

		status, message := s.postJSON("/notes", `{"title":"t","content":"c","edit_code":" 12345678 "}`)

		s.Equal(http.StatusUnprocessableEntity, status, "sentinel %v", sentinel)
		s.Equal(sentinel.Error(), message)
	}
}

// TestCreateLink_ShortEditCodeRejected mirrors TestCreateNote_ShortEditCodeRejected for links.
func (s *HandlerSuite) TestCreateLink_ShortEditCodeRejected() {
	status, message := s.postJSON("/links", `{"target_url":"https://example.com/dest","edit_code":"1"}`)

	s.Equal(http.StatusBadRequest, status)
	s.True(strings.HasPrefix(message, "invalid request body: "), "message %q", message)
	s.Contains(message, "edit_code")
}

// TestCreateLinksBulk_ShortEditCodeRejected: bulk items share CreateLinkRequest, so one short
// caller-chosen code rejects the whole request at the schema, before any link is created.
func (s *HandlerSuite) TestCreateLinksBulk_ShortEditCodeRejected() {
	body := `{"links":[{"target_url":"https://example.com/a"},` +
		`{"target_url":"https://example.com/b","edit_code":"1234567"}]}`

	status, message := s.postJSON("/links/bulk", body)

	s.Equal(http.StatusBadRequest, status)
	s.Contains(message, "edit_code")
}
