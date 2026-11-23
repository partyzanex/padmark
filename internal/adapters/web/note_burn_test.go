package web_test

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// ── BurnInterstitial (GET /{id} + revealStore) ──.

func (s *HandlerSuite) TestGetNote_BurnInterstitial_ShowsConfirmPage() {
	note := newTestNote("secret", "burn me")
	note.BurnAfterReading = true

	s.expectBurnHeld(note)
	s.revealStore.EXPECT().Issue(gomock.Any(), domain.HashSlug(testID)).Return("tok-abc", nil)

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	body := w.Body.String()
	s.Contains(body, "tok-abc", "token must appear in the confirmation form")
	s.Contains(body, "Burns after reading")
}

func (s *HandlerSuite) TestGetNote_BurnInterstitial_NoRevealStore_ServesDirectly() {
	note := newTestNote("secret", "burn me")
	note.BurnAfterReading = true

	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, gomock.Any()).
		Return(note, "<p>burn me</p>", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "burn me")
}

func (s *HandlerSuite) TestGetNote_BurnInterstitial_NonBurnNote_SkipsInterstitial() {
	note := newTestNote("plain", "body")

	// The first read has no burn intent; a note without burn is served by it.
	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, withBurn(false)).
		Return(note, "<p>body</p>", nil)

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.NotContains(w.Body.String(), "Burns after reading")
}

func (s *HandlerSuite) TestGetNote_BurnInterstitial_IssueError_Returns500() {
	note := newTestNote("secret", "burn me")
	note.BurnAfterReading = true

	s.expectBurnHeld(note)
	s.revealStore.EXPECT().Issue(gomock.Any(), domain.HashSlug(testID)).Return("", errors.New("storage down"))

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
}

func (s *HandlerSuite) TestGetNote_BurnInterstitial_GracePeriod_SkipsInterstitial() {
	note := newTestNote("secret", "burn me")
	note.BurnAfterReading = true
	note.BurnTTL = 300

	// The read without the burn intent stops at the burn note; the grace-period note then gets
	// the deliberate read, which starts its timer.
	s.expectBurnHeld(note)
	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, withBurn(true)).
		Return(note, "<p>burn me</p>", nil)

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "burn me")
}

// ── HandleReveal (POST /{id}) ──.

func (s *HandlerSuite) TestHandleReveal_OK() {
	note := newTestNote("secret", "burn me")
	note.BurnAfterReading = true

	// Inspect applies privacy before the token; the confirmed read has the burn intent.
	s.manager.EXPECT().Inspect(gomock.Any(), testID, gomock.Any()).Return(note, nil)
	s.revealStore.EXPECT().Consume(gomock.Any(), "tok-abc", domain.HashSlug(testID)).Return(true, nil)
	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, withBurn(true)).
		Return(note, "<p>burn me</p>", nil)

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := s.revealPOST("/notes/"+testID, "token=tok-abc")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "burn me")
}

