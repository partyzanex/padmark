package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// errGateStorage is a storage failure of the privacy-check read. Its text must never reach the
// client: the central error mapping answers 500 with a constant message.
var errGateStorage = errors.New("pq: connection refused by 10.0.0.5:5432")

// gateCase is one mutation route that has a privacy check before the change.
type gateCase struct {
	name   string
	method string
	path   string
	body   string
	// peek sets the Peek expectation of the record kind of the route.
	peek func(err error)
}

// gatePeekError is one Peek error of the privacy check and the status that it must give.
type gatePeekError struct {
	name   string
	err    error
	status int
}

// gatePeekErrors returns the Peek errors that each mutation route must stop on.
func gatePeekErrors() []gatePeekError {
	return []gatePeekError{
		{"storage failure", errGateStorage, http.StatusInternalServerError},
		{"missing record", domain.ErrNotFound, http.StatusNotFound},
	}
}

// gateRequest makes the request of one gate case. It sends an edit code in the body and in the
// header, so that only the privacy check can stop the change.
func gateRequest(route gateCase) *http.Request {
	r := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
	r.Header.Set("Accept", "application/json")
	r.Header.Set("X-Edit-Code", "right-code")

	if route.body != "" {
		r.Header.Set("Content-Type", "application/json")
	}

	return r
}

// assertGateError checks that the privacy check stopped the change with the expected status and
// without the internal error text. The strict gomock controller fails the test if the handler
// calls Update or Delete, because no test sets an expectation for them.
func assertGateError(check *assert.Assertions, w *httptest.ResponseRecorder, status int) {
	check.Equal(status, w.Code, w.Body.String())
	check.NotContains(w.Body.String(), "10.0.0.5")
	check.NotContains(w.Body.String(), "connection refused")
	check.NotContains(w.Body.String(), "check ")
}

// TestMutationGate_PeekError_StopsMutation covers audit finding 7 for notes and links: with
// accounts on, a failed Peek of the privacy check stops each PUT and DELETE route. A storage
// failure gives 500 and a missing record gives 404; the usecase mutation is never called.
func (s *HandlerSuite) TestMutationGate_PeekError_StopsMutation() {
	notePeek := func(id string) func(error) {
		return func(err error) { s.manager.EXPECT().Peek(gomock.Any(), id).Return(nil, err) }
	}
	linkPeek := func(err error) {
		s.linkManager.EXPECT().Peek(gomock.Any(), testLinkID).Return(nil, err)
	}

	cases := []gateCase{
		{"ogen note PUT", http.MethodPut, "/notes/" + testID, `{"title":"t","content":"c","edit_code":"right-code"}`,
			notePeek(testID)},
		{"ogen note DELETE", http.MethodDelete, "/notes/" + testID, "", notePeek(testID)},
		{"native note PUT", http.MethodPut, "/notes/project/GUIDE.md",
			`{"title":"t","content":"c","edit_code":"right-code"}`, notePeek("project/GUIDE.md")},
		{"native note DELETE", http.MethodDelete, "/notes/project/GUIDE.md", "", notePeek("project/GUIDE.md")},
		{"ogen link PUT", http.MethodPut, "/links/" + testLinkID,
			`{"target_url":"https://example.com/new","edit_code":"right-code"}`, linkPeek},
		{"ogen link DELETE", http.MethodDelete, "/links/" + testLinkID, "", linkPeek},
	}

	for _, route := range cases {
		for _, peekErr := range gatePeekErrors() {
			s.Run(route.name+"/"+peekErr.name, func() {
				router := s.newRouterWithTOTPAuth()

				route.peek(peekErr.err)

				w := httptest.NewRecorder()
				router.ServeHTTP(w, gateRequest(route))

				assertGateError(s.Assert(), w, peekErr.status)
			})
		}
	}
}

// TestMutationGate_PeekError_StopsMutation is the files counterpart of the notes and links test:
// PUT and DELETE /files/{id} stop on a failed Peek, with the same statuses.
func (s *FileHandlerSuite) TestMutationGate_PeekError_StopsMutation() {
	cases := []gateCase{
		{"file PUT", http.MethodPut, "/files/abc", `{"ttl":60}`, nil},
		{"file DELETE", http.MethodDelete, "/files/abc", "", nil},
	}

	for _, route := range cases {
		for _, peekErr := range gatePeekErrors() {
			s.Run(route.name+"/"+peekErr.name, func() {
				router := s.newAuthRouter(NewMockAuthManager(s.ctrl))
				s.files.EXPECT().Peek(gomock.Any(), "abc").Return(nil, peekErr.err)

				w := httptest.NewRecorder()
				router.ServeHTTP(w, gateRequest(route))

				assertGateError(s.Assert(), w, peekErr.status)
			})
		}
	}
}
