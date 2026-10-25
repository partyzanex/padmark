//go:build integration

package integration

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun/dialect"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/infra/lock"
	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/links"
)

type changedLinkRead struct {
	links.Storage

	once      sync.Once
	afterRead func()
}

func (r *changedLinkRead) GetByID(ctx context.Context, id string) (*domain.Link, error) {
	link, err := r.Storage.GetByID(ctx, id)
	if err == nil {
		r.once.Do(r.afterRead)
	}

	return link, err
}

//nolint:ireturn // test factory selects backend implementations of the same usecase ports
func (s *ManagerSuite) linkStorage() (links.Storage, links.NotesChecker) {
	if s.DB.Dialect().Name() == dialect.PG {
		return pgrepo.NewLinkRepository(s.DB, discardLog), pgrepo.NewNoteRepository(s.DB)
	}

	return sqliterepo.NewLinkRepository(s.DB, discardLog), sqliterepo.NewNoteRepository(s.DB)
}

// This suite runs against SQLite and PostgreSQL. A second manager commits between SELECT
// and the read's mutation, exercising each backend's actual visibility predicates.
func (s *ManagerSuite) TestFollowConcurrentPrivacyChange() {
	storage, checker := s.linkStorage()
	ownerID := uuid.New()
	_, err := s.DB.NewRaw("INSERT INTO users (id, username, totp_secret) VALUES (?, ?, ?)",
		ownerID, ownerID.String(), "unused-test-secret").Exec(s.T().Context())
	s.Require().NoError(err)

	for _, scenario := range []struct {
		name    string
		burn    bool
		expired bool
	}{
		{name: "ordinary"},
		{name: "burn", burn: true},
		{name: "expired_then_renewed", expired: true},
	} {
		s.Run(scenario.name, func() {
			options := links.CreateOptions{OwnerID: &ownerID, BurnAfterClick: scenario.burn}
			if scenario.expired {
				options.ExpiresAt = new(time.Now().Add(-time.Hour))
			}

			link, createErr := s.LinkManager.Create(s.T().Context(), "https://example.com/public", options)
			s.Require().NoError(createErr)

			privateURL := "https://example.com/private?secret=hidden"
			changing := &changedLinkRead{Storage: storage, afterRead: func() {
				_, updateErr := s.LinkManager.Update(s.T().Context(), link.ID, "", ownerID, privateURL,
					links.UpdateOptions{Privacy: new(domain.PrivacyOwner), ExpiresAt: new(time.Now().Add(time.Hour))})
				s.Require().NoError(updateErr)
			}}
			reader := newLinkManager(s.T(), changing, checker, lock.NewLocker())
			result, followErr := reader.Follow(s.T().Context(), link.ID, links.FollowOptions{AllowBurn: true})
			s.Require().ErrorIs(followErr, domain.ErrNotFound)
			s.Nil(result, "a raced privacy change must not expose the new destination")
			stored, getErr := storage.GetByID(s.T().Context(), domain.HashSlug(link.ID))
			s.Require().NoError(getErr, "the private replacement must survive both burn and expiry cleanup")
			s.Zero(stored.Views)

			followed, ownerErr := reader.Follow(s.T().Context(), link.ID, links.FollowOptions{
				CallerID: ownerID, Authenticated: true, AllowBurn: true,
			})
			s.Require().NoError(ownerErr)
			s.Equal(privateURL, followed.TargetURL)

			if scenario.burn {
				_, followErr = reader.Follow(s.T().Context(), link.ID, links.FollowOptions{
					CallerID: ownerID, Authenticated: true, AllowBurn: true,
				})
				s.Require().ErrorIs(followErr, domain.ErrNotFound, "only one owner read may consume the link")
			}
		})
	}
}
