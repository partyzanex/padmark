package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	adhttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/domain"
)

// testNoteResponse mirrors noteResponse for decoding test responses.
type testNoteResponse struct {
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ExpiresAt        *time.Time `json:"expires_at"`
	ID               string     `json:"id"`
	Title            string     `json:"title"`
	Content          string     `json:"content"`
	ContentType      string     `json:"content_type"`
	EditCode         string     `json:"edit_code"`
	Views            int        `json:"views"`
	BurnAfterReading bool       `json:"burn_after_reading"`
}

type HandlerSuite struct {
	suite.Suite

	ctrl    *gomock.Controller
	manager *MockNoteManager
	pinger  *MockPinger
	router  http.Handler
}

func TestHandler(t *testing.T) {
	suite.Run(t, new(HandlerSuite))
}

var discardLog = slog.New(slog.DiscardHandler) //nolint:gochecknoglobals // test helper

func (s *HandlerSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.manager = NewMockNoteManager(s.ctrl)
	s.pinger = NewMockPinger(s.ctrl)

	s.router = s.newRouter(nil)
}

func (s *HandlerSuite) newRouter(tokens []string) http.Handler {
	handler := adhttp.NewHandler(s.manager, discardLog).WithPinger(s.pinger)
	ogen := adhttp.NewOgenHandler(s.manager, s.pinger, discardLog)

	opts := adhttp.RouterOptions{CookieMaxAge: 90 * 24 * 60 * 60, MaxBodyBytes: 256 * 1024}

	return adhttp.NewRouter(handler, ogen, tokens, opts)
}

func (s *HandlerSuite) TearDownTest() {
	s.ctrl.Finish()
}

const testID = "abc-123"

func newTestNote(title, content string) *domain.Note {
	return &domain.Note{
		ID:          testID,
		Title:       title,
		Content:     content,
		ContentType: domain.ContentTypeMarkdown,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
}

// ── IndexPage ──

func (s *HandlerSuite) TestIndexPage() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")
	s.Contains(w.Body.String(), "Padmark")
	s.Contains(w.Body.String(), "Publish")
}

// ── EditPage ──

func (s *HandlerSuite) TestEditPage_OK() {
	note := newTestNote("edit me", "# hello")
	note.BurnAfterReading = true
	note.Private = true

	future := time.Now().Add(time.Hour)
	note.ExpiresAt = &future

	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")

	body := w.Body.String()
	s.Contains(body, "edit me")
	s.Contains(body, "# hello")
	s.Contains(body, "Save")
	s.Contains(body, "checked")
	s.Contains(body, `id="privateCheck" checked`)
}

func (s *HandlerSuite) TestEditPage_NotFound() {
	s.manager.EXPECT().Peek(gomock.Any(), "missing").Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/edit/missing", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Body.String(), "not found")
}

// ── SuccessPage ──

func (s *HandlerSuite) TestSuccessPage_OK() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")
	s.Contains(w.Body.String(), "Paste created")
	s.Contains(w.Body.String(), "http://example.com/abc")
	s.Contains(w.Body.String(), "never expires")
}

func (s *HandlerSuite) TestSuccessPage_NoID() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.Equal("/", w.Header().Get("Location"))
}

func (s *HandlerSuite) TestSuccessPage_WithBurnAndExpires() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/success?id=abc&burn=1&expires=2026-06-15T12:00:00Z", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	body := w.Body.String()
	s.Contains(body, "Burn after reading")
	s.Contains(body, "expires Jun 15, 2026")
	// edit code is stored in sessionStorage before redirect and read by client-side JS;
	// it is never rendered into the HTML by the server.
	s.Contains(body, "editCodeBlock")
}

func (s *HandlerSuite) TestSuccessPage_HTTPS() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc", nil)
	r.Header.Set("X-Forwarded-Proto", "https")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "https://")
}

func (s *HandlerSuite) TestSuccessPage_InvalidExpires() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc&expires=bad-date", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "never expires")
}

// ── CreateNote ──

