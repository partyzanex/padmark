package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/google/uuid"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/adapters/web/ogenapi"
	"github.com/partyzanex/padmark/internal/domain"
)

// showContentsMatcher matches a *domain.Note whose ShowContents equals want (nil when want is nil).
//
//nolint:ireturn // gomock matchers are interfaces by design
func showContentsMatcher(want *bool) gomock.Matcher {
	return gomock.Cond(func(x any) bool {
		note, ok := x.(*domain.Note)
		if !ok {
			return false
		}

		if want == nil || note.ShowContents == nil {
			return want == nil && note.ShowContents == nil
		}

		return *want == *note.ShowContents
	})
}

// legacyNoteFields are the response fields that clients written before show_contents read.
func legacyNoteFields() []string {
	return []string{
		"id", "title", "content", "content_type", "views", "burn_after_reading",
		"created_at", "updated_at", "privacy", "private",
	}
}

// TestCreateNote_ShowContents pins the create contract: an old client that omits show_contents
// (also with the v0.1.6 private alias) gets a note without the sidebar.
func (s *HandlerSuite) TestCreateNote_ShowContents() {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"omitted", `{"content":"c"}`, false},
		{"v0.1.6 private alias", `{"content":"c","private":false}`, false},
		{"true", `{"content":"c","show_contents":true}`, true},
		{"false", `{"content":"c","show_contents":false}`, false},
	}

	for _, tcase := range cases {
		s.manager.EXPECT().Create(gomock.Any(), gomock.Cond(func(x any) bool {
			note, ok := x.(*domain.Note)

			return ok && note.ContentsSidebar() == tcase.want
		})).DoAndReturn(func(_ context.Context, note *domain.Note) (*domain.Note, error) {
			created := newTestNote("t", "c")
			created.ShowContents = note.ShowContents

			return created, nil
		})

		w := s.sendJSON(http.MethodPost, "/notes", tcase.body)
		s.Require().Equal(http.StatusCreated, w.Code, tcase.name)

		var created map[string]any

		s.Require().NoError(json.NewDecoder(w.Body).Decode(&created))
		s.Equal(tcase.want, created["show_contents"], tcase.name)

		for _, field := range legacyNoteFields() {
			s.Contains(created, field, "%s: field %s", tcase.name, field)
		}

		s.Contains(created, "edit_code", tcase.name)
	}
}

// TestUpdateNote_ShowContents pins the update contract on the ogen and the native multi-segment
// routes: an omitted show_contents reaches the usecase as nil, so storage keeps the stored value.
func (s *HandlerSuite) TestUpdateNote_ShowContents() {
	cases := []struct {
		want *bool
		name string
		body string
	}{
		{nil, "omitted", `{"content":"c","edit_code":"editcode1234"}`},
		{nil, "v0.1.6 private alias", `{"content":"c","edit_code":"editcode1234","private":false}`},
		{new(true), "true", `{"content":"c","edit_code":"editcode1234","show_contents":true}`},
		{new(false), "false", `{"content":"c","edit_code":"editcode1234","show_contents":false}`},
	}

	for _, id := range []string{testID, "project/GUIDE.md"} {
		for _, tcase := range cases {
			s.expectMutationGate()

			stored := newTestNote("t", "c")
			stored.ShowContents = new(true)
			s.manager.EXPECT().Update(gomock.Any(), id, "editcode1234", uuid.Nil, showContentsMatcher(tcase.want)).
				Return(stored, nil)

			w := s.sendJSON(http.MethodPut, "/notes/"+id, tcase.body)
			s.Require().Equal(http.StatusOK, w.Code, "%s %s", id, tcase.name)

			var updated map[string]any

			s.Require().NoError(json.NewDecoder(w.Body).Decode(&updated))
			s.Equal(true, updated["show_contents"], "%s %s: the response reports the stored value", id, tcase.name)

			for _, field := range legacyNoteFields() {
				s.Contains(updated, field, "%s %s: field %s", id, tcase.name, field)
			}
		}
	}
}

