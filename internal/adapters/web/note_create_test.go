package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/adapters/web"
	"github.com/partyzanex/padmark/internal/domain"
)

// ── CreateNote ──.

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

// TestCreateNote_ContentOverCap_RejectedBeforeStorage: the OpenAPI maxLength on content is
// enforced by the generated request validation, so an oversized body (well inside the 4 MiB
// request limit) never reaches the manager.
// TestCreateNote_LongContentReachesManager checks that the API has no fixed content limit: the
// notes manager applies the configured one (--max-content-length).
func (s *HandlerSuite) TestCreateNote_LongContentReachesManager() {
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, domain.ErrContentTooLarge)

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handlerCfg.CSRFSecret = testCSRFSecret
	handler := newTestHandler(s.T(), handlerCfg)

	router, err := web.NewRouter(handler, &web.RouterOptions{
		MaxBodyBytes: 4 * 1024 * 1024,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	})
	s.Require().NoError(err)

	// One character more than the limit that the API spec had before.
	body := `{"content":"` + strings.Repeat("a", 1<<20+1) + `"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusUnprocessableEntity, w.Code)
	s.Contains(w.Body.String(), "content")
}

// TestCreateNote_ContentTooLargeFromManager_Is422 pins the domain sentinel's status for the
// routes that reach the usecase check (native handlers, or a body the spec validation let by).
func (s *HandlerSuite) TestCreateNote_ContentTooLargeFromManager_Is422() {
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, domain.ErrContentTooLarge)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"content":"x"}`))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusUnprocessableEntity, w.Code)
	s.Contains(w.Body.String(), domain.ErrContentTooLarge.Error())
}

func (s *HandlerSuite) TestCreateNote_WithTTL() {
	note := newTestNote("burn", "content")
	note.BurnAfterReading = true
	note.BurnTTL = 3600

	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, n *domain.Note) (*domain.Note, error) {
			s.True(n.BurnAfterReading)
			s.Equal(int64(3600), n.BurnTTL)
			s.Nil(n.ExpiresAt) // expiry is set on first read, not at creation.
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

// TestCreateNote_LifetimeWithoutBurn: without burn_after_reading, ttl is the note's lifetime
// counted from creation.
func (s *HandlerSuite) TestCreateNote_LifetimeWithoutBurn() {
	note := newTestNote("expiring", "content")
	before := time.Now().UTC()

	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, n *domain.Note) (*domain.Note, error) {
			s.False(n.BurnAfterReading)
			s.Zero(n.BurnTTL)
			s.Require().NotNil(n.ExpiresAt)
			s.WithinDuration(before.Add(time.Hour), *n.ExpiresAt, 5*time.Second)
			note.ID = n.ID
			note.ExpiresAt = n.ExpiresAt

			return note, nil
		})

	body := `{"title":"expiring","content":"content","ttl":3600}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
}

// TestCreateNote_ZeroTTLWithoutBurn_NoExpiry: ttl 0 without burn keeps the note without expiry.
func (s *HandlerSuite) TestCreateNote_ZeroTTLWithoutBurn_NoExpiry() {
	note := newTestNote("plain", "content")

	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, n *domain.Note) (*domain.Note, error) {
			s.Nil(n.ExpiresAt)
			s.Zero(n.BurnTTL)
			note.ID = n.ID

			return note, nil
		})

	body := `{"title":"plain","content":"content","ttl":0}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
}

func (s *HandlerSuite) TestCreateNote_ImmediateBurn() {
	note := newTestNote("burn", "content")
	note.BurnAfterReading = true
	note.BurnTTL = 0

	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, n *domain.Note) (*domain.Note, error) {
			s.True(n.BurnAfterReading)
			s.Equal(int64(0), n.BurnTTL)
			s.Nil(n.ExpiresAt)
			note.ID = n.ID

			return note, nil
		})

	body := `{"title":"burn","content":"content","burn_after_reading":true,"ttl":0}`
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
	note := newTestNote("", "body")
	s.manager.EXPECT().Create(gomock.Any(), gomock.Any()).Return(note, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(`{"content":"body"}`))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusCreated, w.Code)
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
	// ogen validates the enum at schema level → 400.
	body := `{"title":"t","content":"c","content_type":"application/pdf"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	s.router.ServeHTTP(w, r)

	s.Equal(http.StatusBadRequest, w.Code)
}

func (s *HandlerSuite) TestCreateNote_BodyExceedsLimit_Returns413() {
	// The 1 MiB domain content cap is gone; the only size limit is now the configurable
	// MaxBodyBytes, enforced at the transport layer (http.MaxBytesReader). A body over that limit
	// must surface as a clean 413, not an opaque decode error — checked here with a tiny limit.
	// Manager.Create is never reached (no EXPECT): the body is rejected during ogen decoding.
	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handlerCfg.CSRFSecret = testCSRFSecret
	handler := newTestHandler(s.T(), handlerCfg)
	opts := web.RouterOptions{
		MaxBodyBytes: 64,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	}
	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	body := `{"title":"t","content":"` + strings.Repeat("x", 512) + `"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusRequestEntityTooLarge, w.Code)
}

