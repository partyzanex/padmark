# ADR-0004: The note read gives the kind of another record

**Status**: Accepted
**Date**: 2026-11-22
**Authors**: padmark maintainers

## Context

Notes, short links and files share one slug space. The `slug_namespace` table stores the
resource (`notes`, `links`, `files`) of each hashed ID. `GET /{id}` and `/edit/{id}` must find
which kind of record a slug names, then apply the rules of that kind.

Notes are the most frequent records. The owner requires one database read for a note view and
for the note editor. Short links, files and missing slugs must not need more reads than before.

These designs were used or considered before this decision:

1. Read the note, then the link, then the file, one after the other. A file page needed four
   reads and two decryptions, and the order of kinds was a rule inside the web adapter.
2. Read the kind from `slug_namespace` first (`slugs.Resolver`, commit b4bb8df), then call the
   Manager of that kind. Links, files and missing slugs became cheaper, but a note view needed two
   reads instead of one.

## Decision

The note read of both dialects (`notes.Storage.Get`) is one query. It selects the hashed ID,
joins `notes` and joins `slug_namespace`:

- When a note has the ID, the query returns the note.
- When a short link or a file has the ID, the read returns `*domain.OtherKindError` with that
  kind. The error matches `domain.ErrNotFound` with `errors.Is`.
- When no record has the ID, the read returns `domain.ErrNotFound`.

The web dispatch reads the note first. On `OtherKindError` it calls only the Manager of the kind
in the error (`internal/adapters/web/note_or_link.go`). `slugs.Resolver` and its repositories
are removed.

`/notes/{id}` and other note-only routes answer a link or file slug as a missing note, so no
client sees the kind of another record.

Reads for each page, measured on SQLite and PostgreSQL by `tests/slugs/reads_test.go`:

| Page | Reads |
|---|---|
| Note view, every format | 1 |
| Note editor | 1 |
| Burn note confirmation page | 2 |
| Short link page or redirect | 2 |
| File page or content | 2 |
| Missing slug | 1 |

## Alternatives

- **Resolver first (design 2 above).** Rejected: a note view needs two reads.
- **Note first, then the resolver on a miss.** Rejected: a link or file page needs three reads,
  and a missing slug needs two.
- **One combined query for every kind.** Rejected: one query would return columns of three
  tables, and the notes storage would then build links and files.

## Consequences

- A note view and the note editor need one database read.
- Every note `Get` joins `slug_namespace`, also for update, delete and the burn re-read, which do
  not need the kind. The join is one primary-key lookup in the same round trip, not an extra
  query.
- The notes storage reports facts about other record kinds, and the web dispatch depends on an
  error type (`domain.OtherKindError`). Code that asks only for a note sees `ErrNotFound`, so it
  does not change.
- A note of v0.1.6 without a `slug_namespace` row is still found: the join to `notes` does not
  depend on that row. A contract test checks this on both dialects.
- A new record kind needs a new `RecordKind` value, a case in `domain.NoteNotFound` and a case in
  the web dispatch.
