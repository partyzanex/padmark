//go:build integration

package encryption_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	keyrepo "github.com/partyzanex/padmark/internal/infra/storage/contentkeys"
	keys "github.com/partyzanex/padmark/internal/usecases/contentkeys"
)

type interruptedAutoStore struct {
	keys.Storage

	calls int
}

func (s *interruptedAutoStore) Replace(ctx context.Context, row keys.Row, value string) (bool, error) {
	s.calls++
	if s.calls == 2 {
		return false, errors.New("simulated interrupted startup")
	}

	return s.Storage.Replace(ctx, row, value)
}

func testAutomaticStartup(t *testing.T, db *bun.DB) {
	t.Helper()
	reset(t, db)

	repo := keyrepo.New(db)
	row1 := insertLegacy(t, db, "note", "automatic-one")
	row2 := insertLegacy(t, db, "note", "automatic-two")
	row3 := insertLegacy(t, db, "link", "automatic-link")
	ring := testKeyring()
	broken := &interruptedAutoStore{
		Storage: repo,
		calls:   0,
	}
	report, err := manager(t, broken, ring).MigrateIfNeeded(t.Context())
	require.ErrorContains(t, err, "interrupted startup")
	require.Equal(t, keys.Pending, report.State)
	require.Equal(t, 1, report.Changed)
	require.Equal(t, 1, report.Wrapped)
	registry, err := repo.Registry(t.Context())
	require.NoError(t, err)

	installation := registry.InstallationID

	startup := manager(t, repo, ring)
	report, err = startup.MigrateIfNeeded(t.Context())
	require.NoError(t, err)
	require.Equal(t, keys.Ready, report.State)
	require.Equal(t, 2, report.Changed, "resume wraps only the remaining rows")
	require.Equal(t, 3, report.Wrapped)
	rows := []keys.Row{readRow(t, repo, row1), readRow(t, repo, row2), readRow(t, repo, row3)}
	report, err = startup.MigrateIfNeeded(t.Context())
	require.NoError(t, err)
	require.Zero(t, report.Changed, "the ready phase skips all writes on restart")
	require.Equal(t, []keys.Row{readRow(t, repo, row1), readRow(t, repo, row2), readRow(t, repo, row3)}, rows)
	registry, err = repo.Registry(t.Context())
	require.NoError(t, err)
	require.Equal(t, installation, registry.InstallationID)

	wrong := testKeyring()
	wrong.Keys[wrong.Active] = bytes.Repeat([]byte{42}, 32)
	_, err = manager(t, repo, wrong).MigrateIfNeeded(t.Context())
	require.Error(t, err)
	require.Equal(t, []keys.Row{readRow(t, repo, row1), readRow(t, repo, row2), readRow(t, repo, row3)}, rows)

	_, err = db.NewRaw("UPDATE notes SET content = 'invalid raw value' WHERE id = ?", row1.ID).Exec(t.Context())
	require.NoError(t, err)
	_, err = startup.MigrateIfNeeded(t.Context())
	require.Error(t, err, "automatic startup must never prune corrupt records")
	require.Equal(t, "invalid raw value", readRow(t, repo, row1).Value)
}
