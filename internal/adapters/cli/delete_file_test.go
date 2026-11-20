package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/suite"
)

const cmdDeleteFile = "delete-file"

type DeleteFileSuite struct {
	suite.Suite

	status   int
	body     string
	method   string
	editCode string
	server   *httptest.Server
}

func TestDeleteFileSuite(t *testing.T) {
	suite.Run(t, new(DeleteFileSuite))
}

func (s *DeleteFileSuite) SetupTest() {
	isolateEnv(s.T())

	s.status, s.body, s.method, s.editCode = http.StatusNoContent, "", "", ""
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.method, s.editCode = r.Method+" "+r.URL.Path, r.Header.Get("X-Edit-Code")

		if s.status == http.StatusNoContent {
			w.WriteHeader(http.StatusNoContent)

			return
		}

		writeTestResponse(s.T(), w, s.status, s.body)
	}))
}

func (s *DeleteFileSuite) TearDownTest() {
	s.server.Close()
}

func (s *DeleteFileSuite) TestSuccess() {
	out, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdDeleteFile,
		"https://pad.example/files/"+testFileID, "--edit-code", "secret-code")
	s.Require().NoError(err)

	s.Equal("DELETE /files/f1le-id", s.method)
	s.Equal("secret-code", s.editCode)
	s.Equal("deleted f1le-id\n", out)
}

func (s *DeleteFileSuite) TestOwnerWithoutEditCode() {
	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdDeleteFile, testFileID)
	s.Require().NoError(err)
	s.Empty(s.editCode, "no edit code means no header")
}

func (s *DeleteFileSuite) TestNotFound() {
	s.status, s.body = http.StatusNotFound, `{"message":"not found"}`

	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdDeleteFile, testFileID)
	s.Require().ErrorIs(err, ErrFileNotFound)
}

func (s *DeleteFileSuite) TestForbidden() {
	s.status, s.body = http.StatusForbidden, `{"message":"invalid edit code"}`

	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdDeleteFile, testFileID, "-e", "wrong-code")
	s.Require().ErrorIs(err, ErrForbidden)
}

func (s *DeleteFileSuite) TestFilesOff() {
	s.status, s.body = http.StatusServiceUnavailable, `{"message":"feature is not enabled"}`

	_, err := runCLIOutput(s.T().Context(), "--url", s.server.URL, cmdDeleteFile, testFileID)
	s.Require().ErrorIs(err, ErrFilesDisabled)
}
