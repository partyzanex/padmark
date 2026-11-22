//go:generate go run go.uber.org/mock/mockgen@latest -source=releaser.go -destination=releaser_mocks_test.go -package=expiredslugs -write_package_comment=false

// Package expiredslugs removes an expired note, link or file that still holds a slug, so that a
// new record can take the slug. Storage.Create is the only slug uniqueness check
// (docs/adr/0003-storage-slug-uniqueness.md); the managers ask Releaser after a slug conflict.
package expiredslugs

import (
	"context"
	"errors"
	"fmt"

	"github.com/partyzanex/padmark/internal/domain"
)

// RecordExpirer removes the note or link with id while it is expired. It returns
// domain.ErrNotFound when no expired record has id. The note and link repositories implement it.
type RecordExpirer interface {
	DeleteExpired(ctx context.Context, id string) error
}

// FileReleaser removes the file with id while it is expired, with its object. files.Manager
// implements it.
type FileReleaser interface {
	ReleaseExpiredSlug(ctx context.Context, id string) (bool, error)
}

// noFiles is the FileReleaser of a server without file publishing: no file holds a slug.
type noFiles struct{}

func (noFiles) ReleaseExpiredSlug(context.Context, string) (bool, error) { return false, nil }

// Releaser is the one ExpiredSlugReleaser of notes.Manager, links.Manager and files.Manager.
// After a slug conflict a Manager asks it to remove an expired holder of any kind. One record at
// most holds a slug, so the first removal ends the search. Each removal checks the expiry in its
// DELETE, so a record that its owner extended stays. Errors are fail-soft for the managers: they
// keep the slug conflict.
type Releaser struct {
	notes RecordExpirer
	links RecordExpirer
	// files is noFiles until WithFiles sets the file manager.
	files FileReleaser
}

// New returns a Releaser for notes and links. Without WithFiles, no file holds a slug.
func New(notes, links RecordExpirer) *Releaser {
	return &Releaser{notes: notes, links: links, files: noFiles{}}
}

// WithFiles adds the file manager and returns r. The file manager itself gets r, so the
// composition root calls WithFiles after it builds the file manager, before the first request.
func (r *Releaser) WithFiles(files FileReleaser) *Releaser {
	r.files = files

	return r
}

// ReleaseExpiredSlug reports whether it removed an expired note, link or file with id.
func (r *Releaser) ReleaseExpiredSlug(ctx context.Context, id string) (bool, error) {
	for _, records := range []RecordExpirer{r.notes, r.links} {
		err := records.DeleteExpired(ctx, id)
		if err == nil {
			return true, nil
		}

		if !errors.Is(err, domain.ErrNotFound) {
			return false, fmt.Errorf("release expired slug: %w", err)
		}
	}

	released, err := r.files.ReleaseExpiredSlug(ctx, id)
	if err != nil {
		return false, fmt.Errorf("release expired slug: %w", err)
	}

	return released, nil
}