func (s *HandlerSuite) TestCreateNote_OK() {
	note := newTestNote("hello", "# world")
	note.EditCode = "editcode1234"
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(note, nil)

	body := `{"title":"hello","content":"# world"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
	s.Contains(w.Header().Get("Location"), "/")
	s.Contains(w.Header().Get("Content-Type"), "application/json")

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal("hello", resp.Title)
	s.Equal("# world", resp.Content)
	s.NotEmpty(resp.ID)
	s.Equal("editcode1234", resp.EditCode)
}

func (s *HandlerSuite) TestCreateNote_WithTTL() {
	note := newTestNote("burn", "content")
	note.BurnAfterReading = true
	note.BurnTTL = 3600

	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, n *domain.Note) (*domain.Note, error) {
			s.True(n.BurnAfterReading)
			s.Equal(int64(3600), n.BurnTTL)
			s.Nil(n.ExpiresAt) // expiry is set on first read, not at creation
			note.ID = n.ID

			return note, nil
		})

	body := `{"title":"burn","content":"content","burn_after_reading":true,"ttl":3600}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.True(resp.BurnAfterReading)
}

func (s *HandlerSuite) TestCreateNote_EmptyTitle() {
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, domain.ErrTitleRequired)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"title":"","content":"body"}`))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusUnprocessableEntity, w.Code)
}

func (s *HandlerSuite) TestCreateNote_InvalidBody() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`not json`))

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *HandlerSuite) TestCreateNote_WithSlug() {
	note := newTestNote("slugged", "body")
	note.ID = "my-note"
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(note, nil)

	body := `{"title":"slugged","content":"body","slug":"my-note"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
	s.Equal("/my-note", w.Header().Get("Location"))

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal("my-note", resp.ID)
}

func (s *HandlerSuite) TestCreateNote_SlugConflict() {
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, domain.ErrSlugConflict)

	body := `{"title":"first","content":"body","slug":"taken"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusConflict, w.Code)
}

func (s *HandlerSuite) TestCreateNote_InvalidContentType() {
	// ogen validates the enum at schema level → 400
	body := `{"title":"t","content":"c","content_type":"application/pdf"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *HandlerSuite) TestCreateNote_ContentTooLong() {
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, domain.ErrContentTooLong)

	body := `{"title":"t","content":"c"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusRequestEntityTooLarge, w.Code)
}

func (s *HandlerSuite) TestCreateNote_InvalidSlug() {
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, domain.ErrInvalidSlug)

	body := `{"title":"t","content":"c","slug":"bad slug!"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusUnprocessableEntity, w.Code)
}

