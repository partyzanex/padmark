package server

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

func TestDefaultKeyringPath(t *testing.T) {
	cases := []struct {
		name, storage, dsn, want string
	}{
		{"bare relative", "sqlite", "padmark.db", "padmark-keys.json"},
		{"bare with query", "sqlite", "data/padmark.db?_pragma=foreign_keys(1)", filepath.Join("data", "padmark-keys.json")},
		{"absolute", "sqlite", "/var/lib/padmark/padmark.db", "/var/lib/padmark/padmark-keys.json"},
		{
			"file uri with query", "sqlite", "file:/var/lib/padmark/p.db?cache=shared&mode=rwc",
			"/var/lib/padmark/padmark-keys.json",
		},
		{"in-memory", "sqlite", ":memory:", ""},
		{"file memory uri", "sqlite", "file::memory:?cache=shared", ""},
		{"empty dsn", "sqlite", "", ""},
		{"postgres", "postgres", "postgres://u:p@h/db", "padmark-keys.json"},
	}

	for _, tc := range cases {
		require.Equal(t, tc.want, defaultKeyringPath(tc.storage, tc.dsn), tc.name)
	}
}

func TestResolveKeyringPathErrors(t *testing.T) {
	path, derived, err := resolveKeyringPath("", "postgres", "postgres://u:p@h/db")
	require.NoError(t, err)
	require.Equal(t, "padmark-keys.json", path)
	require.True(t, derived)

	_, _, err = resolveKeyringPath("", "sqlite", ":memory:")
	require.ErrorContains(t, err, "content keyring is required")

	path, derived, err = resolveKeyringPath("/x/keys.json", "postgres", "")
	require.NoError(t, err)
	require.Equal(t, "/x/keys.json", path)
	require.False(t, derived)
}

type ServeKeyringSuite struct {
	suite.Suite
}

func TestServeKeyringSuite(t *testing.T) {
	suite.Run(t, new(ServeKeyringSuite))
}

func (s *ServeKeyringSuite) TestLoadServeKeyringCreatesThenReuses() {
	dsn := filepath.Join(s.T().TempDir(), "padmark.db")
	keyPath := filepath.Join(filepath.Dir(dsn), defaultKeyringName)

	var logs bytes.Buffer

	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
		Level:     slog.LevelWarn,
		AddSource: false, ReplaceAttr: nil,
	}))

	first, err := loadServeKeyring("", "sqlite", dsn, false, log)
	s.Require().NoError(err)
	s.Require().Equal("k1", first.Active)
	s.Require().Contains(logs.String(), "level=WARN")
	s.Require().Contains(logs.String(), keyPath)
	s.Require().Contains(logs.String(), "back it up")

	info, err := os.Stat(keyPath)
	s.Require().NoError(err)
	s.Require().Equal(os.FileMode(0o600), info.Mode().Perm())

	before, err := os.ReadFile(keyPath) //nolint:gosec // test-controlled temp path
	s.Require().NoError(err)

	logs.Reset()

	second, err := loadServeKeyring("", "sqlite", dsn, false, log)
	s.Require().NoError(err)
	s.Require().Equal(first.Active, second.Active)
	s.Require().Equal(first.Keys["k1"], second.Keys["k1"])
	s.Require().NotContains(logs.String(), "generated")

	after, err := os.ReadFile(keyPath) //nolint:gosec // test-controlled temp path
	s.Require().NoError(err)
	s.Require().Equal(before, after)
}

func (s *ServeKeyringSuite) TestLoadServeKeyringExplicitMissingFails() {
	dir := s.T().TempDir()
	explicit := filepath.Join(dir, "missing.json")

	_, err := loadServeKeyring(explicit, "sqlite", filepath.Join(dir, "padmark.db"), false, slog.New(slog.DiscardHandler))
	s.Require().Error(err)
	s.Require().NoFileExists(explicit)
	s.Require().NoFileExists(filepath.Join(dir, defaultKeyringName))
}

func (s *ServeKeyringSuite) TestLoadServeKeyringPostgresCreatesDefault() {
	s.T().Chdir(s.T().TempDir())

	ring, err := loadServeKeyring("", "postgres", "postgres://h/db", false, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.Require().Equal("k1", ring.Active)
	s.Require().FileExists(defaultKeyringName)
}

func (s *ServeKeyringSuite) TestLoadToolKeyringDoesNotCreate() {
	dir := s.T().TempDir()
	dsn := filepath.Join(dir, "padmark.db")

	_, err := loadToolKeyring("", "sqlite", dsn, false, slog.New(slog.DiscardHandler))
	s.Require().Error(err)
	s.Require().NoFileExists(filepath.Join(dir, defaultKeyringName))
}
