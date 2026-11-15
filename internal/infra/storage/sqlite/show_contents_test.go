package sqlite

import "github.com/partyzanex/padmark/internal/domain"

// showContentsCases are the create-time values: nil is an old client that does not send the option.
func showContentsCases() []struct {
	value *bool
	id    string
	want  bool
} {
	return []struct {
		value *bool
		id    string
		want  bool
	}{{nil, "sc-nil", false}, {new(false), "sc-false", false}, {new(true), "sc-true", true}}
}

func (s *RepositoryTestSuite) TestShowContents_CreateAndGet() {
	ctx := s.T().Context()

	for _, tcase := range showContentsCases() {
		note := plainNote(tcase.id)
		note.ShowContents = tcase.value
		s.Require().NoError(s.repo.Create(ctx, note))

		got, err := s.repo.Get(ctx, tcase.id)
		s.Require().NoError(err)
		s.Require().NotNil(got.ShowContents, tcase.id)
		s.Equal(tcase.want, *got.ShowContents, tcase.id)
	}
}

// TestShowContents_UpdateNilKeepsStoredValue pins the COALESCE contract: an update without the
// option keeps the stored value, and an explicit value replaces it.
func (s *RepositoryTestSuite) TestShowContents_UpdateNilKeepsStoredValue() {
	ctx := s.T().Context()

	note := plainNote("sc-update")
	note.ShowContents = new(true)
	s.Require().NoError(s.repo.Create(ctx, note))

	for _, step := range []struct {
		value *bool
		want  bool
	}{{nil, true}, {new(false), false}, {nil, false}, {new(true), true}} {
		update := plainNote("sc-update")
		update.ShowContents = step.value
		s.Require().NoError(s.repo.Update(ctx, "sc-update", update))

		got, err := s.repo.Get(ctx, "sc-update")
		s.Require().NoError(err)
		s.Equal(step.want, got.ContentsSidebar())
	}
}

func (s *RepositoryTestSuite) TestShowContents_ConsumeReturnsValue() {
	ctx := s.T().Context()

	note := plainNote("sc-burn")
	note.BurnAfterReading = true
	note.ShowContents = new(true)
	s.Require().NoError(s.repo.Create(ctx, note))

	got, err := s.repo.Consume(ctx, "sc-burn", domain.PrivacyPublic)
	s.Require().NoError(err)
	s.True(got.ContentsSidebar())
}
