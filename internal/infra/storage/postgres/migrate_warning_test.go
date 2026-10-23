//go:build integration

package postgres

import (
	"bytes"
	"log/slog"
)

// TestMigrateWithWarning_UpToDate_NoWarning: the suite DB is fully migrated, so no WARN is emitted.
func (s *RepositoryTestSuite) TestMigrateWithWarning_UpToDate_NoWarning() {
	var buf bytes.Buffer

	log := slog.New(slog.NewTextHandler(&buf, nil))

	_, err := MigrateWithWarning(s.T().Context(), s.db, log)
	s.Require().NoError(err)
	s.Empty(buf.String())
}
