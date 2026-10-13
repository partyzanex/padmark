package sqlite

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// runReadModifyWrite drives writers goroutines through iters read-then-write transactions each,
// the shape every RunInTx in internal/infra/storage/sqlite uses (count-then-insert for API
// tokens, select-then-update for invite and reveal consume). It returns the failures by message
// and the counter's final value.
func runReadModifyWrite(t *testing.T, dsn string, writers, iters int) (failures map[string]int, final int) {
	t.Helper()

	db, err := Open(dsn)
	require.NoError(t, err)

	defer func() { require.NoError(t, db.Close()) }()

	ctx := t.Context()

	_, err = db.ExecContext(ctx, `CREATE TABLE ctr (id INTEGER PRIMARY KEY, n INTEGER NOT NULL)`)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `INSERT INTO ctr (id, n) VALUES (1, 0)`)
	require.NoError(t, err)

	var (
		errsMu sync.Mutex
		errs   = map[string]int{}
		grp    sync.WaitGroup
	)

	for range writers {
		grp.Go(func() {
			for range iters {
				txErr := db.RunInTx(ctx, nil, func(ctx context.Context, dbTx bun.Tx) error {
					var counter int

					scanErr := dbTx.QueryRowContext(ctx, `SELECT n FROM ctr WHERE id = 1`).Scan(&counter)
					if scanErr != nil {
						return scanErr
					}

					_, execErr := dbTx.ExecContext(ctx, `UPDATE ctr SET n = ? WHERE id = 1`, counter+1)

					return execErr
				})
				if txErr != nil {
					errsMu.Lock()

					errs[txErr.Error()]++

					errsMu.Unlock()
				}
			}
		})
	}

	grp.Wait()

	require.NoError(t, db.QueryRowContext(ctx, `SELECT n FROM ctr WHERE id = 1`).Scan(&final))

	return errs, final
}

// TestSQLiteConcurrentWriters_NoLockContention is the regression guard for the _txlock=immediate
// setting in WithPragmas.
//
// Without it, SQLite's default deferred BEGIN leaves a read-then-write transaction holding a
// read lock when it tries to write. SQLite will not promote that lock — doing so risks deadlock
// — and returns SQLITE_BUSY (or SQLITE_BUSY_SNAPSHOT once another writer has committed past the
// snapshot) *without* consulting the busy handler, so busy_timeout cannot absorb it. Measured on
// this exact workload at 8x200, deferred lost 1480 of 1600 transactions; immediate lost none.
//
// Every transaction must therefore succeed, and the counter must equal the number of
// transactions: a lost update would mean two writers overlapped rather than serialised.
func TestSQLiteConcurrentWriters_NoLockContention(t *testing.T) {
	t.Parallel()

	const writers, iters = 8, 40

	dsn := WithPragmas(filepath.Join(t.TempDir(), "concurrency.db"))

	failures, final := runReadModifyWrite(t, dsn, writers, iters)

	require.Empty(t, failures, "read-then-write transactions must not fail under concurrent writers")
	require.Equal(t, writers*iters, final, "every transaction must be reflected in the counter")
}

// TestSQLiteConcurrentWriters_DeferredIsTheProblem pins the cause rather than the symptom: the
// same workload against an explicitly deferred DSN does fail. If a future SQLite or driver
// version made deferred transactions safe here, this test starts failing and the setting above
// can be reconsidered — without it, the reason for _txlock=immediate would only live in a
// comment.
func TestSQLiteConcurrentWriters_DeferredIsTheProblem(t *testing.T) {
	t.Parallel()

	const writers, iters = 8, 40

	dsn := WithPragmas(filepath.Join(t.TempDir(), "deferred.db") + "?_txlock=deferred")

	failures, _ := runReadModifyWrite(t, dsn, writers, iters)

	require.NotEmpty(t, failures,
		"deferred BEGIN is expected to lose transactions to lock contention; if it no longer "+
			"does, re-evaluate the _txlock=immediate default in WithPragmas")
}