// TestGetNote_ShowContentsField checks the JSON reads: the native content-negotiated route and
// the ogen handler report show_contents, false for a note without the value.
func (s *HandlerSuite) TestGetNote_ShowContentsField() {
	for _, stored := range []*bool{nil, new(false), new(true)} {
		note := newTestNote("t", "c")
		note.ShowContents = stored
		want := stored != nil && *stored

		s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
		r.Header.Set("Accept", "application/json")
		s.router.ServeHTTP(w, r)
		s.Require().Equal(http.StatusOK, w.Code)

		var read map[string]any

		s.Require().NoError(json.NewDecoder(w.Body).Decode(&read))
		s.Equal(want, read["show_contents"])

		for _, field := range legacyNoteFields() {
			s.Contains(read, field, "field %s", field)
		}

		s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
		s.manager.EXPECT().ViewPreloaded(gomock.Any(), testID, note).Return(note, nil)

		handler := web.NewOgenHandler(s.manager, s.linkManager, s.pinger, discardLog())
		result, err := handler.GetNote(s.T().Context(), ogenapi.GetNoteParams{ID: testID})
		s.Require().NoError(err)

		resp, ok := result.(*ogenapi.NoteResponse)
		s.Require().True(ok)
		s.Equal(ogenapi.NewOptBool(want), resp.ShowContents)
	}
}

// TestGetNote_HTML_ContentsSidebar checks that the note view has the sidebar only when the
// option is on, so a note without the option keeps the full-width article.
func (s *HandlerSuite) TestGetNote_HTML_ContentsSidebar() {
	for _, stored := range []*bool{nil, new(false), new(true)} {
		note := newTestNote("toc", "## Part")
		note.ShowContents = stored
		s.manager.EXPECT().GetRendered(gomock.Any(), testID).
			Return(note, `<h2 id="user-content-part">Part</h2>`, nil)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
		r.Header.Set("Accept", "text/html")
		s.router.ServeHTTP(w, r)
		s.Require().Equal(http.StatusOK, w.Code)

		body := w.Body.String()
		if stored != nil && *stored {
			s.Contains(body, `<nav class="note-toc" id="noteToc" aria-label="On this page" hidden>`)
			s.Contains(body, `class="note-layout note-layout--toc"`)
			s.Contains(body, "IntersectionObserver")

			continue
		}

		s.NotContains(body, "note-toc")
		s.NotContains(body, "noteToc")
		s.Contains(body, `<main class="note-layout">`)
	}
}

// TestGetNote_BurnInterstitial_NoContentsSidebar checks that the burn confirmation page never
// shows the sidebar: it does not show the note, so it has no headings.
func (s *HandlerSuite) TestGetNote_BurnInterstitial_NoContentsSidebar() {
	note := newTestNote("secret", "## Part")
	note.BurnAfterReading = true
	note.ShowContents = new(true)

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.revealStore.EXPECT().Issue(gomock.Any(), domain.HashSlug(testID)).Return("tok-abc", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")
	s.newRouterWithReveal().ServeHTTP(w, r)

	s.Require().Equal(http.StatusOK, w.Code)
	body := w.Body.String()
	s.Contains(body, "Burns after reading")
	s.NotContains(body, "note-toc")
	s.NotContains(body, "noteToc")
}

// TestEditPage_ContentsSwitch checks that the editor pre-sets the stored sidebar option.
func (s *HandlerSuite) TestEditPage_ContentsSwitch() {
	for _, stored := range []*bool{nil, new(true)} {
		note := newTestNote("edit me", "# hello")
		note.ShowContents = stored
		s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
		s.manager.EXPECT().EnforceExpiry(gomock.Any(), testID, note).Return(nil)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)
		s.router.ServeHTTP(w, r)
		s.Require().Equal(http.StatusOK, w.Code)

		body := w.Body.String()
		s.Contains(body, "Contents sidebar")

		if stored != nil {
			s.Contains(body, `id="contentsCheck" checked>`)

			continue
		}

		s.Contains(body, `id="contentsCheck">`)
	}
}

// TestIndexPage_ContentsSwitchOff checks that a new note starts with the sidebar off.
func (s *HandlerSuite) TestIndexPage_ContentsSwitchOff() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	s.router.ServeHTTP(w, r)

	s.Require().Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), `id="contentsCheck">`)
	s.Contains(w.Body.String(), "show_contents: document.getElementById('contentsCheck').checked")
}
