package server

import (
	"github.com/partyzanex/padmark/internal/infra/storage/postgres"
	"github.com/partyzanex/padmark/internal/infra/storage/sqlite"
	"github.com/partyzanex/padmark/internal/usecases/slugs"
)

// newSlugResolver returns the resolver that tells the web handler which kind of record has a
// slug. It reads the slug_namespace table of the storage backend.
func newSlugResolver(store serverStorage) *slugs.Resolver {
	if store.name == storagePostgres {
		return slugs.NewResolver(postgres.NewSlugRepository(store.db))
	}

	return slugs.NewResolver(sqlite.NewSlugRepository(store.db))
}
