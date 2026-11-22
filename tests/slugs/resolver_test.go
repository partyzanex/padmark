//go:build integration

package slugs_test

import (
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"

	pgrepo "github.com/partyzanex/padmark/internal/infra/storage/postgres"
	sqliterepo "github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/slugs"
)

// slugResolver returns the resolver of the web handler for the backend of db.
func slugResolver(db *bun.DB) *slugs.Resolver {
	if db.Dialect().Name() == dialect.PG {
		return slugs.NewResolver(pgrepo.NewSlugRepository(db))
	}

	return slugs.NewResolver(sqliterepo.NewSlugRepository(db))
}
