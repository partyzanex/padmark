//go:build integration

package cli_test

import (
	"strings"

	adaptercli "github.com/partyzanex/padmark/internal/adapters/cli"
	"github.com/partyzanex/padmark/tests/internal/servertest"
)

// TestLinkLifecycle runs the link commands against a real server: shorten → update-link →
// expand → delete-link. The links need no object storage, so the server runs without S3.
func (s *FilesSuite) TestLinkLifecycle() {
	serverURL, _ := servertest.Start(s.T(), s.binary, s.T().TempDir(), "--rate-limit", "0")

	out, err := s.cli(serverURL, "shorten", "--target-url", "https://example.com/old")
	s.Require().NoError(err, out)

	created := textFields(out)
	s.Require().NotEmpty(created["id"])
	s.Require().NotEmpty(created["edit-code"])

	out, err = s.cli(serverURL, "update-link", created["id"],
		"--target-url", "https://example.com/new#part", "--ttl", "3600", "--edit-code", created["edit-code"])
	s.Require().NoError(err, out)

	updated := textFields(out)
	s.Equal("https://example.com/new#part", updated["target_url"])
	s.NotEmpty(updated["expires"], "--ttl sets an expiry")

	_, err = s.cli(serverURL, "update-link", created["id"],
		"--target-url", "https://example.com/other", "--edit-code", "WrongCode123")
	s.Require().ErrorIs(err, adaptercli.ErrForbidden)

	out, err = s.cli(serverURL, "expand", created["id"])
	s.Require().NoError(err, out)
	s.Equal("https://example.com/new#part", textFields(out)["target_url"], "a refused update changes nothing")

	out, err = s.cli(serverURL, "delete-link", created["id"], "--edit-code", created["edit-code"])
	s.Require().NoError(err, out)

	_, err = s.cli(serverURL, "expand", created["id"])
	s.Require().ErrorIs(err, adaptercli.ErrLinkNotFound)
}

// textFields parses the "label:  value" lines of the text output.
func textFields(out string) map[string]string {
	fields := make(map[string]string)

	for line := range strings.SplitSeq(out, "\n") {
		label, value, ok := strings.Cut(line, ":")
		if ok {
			fields[label] = strings.TrimSpace(value)
		}
	}

	return fields
}
