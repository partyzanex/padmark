package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type UploadRootSuite struct {
	suite.Suite
}

func TestUploadRoot(t *testing.T) {
	suite.Run(t, new(UploadRootSuite))
}

func (s *UploadRootSuite) TestEmptyTurnsUploadsOff() {
	root, err := resolveUploadRoot("")

	s.Require().NoError(err)
	s.Empty(root)
}

// TestKeepsLinkPath checks that a root link to a directory is accepted and kept as given. The
// agent can then use the root path of the operator in an absolute path; openUploadFile resolves
// the link again at each call.
func (s *UploadRootSuite) TestKeepsLinkPath() {
	dir := s.T().TempDir()

	link := filepath.Join(s.T().TempDir(), "root-link")
	s.Require().NoError(os.Symlink(dir, link))

	root, err := resolveUploadRoot(link)

	s.Require().NoError(err)
	s.Equal(link, root)
}

func (s *UploadRootSuite) TestMakesRelativeRootAbsolute() {
	s.T().Chdir(s.T().TempDir())
	s.Require().NoError(os.Mkdir("share", 0o700))

	root, err := resolveUploadRoot("share")

	s.Require().NoError(err)
	s.True(filepath.IsAbs(root), "the root is absolute: %s", root)
	s.Equal("share", filepath.Base(root))
}

func (s *UploadRootSuite) TestHasHiddenElement() {
	cases := map[string]bool{
		".":                false,
		"report.txt":       false,
		"sub/report.txt":   false,
		"a.b/c.d":          false,
		".env":             true,
		".git/config":      true,
		"sub/.ssh/id_rsa":  true,
		"sub/dir/.hidden":  true,
		"sub/..evil/a.txt": true,
	}

	for rel, want := range cases {
		s.Equal(want, hasHiddenElement(filepath.FromSlash(rel)), rel)
	}
}

func (s *UploadRootSuite) TestRefusesFileAndMissingPath() {
	file := filepath.Join(s.T().TempDir(), "a.txt")
	s.Require().NoError(os.WriteFile(file, []byte("x"), 0o600))

	_, err := resolveUploadRoot(file)
	s.Require().ErrorIs(err, errUploadRootNotDir)

	_, err = resolveUploadRoot(filepath.Join(s.T().TempDir(), "missing"))
	s.Require().ErrorIs(err, os.ErrNotExist)
}

// TestRunRefusesBadUploadRoot checks the fail-hard startup: the binary stops before it serves.
func (s *UploadRootSuite) TestRunRefusesBadUploadRoot() {
	isolateEnv(s.T())

	file := filepath.Join(s.T().TempDir(), "a.txt")
	s.Require().NoError(os.WriteFile(file, []byte("x"), 0o600))

	app := NewApp()
	err := app.Run(s.T().Context(), []string{"padmark-mcp", "--" + FlagUploadRoot, file})

	s.Require().ErrorIs(err, errUploadRootNotDir)
}