// ── ogen decode errors (pre-handler failures) ──.

// decodeErrorBody posts body to /notes with the given Content-Type and returns the status and
// the decoded error envelope. It reads into a map rather than a typed struct so an absent
// "message" is visible as an absent key instead of an empty string, which is the exact defect
// these tests guard: ogen's DefaultErrorHandler wrote "error_message" and left "message" unset.
func (s *HandlerSuite) decodeErrorBody(contentType, body string) (int, map[string]any) {
	s.T().Helper()

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)

	s.router.ServeHTTP(w, r)

	var decoded map[string]any
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&decoded))

	return w.Code, decoded
}

// TestOgenDecodeErrors_UseErrorResponseShape covers every failure ogen raises before a request
// reaches a handler method. Two things are pinned per case: the body uses the contract's
// ErrorResponse shape (ogen's native {"error_message": ...} declares none of the required
// "message" field), and the message itself is the sanitised one — ogen's raw string chains its
// own decode path and names generated request types, which tells a caller nothing it can act on.
// Manager.Create is never reached in any case (no EXPECT): each request dies during decoding.
func (s *HandlerSuite) TestOgenDecodeErrors_UseErrorResponseShape() {
	cases := map[string]struct {
		contentType string
		body        string
		wantStatus  int
		wantMessage string
	}{
		"malformed json": {
			"application/json", `{"content":`, http.StatusBadRequest,
			"invalid request body",
		},
		// The one case carrying detail worth forwarding: the field name is schema-defined, so
		// naming it helps the caller without exposing anything the spec doesn't already publish.
		"missing required field": {
			"application/json", `{}`, http.StatusBadRequest,
			"invalid request body: content (field required)",
		},
		"wrong field type": {
			"application/json", `{"content": 12345}`, http.StatusBadRequest,
			"invalid request body",
		},
		"unsupported media type": {
			"text/plain", `hello`, http.StatusUnsupportedMediaType,
			"unsupported content type: text/plain",
		},
	}

	for name, want := range cases {
		s.Run(name, func() {
			status, decoded := s.decodeErrorBody(want.contentType, want.body)

			s.Equal(want.wantStatus, status)
			s.Contains(decoded, "message", "ErrorResponse requires a message field")
			s.NotContains(decoded, "error_message", "ogen's native error shape must not leak through")
			s.Equal(want.wantMessage, decoded["message"])
		})
	}
}

// TestOgenDecodeErrors_DoNotLeakInternals is the same surface checked from the other side: no
// reply may name a generated type, an operation ID, or ogen's decode chain, whatever the input.
// Stated separately from the exact-message assertions above so that rewording a message later
// cannot quietly drop this guarantee along with it.
func (s *HandlerSuite) TestOgenDecodeErrors_DoNotLeakInternals() {
	bodies := []string{`{"content":`, `{}`, `{"content": 12345}`, `[]`, `{"content": [1,2]}`}

	for _, body := range bodies {
		_, decoded := s.decodeErrorBody("application/json", body)

		message, _ := decoded["message"].(string)
		s.NotEmpty(message)

		for _, leak := range []string{"CreateNoteRequest", "operation CreateNote", "decode request", "callback"} {
			s.NotContains(message, leak, "internal detail %q leaked for body %s", leak, body)
		}
	}
}

// TestOgenDecodeErrors_LongContentTypeIsTruncated bounds what a caller can make the server echo.
// The Content-Type header is limited only by MaxHeaderBytes, so quoting it back whole would let
// a caller inflate the error response far beyond the request that caused it.
func (s *HandlerSuite) TestOgenDecodeErrors_LongContentTypeIsTruncated() {
	longType := "text/" + strings.Repeat("z", 4096)

	_, decoded := s.decodeErrorBody(longType, `{}`)

	message, _ := decoded["message"].(string)
	s.Less(len(message), 200, "the echoed content type must be capped, got %d bytes", len(message))
	s.Contains(message, "unsupported content type")
}

// TestCreateNote_BodyExceedsLimit_UsesErrorResponseShape is the 413 counterpart: that branch
// already wrote the right shape before the handler was replaced, so this pins it against a
// regression while the surrounding code changes.
func (s *HandlerSuite) TestCreateNote_BodyExceedsLimit_UsesErrorResponseShape() {
	handlerCfg := testHandlerConfig(s.manager, s.linkManager, s.pinger)
	handlerCfg.CSRFSecret = testCSRFSecret
	handler := newTestHandler(s.T(), handlerCfg)
	opts := web.RouterOptions{
		MaxBodyBytes: 64,

		AllowedHosts: nil, RateLimit: 0, RateBurst: 0, DisableAPI: false,
	}
	router, err := web.NewRouter(handler, &opts)
	s.Require().NoError(err)

	body := `{"title":"t","content":"` + strings.Repeat("x", 512) + `"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/notes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(w, r)

	s.Equal(http.StatusRequestEntityTooLarge, w.Code)

	var decoded map[string]any
	s.Require().NoError(json.NewDecoder(w.Body).Decode(&decoded))
	s.Contains(decoded, "message")
	s.NotContains(decoded, "error_message")
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
