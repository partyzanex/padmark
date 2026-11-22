//go:build integration

package slugs_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"

	"github.com/partyzanex/padmark/internal/domain"
	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
)

// concurrentWriters is the number of writers of each kind that race for one slug.
const concurrentWriters = 2

// storageCreate inserts a record of resource with the record ID id through the production
// repository of the dialect, with no manager and no check before the INSERT.
func storageCreate(ctx context.Context, db *bun.DB, resource, id string) error {
	noteRepo, linkRepo := repositories(db)
	now := time.Now().UTC()

	switch resource {
	case "notes":
		return noteRepo.Create(ctx, &domain.Note{
			ID: id, Title: "", Content: "ciphertext", CreatedAt: now, UpdatedAt: now, ExpiresAt: nil,
			Privacy: nil, OwnerID: nil, ContentType: nil, EditCode: "hash",
			Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false, ShowContents: nil,
		})
	case "links":
		return linkRepo.Create(ctx, &domain.Link{
			ID: id, TargetURL: "ciphertext", CreatedAt: now, ExpiresAt: nil, Privacy: nil, OwnerID: nil,
			DirectRedirect: nil, EditCode: "hash", Views: 0, BurnAfterClick: false,
		})
	default:
		file := &domain.File{
			ID: id, ObjectKey: uuid.NewString(), SealedMeta: "sealed", CreatedAt: now, ExpiresAt: nil,
			Privacy: nil, OwnerID: nil, Name: "", ContentKey: nil, EditCode: "hash",
			Size: 1, Downloads: 0, BurnAfterDownload: false,
		}
		if db.Dialect().Name() == dialect.PG {
			return pgrepo.NewFileRepository(db).Create(ctx, file)
		}

		return sqliterepo.NewFileRepository(db).Create(ctx, file)
	}
}

// testStorageConflicts proves that the storage alone keeps slugs unique, which is the reason
// why the managers make no check before Create (docs/adr/0003-storage-slug-uniqueness.md). For
// each pair of kinds, the second Create returns domain.ErrSlugConflict and the first record
// keeps the reservation.
func testStorageConflicts(t *testing.T, db *bun.DB) {
	t.Helper()

	for _, holder := range sharedResources() {
		for _, claimant := range sharedResources() {
			t.Run(holder+"_then_"+claimant, func(t *testing.T) {
				id := domain.HashSlug(uuid.NewString())
				require.NoError(t, storageCreate(t.Context(), db, holder, id))

				err := storageCreate(t.Context(), db, claimant, id)
				require.ErrorIs(t, err, domain.ErrSlugConflict)
				assertReservation(t, db, id, holder)
				assertOneHolder(t, db, id)
			})
		}
	}
}

// testConcurrentStorageCreates starts two writers of each kind on separate connection pools at
// the same time, for several slugs. Exactly one Create wins each slug; all others get
// domain.ErrSlugConflict.
func testConcurrentStorageCreates(t *testing.T, db *bun.DB, open func(*testing.T) *bun.DB) {
	t.Helper()

	pools := make([]*bun.DB, 0, len(sharedResources())*concurrentWriters)
	for range cap(pools) {
		pools = append(pools, open(t))
	}

	for round := range 10 {
		t.Run(fmt.Sprintf("round_%d", round), func(t *testing.T) {
			id := domain.HashSlug(uuid.NewString())
			start := make(chan struct{})
			errs := make([]error, len(pools))

			var writers sync.WaitGroup

			for index, pool := range pools {
				resource := sharedResources()[index%len(sharedResources())]

				writers.Go(func() {
					<-start

					errs[index] = storageCreate(t.Context(), pool, resource, id)
				})
			}

			close(start)
			writers.Wait()

			winners := 0

			for _, err := range errs {
				if err == nil {
					winners++

					continue
				}

				require.ErrorIs(t, err, domain.ErrSlugConflict)
			}

			require.Equal(t, 1, winners)
			assertOneHolder(t, db, id)
		})
	}
}

// assertOneHolder checks that exactly one note, link or file has id.
func assertOneHolder(t *testing.T, db bun.IDB, id string) {
	t.Helper()

	var count int

	require.NoError(t, db.NewRaw(`SELECT COUNT(*) FROM (
		SELECT id FROM notes WHERE id = ? UNION ALL SELECT id FROM links WHERE id = ?
		UNION ALL SELECT id FROM files WHERE id = ?
	) AS occupied`, id, id, id).Scan(t.Context(), &count))
	require.Equal(t, 1, count)
}
