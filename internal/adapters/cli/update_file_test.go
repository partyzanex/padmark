package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/suite"
)

const cmdUpdateFile = "update-file"

type UpdateFileSuite struct {
	suite.Suite

	status   int
	body     string
	method   string
	editCode string
	payload  string
	server   *httptest.Server
}

func TestUpdateFileSuite(t *testing.T) {
	suite.Run(t, new(UpdateFileSuite))
}

func (s *UpdateFileSuite) SetupTest() {
	isolateEnv(s.T())

	s.status, s.body = http.StatusOK, fileInfoJSON(s.T(), testFileID, "notes.txt")
	s.method, s.editCode, s.payload = "", "", ""
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			s.T().Errorf("read body: %v", err)
		}

		s.method = r.Method + " " + r.URL.Path
		s.editCode, s.payload = r.Header.Get("X-Edit-Code"), string(data)
		writeTestResponse(s.T(), w, s.status, s.body)
	}))
}

func (s *UpdateFileSuite) TearDownTest() {
	s.server.Close()
}

func (s *UpdateFileSuite) update(args ...string) (string, error) {
	return runCLIOutput(s.T().Context(), append([]string{"--url", s.server.URL, cmdUpdateFile}, args...)...)
}

func (s *UpdateFileSuite) TestAllFields() {
	out, err := s.update(testFileID, "--ttl", "60", "--privacy", "owner", "--burn=false", "--edit-code", "secret-code")
	s.Require().NoError(err)

	s.Equal("PUT /files/f1le-id", s.method)
	s.Equal("secret-code", s.editCode, "the edit code goes only in the header")
	s.JSONEq(`{"ttl":60,"privacy":"owner","burn_after_download":false}`, s.payload)
	s.Equal(testFileID, outputFields(out)["id"])
}

func (s *UpdateFileSuite) TestClearExpiry() {
	_, err := s.update(testFileID, "--clear-expiry")
	s.Require().NoError(err)
	s.JSONEq(`{"ttl":null}`, s.payload)
	s.Empty(s.editCode)
}

func (s *UpdateFileSuite) TestBurnOnly() {
	_, err := s.update(testFileID, "--burn")
	s.Require().NoError(err)
	s.JSONEq(`{"burn_after_download":true}`, s.payload, "unset flags keep their values")
}

func (s *UpdateFileSuite) TestNothingToChange() {
	_, err := s.update(testFileID)
	s.Require().ErrorIs(err, errNoFileChange)
	s.Empty(s.method)
}

func (s *UpdateFileSuite) TestTTLConflict() {
	_, err := s.update(testFileID, "--ttl", "60", "--clear-expiry")
	s.Require().ErrorIs(err, errTTLConflict)
	s.Empty(s.method)
}

func (s *UpdateFileSuite) TestNonPositiveTTL() {
	_, err := s.update(testFileID, "--ttl", "-1")
	s.Require().ErrorIs(err, errNonPositiveTTL)
	s.Empty(s.method)
}

func (s *UpdateFileSuite) TestForbidden() {
	s.status, s.body = http.StatusForbidden, `{"message":"invalid edit code"}`

	_, err := s.update(testFileID, "--burn", "--edit-code", "wrong-code")
	s.Require().ErrorIs(err, ErrForbidden)
	s.Require().ErrorContains(err, "wrong edit code or --token is not the file's owner")
}

func (s *UpdateFileSuite) TestOwnerPrivacyWithoutOwner() {
	s.status, s.body = http.StatusUnprocessableEntity, `{"message":"owner privacy requires an owner"}`

	_, err := s.update(testFileID, "--privacy", "owner")
	s.Require().ErrorIs(err, ErrValidation)
}
