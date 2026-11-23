package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// ── UpdateNote ──.

func (s *HandlerSuite) TestUpdateNote_OK() {
	createdAt := time.Now().Add(-time.Hour)
	updated := &domain.Note{
		ID:          testID,
		Title:       "new title",
		Content:     "new content",
		ContentType: new(domain.ContentTypePlain),
		CreatedAt:   createdAt,
		UpdatedAt:   time.Now(),

		ExpiresAt: nil, Privacy: nil, OwnerID: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false, ShowContents: nil,
	}
	s.manager.EXPECT().Update(gomock.Any(), testID, "editcode1234", noCaller(), gomock.Any()).Return(updated, nil)

	body := `{"title":"new title","content":"new content","edit_code":"editcode1234"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal("new title", resp.Title)
	s.Equal("new content", resp.Content)
	s.Equal(createdAt.Unix(), resp.CreatedAt.Unix(), "created_at must be preserved")
	s.Equal("text/plain", resp.ContentType, "content_type must be preserved when not provided")
}

func (s *HandlerSuite) TestUpdateNote_WithTTL() {
	updated := newTestNote("updated", "body")

	s.manager.EXPECT().Update(gomock.Any(), testID, "code", noCaller(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, _ domain.Reader, n *domain.Note) (*domain.Note, error) {
			s.True(n.BurnAfterReading)
			s.Equal(int64(3600), n.BurnTTL)
			s.Nil(n.ExpiresAt) // expiry is set on first read, not at update time.

			return updated, nil
		})

	body := `{"title":"updated","content":"body","edit_code":"code","burn_after_reading":true,"ttl":3600}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_ImmediateBurn() {
	updated := newTestNote("updated", "body")

	s.manager.EXPECT().Update(gomock.Any(), testID, "code", noCaller(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, _ domain.Reader, n *domain.Note) (*domain.Note, error) {
			s.True(n.BurnAfterReading)
			s.Equal(int64(0), n.BurnTTL)
			s.Nil(n.ExpiresAt)

			return updated, nil
		})

	body := `{"title":"updated","content":"body","edit_code":"code","burn_after_reading":true,"ttl":0}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_WithPrivacy() {
	// Protected privacy levels need the account system (requirePrivacySupport).
	s.router = s.newRouterWithTOTPAuth()

	updated := newTestNote("updated", "body")

	s.manager.EXPECT().Update(gomock.Any(), testID, "code", noCaller(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, _ domain.Reader, n *domain.Note) (*domain.Note, error) {
			s.Equal(domain.PrivacyAuthenticated, n.EffectivePrivacy())

			return updated, nil
		})

	body := `{"title":"updated","content":"body","edit_code":"code","privacy":"authenticated"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_NotFound() {
	s.manager.EXPECT().Update(gomock.Any(), "missing", "", noCaller(), gomock.Any()).Return(nil, domain.ErrNotFound)

	body := `{"title":"title","content":"content","edit_code":""}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/missing", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_Forbidden() {
	s.manager.EXPECT().Update(gomock.Any(), testID, "wrong", noCaller(), gomock.Any()).
		Return(nil, domain.ErrInvalidEditCode)

	body := `{"title":"title","content":"content","edit_code":"wrong"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

// ── ACCEPTANCE: edit/delete a path-like slug by its URL ──
//
// The web editor saves with PUT /notes/{id} and deletes with DELETE /notes/{id}. For a slug that
// spans path segments (project/GUIDE.md) these become multi-segment paths that ogen's
// single-segment /notes/{id} route cannot match. The native UpdateNoteByPath / DeleteNoteByPath
// handlers bridge PUT/DELETE /notes/{id...} for exactly this case; these tests assert that a
// path-like slug can be edited and deleted by its address (single-segment IDs still go to ogen).

func (s *HandlerSuite) TestUpdateNote_MultiSegmentSlug_ByURL_Acceptance() {
	const mid = "project/GUIDE.md"

	updated := newTestNote("Guide", "new body")
	s.manager.EXPECT().Update(gomock.Any(), mid, "code", noCaller(), gomock.Any()).Return(updated, nil)

	body := `{"title":"Guide","content":"new body","edit_code":"code"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+mid, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code, "editing a path-like slug via PUT /notes/{id...} must succeed")
}

func (s *HandlerSuite) TestDeleteNote_MultiSegmentSlug_ByURL_Acceptance() {
	const mid = "project/GUIDE.md"

	s.manager.EXPECT().Delete(gomock.Any(), mid, "code", noCaller()).Return(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+mid, nil)
	r.Header.Set("X-Edit-Code", "code")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNoContent, w.Code, "deleting a path-like slug via DELETE /notes/{id...} must succeed")
}

// TestUpdateNote_OmittedPrivate_PreservesPrivacy verifies that when "privacy" is absent
// from a PUT request body, the handler passes Privacy=nil to manager.Update so that the
// storage layer can COALESCE(NULL, privacy) and preserve the existing DB value.
func (s *HandlerSuite) TestUpdateNote_OmittedPrivate_PreservesPrivacy() {
	s.manager.EXPECT().Update(gomock.Any(), testID, "code", noCaller(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, _ domain.Reader, n *domain.Note) (*domain.Note, error) {
			// nil means "don't change" — storage will COALESCE(NULL, privacy).
			s.Nil(n.Privacy, "Privacy must be nil when omitted from the request, not defaulted to public")

			return newTestNote("updated", "new content"), nil
		})

	// No "privacy" field in the body.
	body := `{"title":"updated","content":"new content","edit_code":"code"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_InvalidBody() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(`bad`))

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusBadRequest, w.Code)
}

// ── DeleteNote ──.

func (s *HandlerSuite) TestDeleteNote_OK() {
	s.manager.EXPECT().Delete(gomock.Any(), testID, "editcode1234", noCaller()).Return(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("X-Edit-Code", "editcode1234")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNoContent, w.Code)
}

// TestDeleteNote_QueryParamFallback pins the deprecated v0.1.6 form: without X-Edit-Code the
// edit_code query value reaches the manager, and the header wins when both are sent, for
// single-segment and path-like slugs alike.
func (s *HandlerSuite) TestDeleteNote_QueryParamFallback() {
	for _, id := range []string{testID, "project/GUIDE.md"} {
		s.manager.EXPECT().Delete(gomock.Any(), id, "fromquery", noCaller()).Return(nil)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodDelete, "/notes/"+id+"?edit_code=fromquery", nil)

		s.router.ServeHTTP(w, r)

		s.Equal(http.StatusNoContent, w.Code, "query edit_code must be used without the header for %q", id)

		s.manager.EXPECT().Delete(gomock.Any(), id, "fromheader", noCaller()).Return(nil)

		w = httptest.NewRecorder()
		r = httptest.NewRequest(http.MethodDelete, "/notes/"+id+"?edit_code=fromquery", nil)
		r.Header.Set("X-Edit-Code", "fromheader")

		s.router.ServeHTTP(w, r)

		s.Equal(http.StatusNoContent, w.Code, "X-Edit-Code must win over the query for %q", id)
	}
}

func (s *HandlerSuite) TestDeleteNote_NotFound() {
	s.manager.EXPECT().Delete(gomock.Any(), "nonexistent", "code", noCaller()).Return(domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/nonexistent", nil)
	r.Header.Set("X-Edit-Code", "code")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestDeleteNote_Forbidden() {
	s.manager.EXPECT().Delete(gomock.Any(), testID, "wrong", noCaller()).Return(domain.ErrInvalidEditCode)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("X-Edit-Code", "wrong")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

// ── Mutation privacy rule (PUT/DELETE) ──
//
// notes.Manager.Update and Delete implement the restored semantics: a caller may mutate a note
// with an edit code if and only if they could also read it. The usecase returns ErrForbidden; the
// handlers answer 401. The tests below check the translation on each route, and that a 401 does
// not count for the fail lockout.

// TestNoteMutation_AnonymousDelete_PrivateNote verifies the ogen DELETE route: with auth
// configured, an anonymous DELETE that the usecase refuses (ErrForbidden) gets a bodyless 401.
func (s *HandlerSuite) TestNoteMutation_AnonymousDelete_PrivateNote() {
	router := s.newRouterWithTOTPAuth()
	s.manager.EXPECT().Delete(gomock.Any(), testID, "whatever", anonymousReader()).Return(domain.ErrForbidden)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("X-Edit-Code", "whatever")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
	// DELETE /notes/{id} 401 has no schema in the ogen spec → empty DeleteNoteUnauthorized.
	s.Empty(w.Body.String())
}

// TestNoteMutation_AnonymousDelete_OwnerNote is the same branch for privacy=owner: the usecase
// refuses the anonymous caller with ErrForbidden, which the handler answers with 401.
func (s *HandlerSuite) TestNoteMutation_AnonymousDelete_OwnerNote() {
	router := s.newRouterWithTOTPAuth()
	s.manager.EXPECT().Delete(gomock.Any(), testID, "whatever", anonymousReader()).Return(domain.ErrForbidden)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("X-Edit-Code", "whatever")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

// TestNoteMutation_SignedInNonOwner_OwnerNote covers audit finding R4: a signed-in caller who
// cannot read another account's privacy=owner note may not update or delete it, even with the
// right edit code. The usecase gets the reader of the signed-in caller and refuses it; the
// handler answers 401 like the read.
func (s *HandlerSuite) TestNoteMutation_SignedInNonOwner_OwnerNote() {
	usr := newTestUser("bob")

	// The single-segment path is the ogen route; the multi-segment path is the native one.
	for _, path := range []string{"/notes/" + testID, "/notes/project/GUIDE.md"} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			s.Run(method+path, func() {
				router := s.newRouterWithTOTPAuth()
				if method == http.MethodPut {
					s.manager.EXPECT().Update(gomock.Any(), gomock.Any(), "right-code", signedInReader(usr.ID), gomock.Any()).
						Return(nil, domain.ErrForbidden)
				} else {
					s.manager.EXPECT().Delete(gomock.Any(), gomock.Any(), "right-code", signedInReader(usr.ID)).
						Return(domain.ErrForbidden)
				}

				s.authMgr.EXPECT().GetSession(gomock.Any(), "valid-sess").Return(usr, nil)

				body := `{"title":"t","content":"c","privacy":"public","edit_code":"right-code"}`
				r := sessioned(httptest.NewRequest(method, path, strings.NewReader(body)))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Accept", "application/json")
				r.Header.Set("X-Edit-Code", "right-code")

				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)

				s.Equal(http.StatusUnauthorized, w.Code)
			})
		}
	}
}

// TestNoteMutation_401DoesNotTriggerFailLockout verifies the privacy/lockout interplay: the
// fail-lockout counts consecutive 403s (wrong edit codes), while the 401 of a hidden note never
// reaches the counter — repeated refused attempts stay 401 and never escalate to 429.
func (s *HandlerSuite) TestNoteMutation_401DoesNotTriggerFailLockout() {
	router := s.newRouterWithTOTPAuth()
	s.manager.EXPECT().Update(gomock.Any(), testID, "", anonymousReader(), gomock.Any()).
		Return(nil, domain.ErrForbidden).Times(12)

	for range 12 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(`{"title":"t","content":"c"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json")

		router.ServeHTTP(w, r)

		s.Equal(http.StatusUnauthorized, w.Code, "each gated attempt must be 401")
	}
}

// TestNoteMutation_MultiSegmentPaths verifies the native {id...} routes: an anonymous PUT and
// DELETE of a multi-segment-slug note whose privacy refuses anonymous callers get 401.
func (s *HandlerSuite) TestNoteMutation_MultiSegmentPaths() {
	router := s.newRouterWithTOTPAuth()
	s.manager.EXPECT().Update(gomock.Any(), "project/GUIDE.md", "", anonymousReader(), gomock.Any()).
		Return(nil, domain.ErrForbidden)
	s.manager.EXPECT().Delete(gomock.Any(), "project/GUIDE.md", "whatever", anonymousReader()).
		Return(domain.ErrForbidden)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/project/GUIDE.md", strings.NewReader(`{"title":"t","content":"c"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodDelete, "/notes/project/GUIDE.md", nil)
	r.Header.Set("X-Edit-Code", "whatever")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}