func (s *HandlerSuite) TestCreateNote_InternalError() {
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, errors.New("db crashed"))

	body := `{"title":"t","content":"c"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
}

// ── GetNote ──

func (s *HandlerSuite) TestGetNote_JSON() {
	note := newTestNote("my title", "**bold**")
	note.Views = 3
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal("my title", resp.Title)
	s.Equal("**bold**", resp.Content)
	s.Equal(3, resp.Views)
	s.Empty(resp.EditCode, "edit_code must not be exposed in GET")
}

func (s *HandlerSuite) TestGetNote_HTML() {
	note := newTestNote("HTML note", "# Heading\n\n**bold**")
	s.manager.EXPECT().GetRendered(gomock.Any(), testID).
		Return(note, "<h1>Heading</h1>\n<strong>bold</strong>", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")

	body := w.Body.String()
	s.Contains(body, "HTML note")
	s.Contains(body, "<h1>Heading</h1>")
	s.Contains(body, "<strong>bold</strong>")
}

func (s *HandlerSuite) TestGetNote_HTML_WithExpiry() {
	note := newTestNote("expiring", "body")

	future := time.Now().Add(24 * time.Hour)
	note.ExpiresAt = &future

	s.manager.EXPECT().GetRendered(gomock.Any(), testID).
		Return(note, "<p>body</p>", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "Expires")
}

func (s *HandlerSuite) TestGetNote_HTML_NotFound() {
	s.manager.EXPECT().GetRendered(gomock.Any(), "missing").Return(nil, "", domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/missing", nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")
	s.Contains(w.Body.String(), "not found")
}

func (s *HandlerSuite) TestGetNote_HTML_Expired() {
	s.manager.EXPECT().GetRendered(gomock.Any(), testID).Return(nil, "", domain.ErrExpired)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusGone, w.Code)
	s.Contains(w.Body.String(), "expired")
}

func (s *HandlerSuite) TestGetNote_HTML_Forbidden() {
	s.manager.EXPECT().GetRendered(gomock.Any(), testID).Return(nil, "", domain.ErrForbidden)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
	s.Contains(w.Body.String(), "Forbidden")
}

func (s *HandlerSuite) TestGetNote_HTML_InternalError() {
	s.manager.EXPECT().GetRendered(gomock.Any(), testID).Return(nil, "", errors.New("boom"))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
	s.Contains(w.Body.String(), "Internal server error")
}

func (s *HandlerSuite) TestGetNote_Plain() {
	note := newTestNote("plain note", "raw content here")
	note.ContentType = domain.ContentTypePlain
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/plain")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/plain")
	s.Equal("raw content here", w.Body.String())
}

func (s *HandlerSuite) TestGetNote_Plain_NotFound() {
	s.manager.EXPECT().View(gomock.Any(), testID).Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/plain")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestGetNote_Markdown() {
	note := newTestNote("md note", "raw **md**")
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/markdown")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/markdown")
	s.Equal("raw **md**", w.Body.String())
}

func (s *HandlerSuite) TestGetNote_RawQuery() {
	note := newTestNote("raw note", "raw content")
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID+"?raw=1", nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/markdown")
	s.Equal("raw content", w.Body.String())
}

func (s *HandlerSuite) TestGetNote_UnknownAccept() {
	note := newTestNote("unknown", "body")
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "image/png")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")
}

func (s *HandlerSuite) TestGetNote_AcceptWildcard() {
	note := newTestNote("wildcard", "body")
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "*/*")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")
}

func (s *HandlerSuite) TestGetNote_NotFound() {
	s.manager.EXPECT().View(gomock.Any(), "nonexistent").Return(nil, domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/nonexistent", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestGetNote_Expired() {
	s.manager.EXPECT().View(gomock.Any(), testID).Return(nil, domain.ErrExpired)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusGone, w.Code)
}

func (s *HandlerSuite) TestGetNote_ShortURL() {
	note := newTestNote("short", "body")
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal("short", resp.Title)
}

func (s *HandlerSuite) TestGetNote_Private_HTML_Unauthorized() {
	note := newTestNote("secret", "private content")
	note.Private = true
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)

	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.Equal("/login", w.Header().Get("Location"))
}

func (s *HandlerSuite) TestGetNote_Private_JSON_Unauthorized() {
	note := newTestNote("secret", "private content")
	note.Private = true
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)

	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

func (s *HandlerSuite) TestGetNote_Private_Authorized() {
	note := newTestNote("secret", "private content")
	note.Private = true
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")
	r.Header.Set("Authorization", "Bearer secret-token")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestGetNote_Private_NotFound() {
	s.manager.EXPECT().Peek(gomock.Any(), "missing").Return(nil, domain.ErrNotFound)

	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/missing", nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

// ── UpdateNote ──

func (s *HandlerSuite) TestUpdateNote_OK() {
	createdAt := time.Now().Add(-time.Hour)
	updated := &domain.Note{
		ID:          testID,
		Title:       "new title",
		Content:     "new content",
		ContentType: domain.ContentTypePlain,
		CreatedAt:   createdAt,
		UpdatedAt:   time.Now(),
	}
	s.manager.EXPECT().Update(gomock.Any(), testID, "editcode1234", gomock.Any()).Return(updated, nil)

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

	s.manager.EXPECT().Update(gomock.Any(), testID, "code", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, _ string, n *domain.Note) (*domain.Note, error) {
			s.True(n.BurnAfterReading)
			s.Equal(int64(3600), n.BurnTTL)
			s.Nil(n.ExpiresAt) // expiry is set on first read, not at update time

			return updated, nil
		})

	body := `{"title":"updated","content":"body","edit_code":"code","burn_after_reading":true,"ttl":3600}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_WithPrivate() {
	updated := newTestNote("updated", "body")

	s.manager.EXPECT().Update(gomock.Any(), testID, "code", gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, _ string, n *domain.Note) (*domain.Note, error) {
			s.True(n.Private)

			return updated, nil
		})

	body := `{"title":"updated","content":"body","edit_code":"code","private":true}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_NotFound() {
	s.manager.EXPECT().Update(gomock.Any(), "missing", "", gomock.Any()).Return(nil, domain.ErrNotFound)

	body := `{"title":"title","content":"content","edit_code":""}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/missing", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_Forbidden() {
	s.manager.EXPECT().Update(gomock.Any(), testID, "wrong", gomock.Any()).Return(nil, domain.ErrForbidden)

	body := `{"title":"title","content":"content","edit_code":"wrong"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_InvalidBody() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(`bad`))

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusBadRequest, w.Code)
}

// ── DeleteNote ──

func (s *HandlerSuite) TestDeleteNote_OK() {
	s.manager.EXPECT().Delete(gomock.Any(), testID, "editcode1234").Return(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("X-Edit-Code", "editcode1234")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNoContent, w.Code)
}

func (s *HandlerSuite) TestDeleteNote_QueryParam() {
	s.manager.EXPECT().Delete(gomock.Any(), testID, "fromquery").Return(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID+"?edit_code=fromquery", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNoContent, w.Code)
}

func (s *HandlerSuite) TestDeleteNote_NotFound() {
	s.manager.EXPECT().Delete(gomock.Any(), "nonexistent", "code").Return(domain.ErrNotFound)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/nonexistent", nil)
	r.Header.Set("X-Edit-Code", "code")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestDeleteNote_Forbidden() {
	s.manager.EXPECT().Delete(gomock.Any(), testID, "wrong").Return(domain.ErrForbidden)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("X-Edit-Code", "wrong")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusForbidden, w.Code)
}

// ── Health ──

func (s *HandlerSuite) TestHealthz() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestReadyz_OK() {
	s.pinger.EXPECT().PingContext(gomock.Any()).Return(nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestReadyz_NoPinger() {
	handler := adhttp.NewHandler(s.manager, discardLog)
	ogen := adhttp.NewOgenHandler(s.manager, nil, discardLog)
	opts := adhttp.RouterOptions{CookieMaxAge: 90 * 24 * 60 * 60, MaxBodyBytes: 256 * 1024}
	router := adhttp.NewRouter(handler, ogen, nil, opts)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestReadyz_Error() {
	s.pinger.EXPECT().PingContext(gomock.Any()).Return(errors.New("db down"))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusServiceUnavailable, w.Code)
}

// ── Middleware ──

func (s *HandlerSuite) TestRequestIDHeader() {
	s.manager.EXPECT().View(gomock.Any(), testID).Return(newTestNote("title", "content"), nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)

	s.router.ServeHTTP(w, r)

	s.NotEmpty(w.Header().Get("X-Request-ID"))
}

func (s *HandlerSuite) TestStaticAssets() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/css")
}

// ── Broken writer tests (cover error-log branches) ──

type failWriter struct {
	header http.Header
	code   int
}

func newFailWriter() *failWriter                 { return &failWriter{header: http.Header{}} }
func (fw *failWriter) Header() http.Header       { return fw.header }
func (fw *failWriter) WriteHeader(code int)      { fw.code = code }
func (fw *failWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func (s *HandlerSuite) TestIndexPage_WriteFail() {
	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler))
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	handler.IndexPage(fw, r)

	s.Equal(0, fw.code)
}

func (s *HandlerSuite) TestEditPage_WriteFail() {
	note := newTestNote("edit", "body")
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)

	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler))
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)
	r.SetPathValue("id", testID)

	handler.EditPage(fw, r)

	s.NotNil(fw)
}

func (s *HandlerSuite) TestSuccessPage_WriteFail() {
	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler))
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc", nil)

	handler.SuccessPage(fw, r)

	s.NotNil(fw)
}

func (s *HandlerSuite) TestGetNote_HTML_WriteFail() {
	note := newTestNote("note", "body")
	s.manager.EXPECT().GetRendered(gomock.Any(), testID).Return(note, "<p>body</p>", nil)

	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler))
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.SetPathValue("id", testID)
	r.Header.Set("Accept", "text/html")

	handler.GetNote(fw, r)

	s.NotNil(fw)
}

func (s *HandlerSuite) TestGetNote_Plain_WriteFail() {
	note := newTestNote("note", "body")
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler))
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.SetPathValue("id", testID)
	r.Header.Set("Accept", "text/plain")

	handler.GetNote(fw, r)

	s.NotNil(fw)
}

func (s *HandlerSuite) TestGetNote_JSON_WriteFail() {
	note := newTestNote("note", "body")
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler))
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.SetPathValue("id", testID)
	r.Header.Set("Accept", "application/json")

	handler.GetNote(fw, r)

	s.NotNil(fw)
}

// ── Rate limit ──

func (s *HandlerSuite) TestRateLimit_Exceeded() {
	handler := adhttp.NewHandler(s.manager, discardLog).WithPinger(s.pinger)
	ogen := adhttp.NewOgenHandler(s.manager, s.pinger, discardLog)
	opts := adhttp.RouterOptions{
		CookieMaxAge: 90 * 24 * 60 * 60,
		MaxBodyBytes: 256 * 1024,
		RateLimit:    1,
		RateBurst:    1,
	}
	router := adhttp.NewRouter(handler, ogen, nil, opts)

	send := func() int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "203.0.113.1:5000"
		router.ServeHTTP(w, r)

		return w.Code
	}

	s.Equal(http.StatusOK, send())
	s.Equal(http.StatusTooManyRequests, send())
}

func (s *HandlerSuite) TestRateLimit_DifferentIPs() {
	handler := adhttp.NewHandler(s.manager, discardLog).WithPinger(s.pinger)
	ogen := adhttp.NewOgenHandler(s.manager, s.pinger, discardLog)
	opts := adhttp.RouterOptions{
		CookieMaxAge: 90 * 24 * 60 * 60,
		MaxBodyBytes: 256 * 1024,
		RateLimit:    1,
		RateBurst:    1,
	}
	router := adhttp.NewRouter(handler, ogen, nil, opts)

	send := func(ip string) int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = ip + ":5000"
		router.ServeHTTP(w, r)

		return w.Code
	}

	s.Equal(http.StatusOK, send("203.0.113.1"))
	s.Equal(http.StatusOK, send("203.0.113.2"))
}

// ── Fail lockout ──

func (s *HandlerSuite) TestFailLockout_Lockout() {
	s.manager.EXPECT().
		Update(gomock.Any(), testID, "wrong", gomock.Any()).
		Return(nil, domain.ErrForbidden).
		Times(10)

	body := `{"title":"t","content":"c","edit_code":"wrong"}`

	for range 10 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		s.router.ServeHTTP(w, r)
		s.Equal(http.StatusForbidden, w.Code)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, r)
	s.Equal(http.StatusTooManyRequests, w.Code)
}

// ── writeError default branch ──

func (s *HandlerSuite) TestGetNote_Plain_Expired() {
	s.manager.EXPECT().View(gomock.Any(), testID).Return(nil, domain.ErrExpired)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/plain")
	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusGone, w.Code)
}

func (s *HandlerSuite) TestGetNote_Plain_InternalError() {
	s.manager.EXPECT().View(gomock.Any(), testID).Return(nil, errors.New("unexpected db error"))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/plain")
	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
}

func (s *HandlerSuite) TestWriteErrorPage_WriteFail() {
	s.manager.EXPECT().GetRendered(gomock.Any(), testID).Return(nil, "", domain.ErrNotFound)

	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler))
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.SetPathValue("id", testID)
	r.Header.Set("Accept", "text/html")

	handler.GetNote(fw, r)

	s.Equal(http.StatusNotFound, fw.code)
}

func (s *HandlerSuite) TestRecovery_Panic() {
	s.manager.EXPECT().View(gomock.Any(), "panic-id").DoAndReturn(
		func(_ context.Context, _ string) (*domain.Note, error) {
			panic("test panic")
		},
	)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/panic-id", nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusInternalServerError, w.Code)
}

// ── Auth middleware ──

func (s *HandlerSuite) TestAuth_NoTokensConfigured() {
	note := newTestNote("t", "c")
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"title":"t","content":"c"}`))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
}

