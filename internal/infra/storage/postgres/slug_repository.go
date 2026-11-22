package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/uptrace/bun"

	"github.com/partyzanex/padmark/internal/domain"
	"github.com/partyzanex/padmark/internal/usecases/slugs"
)

// SlugRepository implements slugs.Storage using PostgreSQL. The storage triggers of notes, links and
// files keep the slug_namespace rows (migrations/postgres/20261025000001_slug_namespace.sql and
// 20261118000001_files.sql).
type SlugRepository struct {
	db *bun.DB
}

// NewSlugRepository returns a PostgreSQL-backed SlugRepository.
func NewSlugRepository(db *bun.DB) *SlugRepository {
	return &SlugRepository{db: db}
}

// Kind returns the resource name of the slug_namespace row with id, or domain.ErrNotFound.
func (r *SlugRepository) Kind(ctx context.Context, id string) (slugs.Kind, error) {
	var resource string

	err := r.db.NewSelect().Table("slug_namespace").Column("resource").Where("id = ?", id).Scan(ctx, &resource)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", domain.ErrNotFound
		}

		return "", fmt.Errorf("postgres slug kind: %w", err)
	}

	return slugs.Kind(resource), nil
}