func (s *HandlerSuite) TestHandleReveal_NoRevealStore_Forbidden() {
	w := httptest.NewRecorder()
	r := s.revealPOST("/notes/"+testID, "token=tok-abc")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

// TestHandleReveal_NoCSRF_Forbidden verifies the burn-reveal POST is now CSRF-guarded like
// every other POST: a request without the CSRF cookie/field is rejected before reaching the
// handler (no Inspect/Consume expectations are set, proving the guard short-circuits first).
func (s *HandlerSuite) TestHandleReveal_NoCSRF_Forbidden() {
	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes/"+testID, strings.NewReader("token=tok-abc"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept", "text/html")
	// no padmark_csrf cookie / csrf_token field.

	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

func (s *HandlerSuite) TestHandleReveal_InvalidToken_Forbidden() {
	note := newTestNote("secret", "burn me")
	s.manager.EXPECT().Inspect(gomock.Any(), testID, gomock.Any()).Return(note, nil)
	s.revealStore.EXPECT().Consume(gomock.Any(), "bad-tok", domain.HashSlug(testID)).Return(false, nil)

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := s.revealPOST("/notes/"+testID, "token=bad-tok")

	router.ServeHTTP(w, r)

	// A wrong or used token keeps the Forbidden page; only a store failure gives the 500 page.
	s.Equal(http.StatusForbidden, w.Code)
	s.Contains(w.Body.String(), "permission to perform this action")
	s.NotContains(w.Body.String(), "burn me")
}

// TestHandleReveal_TokenForWrongNote_Forbidden verifies that posting a token issued
// for a different note returns 403 AND does not burn the token (fix for DoS bug).
// Consume receives the URL's noteID and returns false — the DB rejects the mismatch.
func (s *HandlerSuite) TestHandleReveal_TokenForWrongNote_Forbidden() {
	note := newTestNote("secret", "burn me")
	s.manager.EXPECT().Inspect(gomock.Any(), testID, gomock.Any()).Return(note, nil)
	// Consume is called with testID (URL note); token was issued for another note →
	// DB WHERE note_id = testID finds no row → returns false, token untouched.
	s.revealStore.EXPECT().Consume(gomock.Any(), "tok-other", domain.HashSlug(testID)).Return(false, nil)

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := s.revealPOST("/notes/"+testID, "token=tok-other")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

func (s *HandlerSuite) TestHandleReveal_MissingToken_Forbidden() {
	note := newTestNote("secret", "burn me")
	s.manager.EXPECT().Inspect(gomock.Any(), testID, gomock.Any()).Return(note, nil)
	s.revealStore.EXPECT().Consume(gomock.Any(), "", domain.HashSlug(testID)).Return(false, nil)

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := s.revealPOST("/notes/"+testID, "")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

// TestHandleReveal_CrossNoteToken_TokenNotBurned verifies the fix for the DoS bug:
// posting note-A's token to note-B's endpoint must return 403 WITHOUT burning the token.
// Consume is called with note-B's ID; the DB WHERE note_id = 'wrong-note' finds no row
// and returns false — the token for testID remains intact.
func (s *HandlerSuite) TestHandleReveal_CrossNoteToken_TokenNotBurned() {
	wrongNote := &domain.Note{
		ID:          "wrong-note",
		Title:       "wrong",
		Content:     "body",
		ContentType: new(domain.ContentTypeMarkdown),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),

		ExpiresAt: nil, Privacy: nil, OwnerID: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false, ShowContents: nil,
	}

	s.manager.EXPECT().Inspect(gomock.Any(), "wrong-note", gomock.Any()).Return(wrongNote, nil)
	// Consume called with "wrong-note" as noteID — DB rejects mismatch, token preserved.
	s.revealStore.EXPECT().Consume(gomock.Any(), "tok-for-abc123", domain.HashSlug("wrong-note")).Return(false, nil)

	router := s.newRouterWithReveal()

	w := httptest.NewRecorder()
	r := s.revealPOST("/notes/wrong-note", "token=tok-for-abc123")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
	// gomock TearDown verifies Consume(_, "tok-for-abc123", "wrong-note") was called —
	// confirming the handler passed the URL noteID to Consume, not a blind token burn.
}

// TestHandleReveal_StoreFails_InternalError pins C4 on the web side: a store failure while the
// token is used gives the server error page, not the wrong-token page. The strict mock has no
// ReadRendered expectation, so a read or a burn of the note fails the test. The handler writes
// one error record, and the record does not contain the token.
func (s *HandlerSuite) TestHandleReveal_StoreFails_InternalError() {
	note := newTestNote("secret", "burn me")
	note.BurnAfterReading = true

	s.manager.EXPECT().Inspect(gomock.Any(), testID, gomock.Any()).Return(note, nil)
	s.revealStore.EXPECT().Consume(gomock.Any(), "tok-abc", domain.HashSlug(testID)).
		Return(false, errors.New("consume reveal token: database is locked"))

	logs := new(strings.Builder)
	router := s.newRouterWithRevealLog(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{
		AddSource: false, Level: slog.LevelDebug, ReplaceAttr: nil,
	})))

	w := httptest.NewRecorder()
	r := s.revealPOST("/notes/"+testID, "token=tok-abc")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
	s.Contains(w.Body.String(), "Internal server error")
	s.NotContains(w.Body.String(), "burn me")
	s.NotContains(w.Body.String(), "database is locked", "the page must not show the internal error")
	s.Equal(1, strings.Count(logs.String(), `"level":"ERROR"`), "the failure is logged once at error level")
	s.Contains(logs.String(), "database is locked")
	s.NotContains(logs.String(), "tok-abc", "the log must not contain the token")
}
