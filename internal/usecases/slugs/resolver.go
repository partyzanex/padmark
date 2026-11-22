//go:generate go run go.uber.org/mock/mockgen@latest -source=resolver.go -destination=resolver_mocks_test.go -package=slugs -write_package_comment=false

// Package slugs tells which kind of record has a slug. Notes, short links and files share one
// slug namespace: the slug_namespace table keeps one row (id, resource) for each record.
package slugs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/partyzanex/padmark/internal/domain"
)

// Kind is the kind of record that has a slug. The values are the resource names that the
// slug_namespace table keeps, so the storage returns the column value without a translation.
type Kind string

const (
	// KindNote is a note.
	KindNote Kind = "notes"
	// KindLink is a short link.
	KindLink Kind = "links"
	// KindFile is a file.
	KindFile Kind = "files"
)

// errUnknownKind tells that the slug namespace has a resource name that this version does not
// know. Only a database written by a newer version can have one.
var errUnknownKind = errors.New("unknown record kind in the slug namespace")

// Storage reads the slug namespace. Errors are fail-hard: without the kind, the caller cannot
// select the record, so it returns the error.
type Storage interface {
	// Kind returns the resource name of the namespace row with id, domain.HashSlug of the slug.
	// It returns domain.ErrNotFound when no row has the id.
	Kind(ctx context.Context, id string) (Kind, error)
}

// Resolver tells which kind of record has a slug, with one read of the slug namespace.
type Resolver struct {
	storage Storage
}

// NewResolver returns a Resolver that reads the namespace from storage.
func NewResolver(storage Storage) *Resolver {
	return &Resolver{storage: storage}
}

// KindOf returns the kind of record that has slug, or domain.ErrNotFound when no record has it.
// A slug with "/" can name only a note: links and files refuse such a slug at creation
// (links.Manager and files.Manager checkCustomSlug). Thus KindOf returns KindNote for it without
// a read, and the note read then tells whether the note exists.
//
// An expired record keeps its row until a read, a sweep or a new record with the same slug
// deletes it. Thus KindOf still returns its kind, and the Manager of that kind gives
// domain.ErrExpired as before.
func (r *Resolver) KindOf(ctx context.Context, slug string) (Kind, error) {
	if strings.Contains(slug, "/") {
		return KindNote, nil
	}

	kind, err := r.storage.Kind(ctx, domain.HashSlug(slug))
	if err != nil {
		return "", fmt.Errorf("slug kind: %w", err)
	}

	switch kind {
	case KindNote, KindLink, KindFile:
		return kind, nil
	default:
		return "", fmt.Errorf("slug kind %q: %w", kind, errUnknownKind)
	}
}