func (s *HandlerSuite) TestAuth_BearerToken() {
	note := newTestNote("t", "c")
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(note, nil)

	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"title":"t","content":"c"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer secret-token")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
}

func (s *HandlerSuite) TestAuth_CookieToken() {
	note := newTestNote("t", "c")
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")
	r.AddCookie(&http.Cookie{Name: "padmark_token", Value: "secret-token"})

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestAuth_MissingToken_API() {
	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"title":"t","content":"c"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

func (s *HandlerSuite) TestAuth_MissingToken_Browser() {
	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept", "text/html")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.Equal("/login", w.Header().Get("Location"))
}

func (s *HandlerSuite) TestAuth_InvalidToken() {
	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Accept", "text/html")
	r.AddCookie(&http.Cookie{Name: "padmark_token", Value: "wrong"})

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
}

func (s *HandlerSuite) TestAuth_PublicPaths() {
	router := s.newRouter([]string{"secret-token"})

	for _, path := range []string{"/login", "/static/style.css", "/healthz"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)

		router.ServeHTTP(w, r)

		s.NotEqual(http.StatusUnauthorized, w.Code, "path %s should be public", path)
		s.NotEqual(http.StatusSeeOther, w.Code, "path %s should not redirect", path)
	}
}

func (s *HandlerSuite) TestAuth_PublicNoteView() {
	note := newTestNote("public", "body")
	s.manager.EXPECT().Peek(gomock.Any(), testID).Return(note, nil)
	s.manager.EXPECT().View(gomock.Any(), testID).Return(note, nil)

	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestAuth_CreateRequiresAuth() {
	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"title":"t","content":"c"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

func (s *HandlerSuite) TestAuth_UpdateRequiresAuth() {
	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	body := `{"title":"t","content":"c","edit_code":"x"}`
	r := httptest.NewRequest(http.MethodPut, "/notes/"+testID, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

func (s *HandlerSuite) TestAuth_DeleteRequiresAuth() {
	router := s.newRouter([]string{"secret-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+testID, nil)
	r.Header.Set("Accept", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnauthorized, w.Code)
}

func (s *HandlerSuite) TestGetNote_Public_NoEditButtons() {
	note := newTestNote("public note", "content")
	s.manager.EXPECT().GetRendered(gomock.Any(), testID).Return(note, "<p>content</p>", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	body := w.Body.String()
	s.NotContains(body, `href="/edit/`)
	// "New" link should only appear when CanEdit is true; "Raw" is always present.
	// The paste-footer contains Raw for public notes, but not Edit/New.
	pasteFooterStart := strings.Index(body, `class="paste-footer"`)
	s.Require().GreaterOrEqual(pasteFooterStart, 0)
	pasteFooterEnd := strings.Index(body[pasteFooterStart:], "</div>")
	footer := body[pasteFooterStart : pasteFooterStart+pasteFooterEnd]
	s.NotContains(footer, "Edit")
	s.NotContains(footer, "New")
}

// ── Login ──

func (s *HandlerSuite) TestLoginPage() {
	router := s.newRouter([]string{"token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/login", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "Authentication required")
}

func (s *HandlerSuite) TestLoginPage_Error() {
	router := s.newRouter([]string{"token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/login?error=1", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Body.String(), "Invalid token")
}

func (s *HandlerSuite) TestLogin_OK() {
	router := s.newRouter([]string{"valid-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader("token=valid-token"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.Equal("/", w.Header().Get("Location"))

	cookies := w.Result().Cookies()
	s.Require().NotEmpty(cookies)
	s.Equal("padmark_token", cookies[0].Name)
	s.Equal("valid-token", cookies[0].Value)
	s.True(cookies[0].HttpOnly)
}

func (s *HandlerSuite) TestLogin_InvalidToken() {
	router := s.newRouter([]string{"valid-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader("token=wrong"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.Contains(w.Header().Get("Location"), "/login?error=1")
}

func (s *HandlerSuite) TestLogin_EmptyToken() {
	router := s.newRouter([]string{"valid-token"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login",
		strings.NewReader("token="))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusSeeOther, w.Code)
	s.Contains(w.Header().Get("Location"), "/login?error=1")
}

func (s *HandlerSuite) TestLoginPage_WriteFail() {
	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler))
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/login?error=1", nil)

	handler.LoginPage(fw, r)

	s.NotNil(fw)
}

// ── API docs ──

func (s *HandlerSuite) TestAPIDocsPage() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")
	s.Contains(w.Body.String(), "openapi.yaml")
	s.Contains(w.Body.String(), "redoc")
}

func (s *HandlerSuite) TestAPISpec() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Equal("application/yaml", w.Header().Get("Content-Type"))
	s.Contains(w.Body.String(), "openapi: 3.1.0")
	s.Contains(w.Body.String(), "Padmark API")
}

func (s *HandlerSuite) TestAPIDocsPage_WriteFail() {
	handler := adhttp.NewHandler(s.manager, slog.New(slog.DiscardHandler))
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/api", nil)

	handler.APIDocsPage(fw, r)

	s.NotNil(fw)
}

func (s *HandlerSuite) TestAPISpec_WriteFail() {
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)

	adhttp.APISpec(fw, r)

	s.NotNil(fw)
}

func (s *HandlerSuite) TestAPIDocsPage_Public() {
	discardLog := slog.New(slog.DiscardHandler)
	handler := adhttp.NewHandler(s.manager, discardLog)
	ogen := adhttp.NewOgenHandler(s.manager, s.pinger, discardLog)
	opts := adhttp.RouterOptions{CookieMaxAge: 90 * 24 * 60 * 60, MaxBodyBytes: 256 * 1024}
	router := adhttp.NewRouter(handler, ogen, []string{"secret"}, opts)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api", nil)

	router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code, "/api should be public")
}
