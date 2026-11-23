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

// errGateStorage is a storage failure of the read inside the usecase mutation. Its text must
// never reach the client: the central error mapping answers 500 with a constant message.
var errGateStorage = errors.New("pq: connection refused by 10.0.0.5:5432")

// gateCase is one mutation route. The usecase reads the record and checks privacy before the
// change; the handler only translates the error.
type gateCase struct {
	name   string
	method string
	path   string
	body   string
	// mutate sets the Update or Delete expectation of the route, which returns err.
	mutate func(err error)
}

// gatePeekError is one error of the usecase mutation and the status that it must give.
type gatePeekError struct {
	name   string
	err    error
	status int
}

// gatePeekErrors returns the usecase errors that each mutation route must translate. A hidden
// record gives 401, as for a read, so that it does not count as a wrong edit code.
func gatePeekErrors() []gatePeekError {
	return []gatePeekError{
		{"storage failure", errGateStorage, http.StatusInternalServerError},
		{"missing record", domain.ErrNotFound, http.StatusNotFound},
		{"hidden record", domain.ErrForbidden, http.StatusUnauthorized},
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

// assertGateError checks that the handler answered the usecase error with the expected status and
// without the internal error text.
func assertGateError(check *assert.Assertions, w *httptest.ResponseRecorder, status int) {
	check.Equal(status, w.Code, w.Body.String())
	check.NotContains(w.Body.String(), "10.0.0.5")
	check.NotContains(w.Body.String(), "connection refused")
	check.NotContains(w.Body.String(), "check ")
}

// TestMutationGate_PeekError_StopsMutation covers audit finding 7 for notes and links: with
// accounts on, the usecase reads the record and checks privacy inside Update and Delete. A
// storage failure gives 500, a missing record 404 and a hidden record 401, on each PUT and
// DELETE route, without the internal error text.
func (s *HandlerSuite) TestMutationGate_PeekError_StopsMutation() {
	noteUpdate := func(id string) func(error) {
		return func(err error) {
			s.manager.EXPECT().Update(gomock.Any(), id, "right-code", anonymousReader(), gomock.Any()).Return(nil, err)
		}
	}
	noteDelete := func(id string) func(error) {
		return func(err error) {
			s.manager.EXPECT().Delete(gomock.Any(), id, "right-code", anonymousReader()).Return(err)
		}
	}
	linkUpdate := func(err error) {
		s.linkManager.EXPECT().
			Update(gomock.Any(), testLinkID, "right-code", anonymousReader(), gomock.Any(), gomock.Any()).
			Return(nil, err)
	}
	linkDelete := func(err error) {
		s.linkManager.EXPECT().Delete(gomock.Any(), testLinkID, "right-code", anonymousReader()).Return(err)
	}

	cases := []gateCase{
		{"ogen note PUT", http.MethodPut, "/notes/" + testID, `{"title":"t","content":"c","edit_code":"right-code"}`,
			noteUpdate(testID)},
		{"ogen note DELETE", http.MethodDelete, "/notes/" + testID, "", noteDelete(testID)},
		{"native note PUT", http.MethodPut, "/notes/project/GUIDE.md",
			`{"title":"t","content":"c","edit_code":"right-code"}`, noteUpdate("project/GUIDE.md")},
		{"native note DELETE", http.MethodDelete, "/notes/project/GUIDE.md", "", noteDelete("project/GUIDE.md")},
		{"ogen link PUT", http.MethodPut, "/links/" + testLinkID,
			`{"target_url":"https://example.com/new","edit_code":"right-code"}`, linkUpdate},
		{"ogen link DELETE", http.MethodDelete, "/links/" + testLinkID, "", linkDelete},
	}

	for _, route := range cases {
		for _, peekErr := range gatePeekErrors() {
			s.Run(route.name+"/"+peekErr.name, func() {
				router := s.newRouterWithTOTPAuth()

				route.mutate(peekErr.err)

				w := httptest.NewRecorder()
				router.ServeHTTP(w, gateRequest(route))

				assertGateError(s.Assert(), w, peekErr.status)
			})
		}
	}
}

// TestMutationGate_PeekError_StopsMutation is the files counterpart of the notes and links test:
// PUT and DELETE /files/{id} translate the usecase errors with the same statuses.
func (s *FileHandlerSuite) TestMutationGate_PeekError_StopsMutation() {
	cases := []gateCase{
		{"file PUT", http.MethodPut, "/files/abc", `{"ttl":60}`, func(err error) {
			s.files.EXPECT().Update(gomock.Any(), "abc", "right-code", anonymousReader(), gomock.Any()).Return(nil, err)
		}},
		{"file DELETE", http.MethodDelete, "/files/abc", "", func(err error) {
			s.files.EXPECT().Delete(gomock.Any(), "abc", "right-code", anonymousReader()).Return(err)
		}},
	}

	for _, route := range cases {
		for _, peekErr := range gatePeekErrors() {
			s.Run(route.name+"/"+peekErr.name, func() {
				router := s.newAuthRouter(s.auth)

				route.mutate(peekErr.err)

				w := httptest.NewRecorder()
				router.ServeHTTP(w, gateRequest(route))

				assertGateError(s.Assert(), w, peekErr.status)
			})
		}
	}
}
