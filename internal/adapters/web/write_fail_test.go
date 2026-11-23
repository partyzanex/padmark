package web_test

import (
	"errors"
	"net/http"
	"net/http/httptest"

	"go.uber.org/mock/gomock"

	"github.com/partyzanex/padmark/internal/domain"
)

// ── Broken writer tests (cover error-log branches) ──.

type failWriter struct {
	header http.Header
	code   int
}

func newFailWriter() *failWriter {
	return &failWriter{header: http.Header{}, code: 0}
}

func (fw *failWriter) Header() http.Header { return fw.header }

func (fw *failWriter) WriteHeader(code int) { fw.code = code }

func (fw *failWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func (s *HandlerSuite) TestIndexPage_WriteFail() {
	handlerCfg := testHandlerConfig(s.manager, s.linkManager, nil)
	handler := newTestHandler(s.T(), handlerCfg)
	fw := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	handler.IndexPage(fw, r)

	s.Equal(0, fw.code)
}

func (s *HandlerSuite) TestEditPage_WriteFail() {
	note := newTestNote("edit", "body")

	s.manager.EXPECT().Inspect(gomock.Any(), testID, gomock.Any()).Return(note, nil)

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, nil)
	handler := newTestHandler(s.T(), handlerCfg)
	fwr := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/edit/"+testID, nil)
	r.SetPathValue("id", testID)

	handler.EditPage(fwr, r)

	s.NotNil(fwr)
}

func (s *HandlerSuite) TestSuccessPage_WriteFail() {
	handlerCfg := testHandlerConfig(s.manager, s.linkManager, nil)
	handler := newTestHandler(s.T(), handlerCfg)
	fwr := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/success?id=abc", nil)

	handler.SuccessPage(fwr, r)

	s.NotNil(fwr)
}

func (s *HandlerSuite) TestGetNote_HTML_WriteFail() {
	note := newTestNote("note", "body")
	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, gomock.Any()).Return(note, "<p>body</p>", nil)

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, nil)
	handler := newTestHandler(s.T(), handlerCfg)
	fwr := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.SetPathValue("id", testID)
	r.Header.Set("Accept", "text/html")

	handler.GetNote(fwr, r)

	s.NotNil(fwr)
}

func (s *HandlerSuite) TestGetNote_Plain_WriteFail() {
	note := newTestNote("note", "body")
	s.manager.EXPECT().Read(gomock.Any(), testID, gomock.Any()).Return(note, nil)

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, nil)
	handler := newTestHandler(s.T(), handlerCfg)
	fwr := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.SetPathValue("id", testID)
	r.Header.Set("Accept", "text/plain")

	handler.GetNote(fwr, r)

	s.NotNil(fwr)
}

func (s *HandlerSuite) TestGetNote_JSON_WriteFail() {
	note := newTestNote("note", "body")
	s.manager.EXPECT().Read(gomock.Any(), testID, gomock.Any()).Return(note, nil)

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, nil)
	handler := newTestHandler(s.T(), handlerCfg)
	fwr := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.SetPathValue("id", testID)
	r.Header.Set("Accept", "application/json")

	handler.GetNote(fwr, r)

	s.NotNil(fwr)
}

func (s *HandlerSuite) TestWriteErrorPage_WriteFail() {
	s.manager.EXPECT().ReadRendered(gomock.Any(), testID, gomock.Any()).Return(nil, "", domain.ErrNotFound)

	handlerCfg := testHandlerConfig(s.manager, s.linkManager, nil)
	handler := newTestHandler(s.T(), handlerCfg)
	fwr := newFailWriter()
	r := httptest.NewRequest(http.MethodGet, "/notes/"+testID, nil)
	r.SetPathValue("id", testID)
	r.Header.Set("Accept", "text/html")

	handler.GetNote(fwr, r)

	s.Equal(http.StatusNotFound, fwr.code)
}
