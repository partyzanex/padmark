# ADR-0003: No slug pre-checks; storage uniqueness and retry

**Status**: Accepted
**Date**: 2026-11-22
**Authors**: padmark maintainers

## Context

Notes, links and files share one slug space. Three mechanisms kept a slug unique:

1. An in-process lock (`lock.Locker`) that the three managers shared.
2. Pre-checks: each manager called `Exists` on the tables of the other kinds
   (`LinksChecker`, `NotesChecker`, `SlugChecker`) before `Storage.Create`.
3. The `slug_namespace` primary key and the resource triggers. Each INSERT reserves the hashed
   ID in the same transaction, so a slug of another kind fails the INSERT with
   `domain.ErrSlugConflict`.

Only the third mechanism works between server processes. The first two had these problems:

- The lock was held during database calls (create, link update, link reads with a cache), and
  `Lock` ignored context cancellation. One slow query stopped all other requests for that slug.
- Each manager knew the tables of the other kinds. The file feature needed changes in the note
  and link managers (`ExpiredFileReleaser`, `WithExpiredFiles`).
- The rules were different: notes did not check files and never retried a random slug; links
  and files retried five times.
- `Lock` returned an error that never occurred, so six error branches were dead code.

## Decision

1. `Storage.Create` is the only slug uniqueness check. The managers make no check before it and
   hold no lock. The `lock` package is removed.
2. After `domain.ErrSlugConflict`, all three managers apply the same rules:
   - A random slug gets a new random slug, at most five attempts, then `ErrHashCollision`.
   - A custom slug asks one shared port, `ExpiredSlugReleaser`, to remove an expired note,
     link or file that holds the slug. When a record was removed, the manager tries once more.
     Otherwise it returns the conflict.
3. `internal/infra/expiredslugs.Releaser` implements the port for all three managers. It calls
   the conditional `DeleteExpired` of notes and links, then `files.Manager.ReleaseExpiredSlug`,
   which also removes the object. Each removal checks the expiry in its own DELETE, so a record
   that its owner extended stays.
4. The link target cache needs no lock. The cache entry is bound to the ciphertext of the row,
   so a stale entry never matches a new row.
5. `tests/slugs` proves the triggers on SQLite and PostgreSQL: each pair of kinds conflicts at
   the repository, and concurrent creates on separate connection pools have exactly one winner.

## Alternatives

- Keep the pre-checks as a fast path. Rejected: a pre-check cannot replace the INSERT check,
  so it adds a query to each create and a second place to change for each new kind.
- Keep the lock, without database calls under it. Rejected: the lock gave no correctness that
  the triggers do not give, and the cache does not need it.
- A database-backed lock (for example, PostgreSQL advisory locks). Rejected: the primary key
  already serializes the competing INSERTs, and SQLite has no equivalent.
- Read the kind of the holder from `slug_namespace` before the release. Rejected for now: the
  release path runs only after a conflict of a custom slug, and three conditional deletes are
  simple. A slug-kind resolver can replace the order later.

## Consequences

- A new kind of record needs triggers and one branch in `expiredslugs.Releaser`. The other
  managers do not change.
- A random-slug collision costs one failed INSERT and an encryption for the new record ID.
- A read that races a link update can store the old public target in the local cache after the
  eviction. The fingerprint check never serves it, and the next read or the cache time to live
  (30 seconds) replaces it. Before this decision, a link delete and other replicas had the same
  behavior.
- The composition root connects the file manager to the shared releaser after it builds the
  file manager (`Releaser.WithFiles`), because the file manager also uses the releaser.
