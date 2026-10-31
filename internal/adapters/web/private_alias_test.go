package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// conflictMessage is the 400 message for a private/privacy pair that disagrees.
const conflictMessage = "private conflicts with privacy: private=true requires privacy authenticated or owner, " +
	"private=false requires public"

// privacyMatcher matches a *domain.Note whose Privacy equals want (nil when want is nil).
//
//nolint:ireturn // gomock matchers are interfaces by design
func privacyMatcher(want *domain.Privacy) gomock.Matcher {
	return gomock.Cond(func(x any) bool {
		note, ok := x.(*domain.Note)
		if !ok {
			return false
		}

		if want == nil || note.Privacy == nil {
			return want == nil && note.Privacy == nil
		}

		return *want == *note.Privacy
	})
}

func (s *HandlerSuite) sendJSON(method, path, body string) *httptest.ResponseRecorder {
	s.T().Helper()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	return w
}

// TestCreateNote_PrivateAlias pins the deprecated v0.1.6 `private` boolean: it maps onto privacy
// before the manager sees the note, and a disagreeing pair is a 400 that never reaches the manager.
func (s *HandlerSuite) TestCreateNote_PrivateAlias() {
	// Protected privacy levels need the account system (requirePrivacySupport).
	s.router = s.newRouterWithTOTPAuth()
	cases := []struct {
		name string
		body string
		want *domain.Privacy
	}{
		{"true", `"private":true`, new(domain.PrivacyAuthenticated)},
		{"false", `"private":false`, new(domain.PrivacyPublic)},
		{"omitted", `"slug_unused":1`, nil},
		{"true+authenticated", `"private":true,"privacy":"authenticated"`, new(domain.PrivacyAuthenticated)},
		{"true+owner", `"private":true,"privacy":"owner"`, new(domain.PrivacyOwner)},
		{"false+public", `"private":false,"privacy":"public"`, new(domain.PrivacyPublic)},
	}

	for _, tcase := range cases {
		s.manager.EXPECT().Create(gomock.Any(), privacyMatcher(tcase.want)).Return(newTestNote("t", "c"), nil)

		w := s.sendJSON(http.MethodPost, "/notes", `{"content":"c",`+tcase.body+`}`)

		s.Equal(http.StatusCreated, w.Code, tcase.name)
	}
}

func (s *HandlerSuite) TestCreateNote_PrivateAliasConflict() {
	for _, body := range []string{
		`"private":true,"privacy":"public"`,
		`"private":false,"privacy":"authenticated"`,
		`"private":false,"privacy":"owner"`,
	} {
		w := s.sendJSON(http.MethodPost, "/notes", `{"content":"c",`+body+`}`)

		s.Equal(http.StatusBadRequest, w.Code, body)

		var decoded struct {
			Message string `json:"message"`
		}

		s.Require().NoError(json.NewDecoder(w.Body).Decode(&decoded))
		s.Equal(conflictMessage, decoded.Message, body)
	}
}

func (s *HandlerSuite) TestUpdateNote_PrivateAlias() {
	// Protected privacy levels need the account system (requirePrivacySupport).
	s.router = s.newRouterWithTOTPAuth()
	cases := []struct {
		name string
		body string
		want *domain.Privacy
	}{
		{"true", `"private":true`, new(domain.PrivacyAuthenticated)},
		{"false", `"private":false`, new(domain.PrivacyPublic)},
		{"omitted", `"title":"x"`, nil},
		{"true+owner", `"private":true,"privacy":"owner"`, new(domain.PrivacyOwner)},
	}

	// Anonymous mutation consults Peek first; a public note lets the request through.
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(newTestNote("t", "c"), nil).AnyTimes()

	for _, tcase := range cases {
		s.manager.EXPECT().Update(gomock.Any(), testID, "editcode1234", uuid.Nil, privacyMatcher(tcase.want)).
			Return(newTestNote("t", "c"), nil)

		w := s.sendJSON(http.MethodPut, "/notes/"+testID, `{"content":"c","edit_code":"editcode1234",`+tcase.body+`}`)

		s.Equal(http.StatusOK, w.Code, tcase.name)
	}
}

func (s *HandlerSuite) TestUpdateNote_PrivateAliasConflict() {
	w := s.sendJSON(http.MethodPut, "/notes/"+testID,
		`{"content":"c","edit_code":"editcode1234","private":false,"privacy":"owner"}`)

	s.Equal(http.StatusBadRequest, w.Code)
	s.Contains(w.Body.String(), conflictMessage)
}

// TestNoteResponses_PrivateField: responses carry `private` = (privacy != public).
func (s *HandlerSuite) TestNoteResponses_PrivateField() {
	cases := map[domain.Privacy]bool{
		domain.PrivacyPublic:        false,
		domain.PrivacyAuthenticated: true,
		domain.PrivacyOwner:         true,
	}

	for level, want := range cases {
		note := newTestNote("t", "c")
		note.Privacy = new(level)

		s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(note, nil)

		w := s.sendJSON(http.MethodPost, "/notes", `{"content":"c"}`)
		s.Require().Equal(http.StatusCreated, w.Code)

		var created map[string]any

		s.Require().NoError(json.NewDecoder(w.Body).Decode(&created))
		s.Equal(want, created["private"], "create %s", level)
		s.Equal(string(level), created["privacy"])
	}
}

// TestUpdateNoteByPath_PrivateAlias covers the native multi-segment PUT, which shares the mapping.
func (s *HandlerSuite) TestUpdateNoteByPath_PrivateAlias() {
	s.manager.EXPECT().Peek(gomock.Any(), "dir/note").Return(newTestNote("t", "c"), nil).AnyTimes()
	s.manager.EXPECT().
		Update(gomock.Any(), "dir/note", "editcode1234", uuid.Nil, privacyMatcher(new(domain.PrivacyAuthenticated))).
		Return(newTestNote("t", "c"), nil)

	w := s.sendJSON(http.MethodPut, "/notes/dir/note", `{"content":"c","edit_code":"editcode1234","private":true}`)
	s.Equal(http.StatusOK, w.Code)

	w = s.sendJSON(http.MethodPut, "/notes/dir/note",
		`{"content":"c","edit_code":"editcode1234","private":true,"privacy":"public"}`)
	s.Equal(http.StatusBadRequest, w.Code)
	s.Contains(w.Body.String(), conflictMessage)
}
