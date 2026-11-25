package cli

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/suite"
)

// UpdateGoneSuite: the server refuses an update of an expired record with 410. Each update
// command reports the "gone" error of its kind of record, not an unexpected response.
type UpdateGoneSuite struct {
	suite.Suite

	server *httptest.Server
	method string
}

func TestUpdateGoneSuite(t *testing.T) {
	suite.Run(t, new(UpdateGoneSuite))
}

func (s *UpdateGoneSuite) SetupTest() {
	isolateEnv(s.T())

	s.method = ""
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.method = r.Method + " " + r.URL.Path
		writeTestResponse(s.T(), w, http.StatusGone, `{"message":"expired"}`)
	}))
}

func (s *UpdateGoneSuite) TearDownTest() {
	s.server.Close()
}

func (s *UpdateGoneSuite) run(args ...string) error {
	_, err := runCLIOutput(s.T().Context(), append([]string{"--url", s.server.URL}, args...)...)

	return err
}

func (s *UpdateGoneSuite) TestEdit() {
	err := s.run(cmdEdit, "old-note", "--edit-code", "secret-code", "--content", "new")

	s.Require().ErrorIs(err, ErrNoteGone)
	s.Equal("PUT /notes/old-note", s.method)
}

func (s *UpdateGoneSuite) TestUpdateLink() {
	err := s.run(cmdUpdateLink, testLinkID, "--edit-code", "secret-code", "--target-url", "https://new.example")

	s.Require().ErrorIs(err, ErrLinkGone)
	s.Equal("PUT /links/"+testLinkID, s.method)
}

func (s *UpdateGoneSuite) TestUpdateFile() {
	err := s.run(cmdUpdateFile, testFileID, "--edit-code", "secret-code", "--burn=false")

	s.Require().ErrorIs(err, ErrFileGone)
	s.Equal("PUT /files/"+testFileID, s.method)
}
