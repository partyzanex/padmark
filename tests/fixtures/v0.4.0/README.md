# padmark v0.4.0 upgrade fixtures

`sqlite.sql` and `postgres.sql` are plain-SQL dumps of databases written by a real
**padmark v0.4.0** server (tag `v0.4.0`, commit `59acd86`). `tests/upgrade` loads them into a
fresh database and starts the current server on them. The server applies the v0.5.0 schema
migration `20261118000001_files` at startup. The test then checks that the notes, the links and
the slug namespace stay the same and stay readable.

v0.4.0 stored notes and links as native `pmc2$p$…` envelopes, so the dumps need a keyring.
`generate.sh` starts the server with the fixed, public test keyring of
`internal/testutil.ContentKeyring` (key ID `test`). The upgrade test writes the same keyring.
Never use this keyring in a real installation.

## Regenerating

Requires `go`, `curl`, `jq`, `sqlite3` and Docker. Run from the repository root:

```sh
scratch=$(mktemp -d)
mkdir "$scratch/src"
git archive v0.4.0 | tar -x -C "$scratch/src"
(cd "$scratch/src" && go build -buildvcs=false -o "$scratch/padmark-v040" ./cmd/padmark-server)

tests/fixtures/v0.4.0/generate.sh "$scratch/padmark-v040" sqlite   tests/fixtures/v0.4.0/sqlite.sql
tests/fixtures/v0.4.0/generate.sh "$scratch/padmark-v040" postgres tests/fixtures/v0.4.0/postgres.sql
```

`generate.sh` starts the v0.4.0 binary (`serve --storage <dialect> --rate-limit 0`, no accounts)
on an empty database: a temporary SQLite file, or a throwaway `postgres:18-alpine` container.
It makes the records below through the v0.4.0 HTTP API (`POST /notes`, `POST /links`), stops the
server and dumps the database:

- SQLite: `sqlite3 <db> .dump`
- Postgres: `pg_dump --inserts --no-owner --no-privileges` inside the container, without the psql
  meta-command lines (`\restrict` / `\unrestrict`), so the file runs as plain SQL.

Each dump starts with a comment that lists `id edit_code` for each record in scenario order.
Random IDs and generated edit codes change on each run: copy them into `v04Fixtures()` in
`tests/upgrade/v04_startup_test.go`.

## Scenario

| # | Kind | Create request (v0.4.0)                                            | After the upgrade                 |
|---|------|--------------------------------------------------------------------|-----------------------------------|
| 1 | note | `slug: v040-unicode`, `edit_code: fixture-code-1`, unicode Markdown | same content; the edit code works |
| 2 | note | random slug, `content_type: text/plain`                            | same content                      |
| 3 | note | `slug: v040-burn`, `burn_after_reading: true`                      | burns on its first read           |
| 4 | note | `slug: v040-ttl`, `ttl: 315360000` (10 years)                      | same content and `expires_at`     |
| 5 | note | `slug: docs/v040-nested` (two path segments)                       | same content                      |
| 6 | link | `slug: v040-link`, `edit_code: fixture-link-1`                     | same target; the edit code works  |
| 7 | link | random slug                                                        | same target                       |
| 8 | link | `slug: v040-click`, `burn_after_click: true`                       | same target                       |

The edit codes are fixture data from a throwaway database. The test uses them to prove that a
v0.4.0 code still authorizes changes after the upgrade.
