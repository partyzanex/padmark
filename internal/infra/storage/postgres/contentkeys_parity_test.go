//go:build integration

package postgres

import (
	"fmt"
	"time"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/storage/contentkeys"
	keys "github.com/partyzanex/padmark/internal/usecases/contentkeys"
)

// contentkeys.Repository is one bun implementation shared by both backends; its direct tests
// (storage/contentkeys/repository_test.go) run on SQLite only. These ports run the same
// assertions against PostgreSQL.

func (s *ParitySuite) TestContentKeys_InitializeRollsBackStateWhenKeyCheckInsertFails() {
	ctx := s.T().Context()
	repo := contentkeys.New(s.db)
	s.Require().NoError(repo.Register(ctx, "existing", "original-check"))

	err := repo.Initialize(ctx, &keys.Registry{
		InstallationID: "11111111-1111-4111-8111-111111111111", Phase: keys.Pending,
		Checks: map[string]string{"existing": "replacement-check"},
	})
	s.Require().Error(err)

	registry, err := repo.Registry(ctx)
	s.Require().NoError(err)
	s.Nil(registry, "state and checks must commit atomically")

	var check string

	s.Require().NoError(s.db.NewSelect().Table("content_encryption_keys").Column("verification").
		Where("key_id = ?", "existing").Scan(ctx, &check))
	s.Equal("original-check", check)
}

func (s *ParitySuite) TestContentKeys_InitializeLeavesWinningRegistryUntouched() {
	ctx := s.T().Context()
	repo := contentkeys.New(s.db)

	s.Require().NoError(repo.Initialize(ctx, &keys.Registry{
		InstallationID: "11111111-1111-4111-8111-111111111111", Phase: keys.Pending,
		Checks: map[string]string{"winner": "winner-check"},
	}))
	s.Require().NoError(repo.Initialize(ctx, &keys.Registry{
		InstallationID: "22222222-2222-4222-8222-222222222222", Phase: keys.Pending,
		Checks: map[string]string{"loser": "loser-check"},
	}))

	registry, err := repo.Registry(ctx)
	s.Require().NoError(err)
	s.Require().NotNil(registry)
	s.Equal("11111111-1111-4111-8111-111111111111", registry.InstallationID)
	s.Equal(map[string]string{"winner": "winner-check"}, registry.Checks)
}

func (s *ParitySuite) TestContentKeys_CiphertextCASPreservesConcurrentReplacements() {
	ctx := s.T().Context()
	s.Require().NoError(NewNoteRepository(s.db).Create(ctx, &domain.Note{
		ID: domain.HashSlug("slug"), Content: "old", EditCode: "hash", CreatedAt: time.Now(), UpdatedAt: time.Now(),

		ExpiresAt: nil, Privacy: nil, OwnerID: nil, Title: "",
		ContentType: nil, Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}))

	repo := contentkeys.New(s.db)

	row, err := repo.Get(ctx, "note", domain.HashSlug("slug"))
	s.Require().NoError(err)
	s.Require().NotNil(row)

	changed, err := repo.Replace(ctx, *row, "concurrent")
	s.Require().NoError(err)
	s.True(changed)

	changed, err = repo.Replace(ctx, *row, "stale-replacement")
	s.Require().NoError(err)
	s.False(changed)

	deleted, err := repo.Delete(ctx, *row)
	s.Require().NoError(err)
	s.False(deleted)

	stored, err := repo.Get(ctx, "note", row.ID)
	s.Require().NoError(err)
	s.Equal("concurrent", stored.Value)
}

func (s *ParitySuite) TestContentKeys_ConvertLegacyCASAndTargetConflict() {
	ctx := s.T().Context()

	for _, slug := range []string{"legacy-slug", "taken-slug"} {
		_, err := s.db.NewRaw(`INSERT INTO notes
 (id, title, content, edit_code, views, burn_after_reading, burn_ttl, privacy, created_at, updated_at, legacy_slug)
 VALUES (?, 'title', ?, 'hunter2', 0, false, 0, 'public', ?, ?, ?)`,
			slug, "plain "+slug, time.Now(), time.Now(), slug).Exec(ctx)
		s.Require().NoError(err)
	}

	s.Require().NoError(NewNoteRepository(s.db).Create(ctx, &domain.Note{
		ID: domain.HashSlug("taken-slug"), Content: "current", EditCode: "s1$hash",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),

		ExpiresAt: nil, Privacy: nil, OwnerID: nil, Title: "",
		ContentType: nil, Views: 0, BurnTTL: 0, BurnAfterReading: false,
		ClearExpiry: false,
	}))

	repo := contentkeys.New(s.db)

	legacy, err := repo.Get(ctx, "note", "legacy-slug")
	s.Require().NoError(err)
	s.Require().Equal(&keys.LegacyNote{Slug: "legacy-slug", EditCode: "hunter2"}, legacy.Legacy)

	stale := *legacy
	stale.Value = "edited meanwhile"
	converted, err := repo.ConvertLegacy(ctx, stale, domain.HashSlug("legacy-slug"), "pmc2$p$new", "v1$hash")
	s.Require().NoError(err)
	s.False(converted)

	converted, err = repo.ConvertLegacy(ctx, *legacy, domain.HashSlug("legacy-slug"), "pmc2$p$new", "v1$hash")
	s.Require().NoError(err)
	s.True(converted)

	current, err := repo.Get(ctx, "note", domain.HashSlug("legacy-slug"))
	s.Require().NoError(err)
	s.Equal(keys.Row{
		Resource: "note", ID: domain.HashSlug("legacy-slug"), Value: "pmc2$p$new", StoredEditCode: "v1$hash",

		Legacy: nil,
	}, *current)

	taken, err := repo.Get(ctx, "note", "taken-slug")
	s.Require().NoError(err)
	converted, err = repo.ConvertLegacy(ctx, *taken, domain.HashSlug("taken-slug"), "pmc2$p$new", "s1$new")
	s.Require().ErrorIs(err, keys.ErrConvertTargetExists)
	s.False(converted)
	s.NotContains(err.Error(), "taken")

	untouched, err := repo.Get(ctx, "note", "taken-slug")
	s.Require().NoError(err)
	s.Equal(taken, untouched)

	existing, err := repo.Get(ctx, "note", domain.HashSlug("taken-slug"))
	s.Require().NoError(err)
	s.Equal("current", existing.Value)
}

func (s *ParitySuite) TestContentKeys_ConcurrentReplaceSingleWinner() {
	ctx := s.T().Context()
	s.Require().NoError(NewLinkRepository(s.db, s.discard).Create(ctx, &domain.Link{
		ID: domain.HashSlug("link"), TargetURL: "old", CreatedAt: time.Now(),

		ExpiresAt: nil, Privacy: nil, OwnerID: nil, DirectRedirect: nil,
		EditCode: "", Views: 0, BurnAfterClick: false,
	}))

	repo := contentkeys.New(s.db)

	row, err := repo.Get(ctx, "link", domain.HashSlug("link"))
	s.Require().NoError(err)
	s.Require().NotNil(row)

	results := race(func(attempt int) consumeOutcome {
		changed, replaceErr := repo.Replace(ctx, *row, fmt.Sprintf("rewrapped-%d", attempt))

		return consumeOutcome{err: replaceErr, ok: changed}
	})

	for _, result := range results {
		s.Require().NoError(result.err)
	}

	s.Equal(1, singleWinner(results, func(r consumeOutcome) bool { return r.ok }),
		"exactly one rewrap may replace a given ciphertext")
}
