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

func (s *UploadRootSuite) TestResolvesLinks() {
	dir := s.T().TempDir()
	want, err := filepath.EvalSymlinks(dir)
	s.Require().NoError(err)

	link := filepath.Join(s.T().TempDir(), "root-link")
	s.Require().NoError(os.Symlink(dir, link))

	root, err := resolveUploadRoot(link)

	s.Require().NoError(err)
	s.Equal(want, root)
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
