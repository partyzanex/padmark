package http_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"

	adhttp "github.com/partyzanex/padmark/internal/adapters/http"
	"github.com/partyzanex/padmark/internal/infra/render"
	"github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/notes"
)

// testNoteResponse mirrors noteResponse for decoding test responses.
type testNoteResponse struct {
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Content     string    `json:"content"`
	ContentType string    `json:"content_type"`
}

type HandlerSuite struct {
	suite.Suite
	db     *bun.DB
	router http.Handler
}

func TestHandler(t *testing.T) {
	suite.Run(t, new(HandlerSuite))
}

func (s *HandlerSuite) SetupTest() {
	sqldb, err := sql.Open(sqliteshim.DriverName(), ":memory:")
	s.Require().NoError(err)
	sqldb.SetMaxOpenConns(1)

	s.db = bun.NewDB(sqldb, sqlitedialect.New())
	s.Require().NoError(sqlite.Migrate(s.T().Context(), s.db))

	repo := sqlite.NewRepository(s.db)
	renderer := render.NewRenderer()
	manager := notes.NewManager(repo, renderer, slog.New(slog.DiscardHandler))
	handler := adhttp.NewHandler(manager, slog.New(slog.DiscardHandler)).WithPinger(s.db.DB)
	s.router = adhttp.NewRouter(handler)
}

func (s *HandlerSuite) TearDownTest() {
	s.Require().NoError(s.db.Close())
}

// createNote is a test helper that inserts a note and returns its ID.
func (s *HandlerSuite) createNote(title, content string) string {
	body := fmt.Sprintf(`{"title":%q,"content":%q}`, title, content)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)
	s.Require().Equal(http.StatusCreated, w.Code)

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))

	return resp.ID
}

// CreateNote

func (s *HandlerSuite) TestCreateNote_OK() {
	body := `{"title":"hello","content":"# world"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
	s.Contains(w.Header().Get("Location"), "/notes/")
	s.Contains(w.Header().Get("Content-Type"), "application/json")

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal("hello", resp.Title)
	s.Equal("# world", resp.Content)
	s.NotEmpty(resp.ID)
}

func (s *HandlerSuite) TestCreateNote_EmptyTitle() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"content":"body"}`))
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

// GetNote

func (s *HandlerSuite) TestGetNote_JSON() {
	id := s.createNote("my title", "**bold**")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+id, nil)
	r.Header.Set("Accept", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "application/json")

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal("my title", resp.Title)
	s.Equal("**bold**", resp.Content)
}

func (s *HandlerSuite) TestGetNote_HTML() {
	id := s.createNote("HTML note", "# Heading\n\n**bold**")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+id, nil)
	r.Header.Set("Accept", "text/html")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/html")

	body := w.Body.String()
	s.Contains(body, "HTML note")
	s.Contains(body, "<h1")
	s.Contains(body, "<strong>bold</strong>")
}

func (s *HandlerSuite) TestGetNote_Plain() {
	id := s.createNote("plain note", "raw content here")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+id, nil)
	r.Header.Set("Accept", "text/plain")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Contains(w.Header().Get("Content-Type"), "text/plain")
	s.Equal("raw content here", w.Body.String())
}

func (s *HandlerSuite) TestGetNote_Markdown() {
	id := s.createNote("md note", "raw **md**")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+id, nil)
	r.Header.Set("Accept", "text/markdown")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
	s.Equal("raw **md**", w.Body.String())
}

func (s *HandlerSuite) TestGetNote_NotFound() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/nonexistent", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

// UpdateNote

func (s *HandlerSuite) TestUpdateNote_OK() {
	id := s.createNote("old title", "old content")

	body := `{"title":"new title","content":"new content"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+id, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)

	var resp testNoteResponse
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&resp))
	s.Equal("new title", resp.Title)
	s.Equal("new content", resp.Content)
}

func (s *HandlerSuite) TestUpdateNote_NotFound() {
	body := `{"title":"title","content":"content"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/nonexistent", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

func (s *HandlerSuite) TestUpdateNote_InvalidBody() {
	id := s.createNote("title", "content")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/notes/"+id, strings.NewReader(`bad`))

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusBadRequest, w.Code)
}

// DeleteNote

func (s *HandlerSuite) TestDeleteNote_OK() {
	id := s.createNote("to delete", "bye")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/"+id, nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNoContent, w.Code)

	// confirm gone
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/notes/"+id, nil)
	s.router.ServeHTTP(w2, r2)
	s.Equal(http.StatusNotFound, w2.Code)
}

func (s *HandlerSuite) TestDeleteNote_NotFound() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/notes/nonexistent", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusNotFound, w.Code)
}

// Health

func (s *HandlerSuite) TestHealthz() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

func (s *HandlerSuite) TestReadyz() {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusOK, w.Code)
}

// Middleware

func (s *HandlerSuite) TestRequestIDHeader() {
	id := s.createNote("title", "content")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+id, nil)

	s.router.ServeHTTP(w, r)

	s.NotEmpty(w.Header().Get("X-Request-ID"))
}
