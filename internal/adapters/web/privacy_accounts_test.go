package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// These tests pin review R2 in the default configuration (s.router: no account system). A
// protected privacy level cannot be enforced there, so the `privacy` field may not request one;
// the deprecated v0.1.6 `private: true` stays accepted, as v0.1.6 accepted it without auth.

// privacyNeedsAccountsMessage is domain.ErrPrivacyNeedsAccounts as the API reports it.
const privacyNeedsAccountsMessage = "privacy authenticated and owner need the account system, " +
	"which is disabled on this server; send privacy public or omit it"

func (s *HandlerSuite) requirePrivacyNeedsAccounts(code int, body []byte) {
	s.T().Helper()

	s.Equal(http.StatusUnprocessableEntity, code)

	var decoded struct {
		Message string `json:"message"`
	}

	s.Require().NoError(json.Unmarshal(body, &decoded))
	s.Equal(privacyNeedsAccountsMessage, decoded.Message)
}

func (s *HandlerSuite) TestCreateNote_ProtectedPrivacyNeedsAccounts() {
	// No Create expectation: the strict mock fails the test if the note is stored.
	for _, level := range []string{"authenticated", "owner"} {
		w := s.sendJSON(http.MethodPost, "/notes", `{"content":"c","privacy":"`+level+`"}`)
		s.requirePrivacyNeedsAccounts(w.Code, w.Body.Bytes())
	}
}

func (s *HandlerSuite) TestCreateNote_PublicAndV016PrivateWithoutAccounts() {
	cases := []struct {
		body string
		want *domain.Privacy
	}{
		{`"privacy":"public"`, new(domain.PrivacyPublic)},
		// v0.1.6 compatibility: stored as authenticated, enforced once accounts are enabled.
		{`"private":true`, new(domain.PrivacyAuthenticated)},
	}

	for _, tcase := range cases {
		s.manager.EXPECT().Create(gomock.Any(), privacyMatcher(tcase.want)).Return(newTestNote("t", "c"), nil)

		w := s.sendJSON(http.MethodPost, "/notes", `{"content":"c",`+tcase.body+`}`)
		s.Equal(http.StatusCreated, w.Code, tcase.body)
	}
}

func (s *HandlerSuite) TestUpdateNote_ProtectedPrivacyNeedsAccounts() {
	// Both the generated single-segment route and the native path-like route apply the rule.
	for _, path := range []string{"/notes/" + testID, "/notes/project/GUIDE.md"} {
		w := s.sendJSON(http.MethodPut, path, `{"content":"c","edit_code":"editcode1234","privacy":"authenticated"}`)
		s.requirePrivacyNeedsAccounts(w.Code, w.Body.Bytes())
	}
}

// TestMutateV016PrivateNoteWithoutAccounts: a v0.1.6 private note (now authenticated) stays
// editable and deletable with its edit code, as in v0.1.6 without --auth-tokens; omitting privacy
// keeps the stored level.
func (s *HandlerSuite) TestMutateV016PrivateNoteWithoutAccounts() {
	s.manager.EXPECT().Update(gomock.Any(), testID, "editcode1234", uuid.Nil, privacyMatcher(nil)).
		Return(newTestNote("t", "edited"), nil)

	w := s.sendJSON(http.MethodPut, "/notes/"+testID, `{"content":"edited","edit_code":"editcode1234"}`)
	s.Equal(http.StatusOK, w.Code)

	s.manager.EXPECT().Delete(gomock.Any(), testID, "editcode1234", uuid.Nil).Return(nil)

	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("X-Edit-Code", "editcode1234")
	s.router.ServeHTTP(w, r)
	s.Equal(http.StatusNoContent, w.Code)
}

func (s *HandlerSuite) TestLinks_ProtectedPrivacyNeedsAccounts() {
	w := s.sendJSON(http.MethodPost, "/links", `{"target_url":"https://example.com","privacy":"authenticated"}`)
	s.requirePrivacyNeedsAccounts(w.Code, w.Body.Bytes())

	w = s.sendJSON(http.MethodPut, "/links/"+testLinkID,
		`{"target_url":"https://example.com/new","edit_code":"correct","privacy":"owner"}`)
	s.requirePrivacyNeedsAccounts(w.Code, w.Body.Bytes())

	w = s.sendJSON(http.MethodPost, "/links/bulk",
		`{"links":[{"target_url":"https://example.com","privacy":"authenticated"}]}`)
	s.Require().Equal(http.StatusOK, w.Code)

	var bulk struct {
		Results []struct {
			Error string `json:"error"`
		} `json:"results"`
	}

	s.Require().NoError(json.Unmarshal(w.Body.Bytes(), &bulk))
	s.Require().Len(bulk.Results, 1)
	s.Equal(privacyNeedsAccountsMessage, bulk.Results[0].Error)
}
