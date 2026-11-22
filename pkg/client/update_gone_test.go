package client_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/pkg/client"
)

// UpdateGoneSuite checks that the client decodes a 410 answer to an update of an expired note,
// link or file into a typed variant, and that ResponseError gives the status and the message.
// Without the 410 in the contract the decoder returns an unexpected-status error instead.
type UpdateGoneSuite struct {
	suite.Suite

	server *httptest.Server
	cl     *client.Client
	method string
}

func TestUpdateGoneSuite(t *testing.T) {
	suite.Run(t, new(UpdateGoneSuite))
}

func (s *UpdateGoneSuite) SetupTest() {
	s.method = ""
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.method = r.Method + " " + r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"message":"expired"}`))
	}))

	cl, err := client.NewClient(s.server.URL)
	s.Require().NoError(err)

	s.cl = cl
}

func (s *UpdateGoneSuite) TearDownTest() {
	s.server.Close()
}

func (s *UpdateGoneSuite) TestUpdateNote() {
	res, err := s.cl.UpdateNote(s.T().Context(), &client.UpdateNoteRequest{Content: "new"},
		client.UpdateNoteParams{ID: "old-note"})
	s.Require().NoError(err)
	s.IsType(&client.UpdateNoteGone{}, res)
	s.Equal("PUT /notes/old-note", s.method)
	s.expectGone(res)
}

func (s *UpdateGoneSuite) TestUpdateLink() {
	target, err := url.Parse("https://example.com/new")
	s.Require().NoError(err)

	res, err := s.cl.UpdateLink(s.T().Context(), &client.UpdateLinkRequest{TargetURL: *target},
		client.UpdateLinkParams{ID: "old-link"})
	s.Require().NoError(err)
	s.IsType(&client.UpdateLinkGone{}, res)
	s.Equal("PUT /links/old-link", s.method)
	s.expectGone(res)
}

func (s *UpdateGoneSuite) TestUpdateFile() {
	res, err := s.cl.UpdateFile(s.T().Context(), &client.UpdateFileRequest{BurnAfterDownload: client.NewOptBool(false)},
		client.UpdateFileParams{ID: "old-file", XEditCode: client.NewOptString("code")})
	s.Require().NoError(err)
	s.IsType(&client.UpdateFileGone{}, res)
	s.Equal("PUT /files/old-file", s.method)
	s.expectGone(res)
}

// expectGone checks the APIError of the variant res.
func (s *UpdateGoneSuite) expectGone(res any) {
	apiErr, ok := client.ResponseError(res)
	s.Require().True(ok)
	s.Equal(&client.APIError{Message: "expired", Code: "", StatusCode: http.StatusGone}, apiErr)
}
