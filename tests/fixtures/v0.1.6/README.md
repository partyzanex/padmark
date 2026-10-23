# padmark v0.1.6 upgrade fixtures

`sqlite.sql` and `postgres.sql` are plain-SQL dumps of databases written by a real
**padmark v0.1.6** server (tag `v0.1.6`, commit `d2e5e8f`). `tests/upgrade` loads them into a
fresh database and replays the operator upgrade path: `padmark-server migrate` →
`padmark-server encryption migrate` → `serve`.

v0.1.6 stored everything in plaintext: `notes.id` is the slug itself, `content` is the raw
note body and `edit_code` is the plaintext edit code. The dumps include the `goose_db_version`
rows, so the current migrations apply from the right point:

| Dialect  | Applied v0.1.6 migrations                          |
|----------|----------------------------------------------------|
| SQLite   | `20260327173334`, `20260430163000`, `20260508000001` |
| Postgres | `20260329000001`, `20260430163000`, `20260508000001` |

## Regenerating

Requires `go`, `curl`, `jq`, `sqlite3` and Docker. Run from the repository root:

```sh
scratch=$(mktemp -d /private/tmp/padmark-v016.XXXXXX)
git worktree add --detach "$scratch/src" v0.1.6
(cd "$scratch/src" && go build -o "$scratch/padmark-v016" ./cmd/padmark-server)  # go 1.26.1 module
git worktree remove "$scratch/src"

tests/fixtures/v0.1.6/generate.sh "$scratch/padmark-v016" sqlite   tests/fixtures/v0.1.6/sqlite.sql
tests/fixtures/v0.1.6/generate.sh "$scratch/padmark-v016" postgres tests/fixtures/v0.1.6/postgres.sql
```

`generate.sh` starts the v0.1.6 binary (`serve --storage <dialect> --rate-limit 0`, no
`--auth-tokens`) against an empty database — a temporary SQLite file, or a throwaway
`postgres:18-alpine` container — creates the scenario below through the v0.1.6 HTTP API
(`POST /notes`, `GET /notes/{id}` with `Accept: application/json`), stops the server and dumps
the database:

- SQLite: `sqlite3 <db> .dump`
- Postgres: `pg_dump --inserts --no-owner --no-privileges` inside the container, with psql
  meta-command lines (`\restrict` / `\unrestrict`) removed so the file runs as plain SQL.

Each dump starts with a comment listing `slug edit_code` per note in scenario order.

Slugs, generated edit codes and timestamps are random/time-dependent, so a regenerated dump
differs: update `sqliteFixture()` / `postgresFixture()` in `tests/upgrade/fixture_test.go` from
the new dump header and `INSERT` rows.

## Scenario

v0.1.6 accepted no `expires_at` on create: an expiry only came from the first read of a
`burn_after_reading` note with a `ttl`, which also cleared `burn_after_reading`. Notes 5 and 6
use that path.

| # | Title                   | Create request (v0.1.6)                                   | Then                    | Edit code            | After the upgrade                                         |
|---|-------------------------|-----------------------------------------------------------|-------------------------|----------------------|-----------------------------------------------------------|
| 1 | `1 unicode and dollars` | random slug; markdown starting with `pmc2$p$fake`, unicode, `$`, a valid base64 line | —     | generated (12 chars) | same content; edit code hashed `s1$…`                    |
| 2 | `2 custom slug`         | `slug: my-custom_Slug-1`, `edit_code: hunter2`             | —                       | caller-chosen        | editable with `hunter2`; hashed `v1$…` (argon2id)          |
| 3 | `3 private`             | `content_type: text/plain`, `private: true`                | —                       | generated            | `privacy = authenticated`                                  |
| 4 | `4 burn after reading`  | `burn_after_reading: true`                                 | never read              | generated            | burns on its first read                                    |
| 5 | `5 far future expiry`   | `burn_after_reading: true, ttl: 315360000` (10 years)      | read once               | generated            | keeps its `expires_at` (+10 years)                         |
| 6 | `6 already expired`     | `burn_after_reading: true, ttl: 1`                         | read once, then 3 s wait | generated           | stays expired: deleted on access, never revived            |
| 7 | `7 viewed once`         | content is entirely canonical base64                       | read once (`views = 1`) | generated            | same content, `views = 1`                                  |

### SQLite (`sqlite.sql`)

| # | Slug               | Edit code      | `created_at` (UTC)           | `expires_at` (UTC)           |
|---|--------------------|----------------|------------------------------|------------------------------|
| 1 | `hhyij667uv`       | `rbHkuXQXtZ1t` | `2026-10-23 00:12:31.479833` | —                            |
| 2 | `my-custom_Slug-1` | `hunter2`      | `2026-10-23 00:12:31.499617` | —                            |
| 3 | `82xn2a7pjg`       | `aXdEg4i0pMWF` | `2026-10-23 00:12:31.517539` | —                            |
| 4 | `ew30mehhgc`       | `giy5UADOyaH0` | `2026-10-23 00:12:31.534146` | —                            |
| 5 | `laswfgz6p8`       | `AuoENnsWIUns` | `2026-10-23 00:12:31.550259` | `2036-10-20 00:12:31.602118` |
| 6 | `48fv04c55i`       | `XpwoUj6sTQrK` | `2026-10-23 00:12:31.568828` | `2026-10-23 00:12:32.614163` |
| 7 | `vma86eg2i3`       | `psG26TQ5Rxne` | `2026-10-23 00:12:31.586433` | —                            |

### Postgres (`postgres.sql`)

| # | Slug               | Edit code      | `created_at` (UTC)           | `expires_at` (UTC)           |
|---|--------------------|----------------|------------------------------|------------------------------|
| 1 | `humo6zl1b3`       | `k64ME2eRHxVG` | `2026-10-23 00:12:49.175006` | —                            |
| 2 | `my-custom_Slug-1` | `hunter2`      | `2026-10-23 00:12:49.195397` | —                            |
| 3 | `hlfqtq3hbu`       | `YbCBOeWRdZFq` | `2026-10-23 00:12:49.214388` | —                            |
| 4 | `k18ti00u4c`       | `qX1zEDumDqQk` | `2026-10-23 00:12:49.232445` | —                            |
| 5 | `kbv4rbbyo3`       | `NjU2rDbqn1x1` | `2026-10-23 00:12:49.252975` | `2036-10-20 00:12:49.299821` |
| 6 | `od34bxkz3h`       | `4fbyuOALkCQk` | `2026-10-23 00:12:49.27025`  | `2026-10-23 00:12:50.314342` |
| 7 | `xjr0nb16kk`       | `4lHg2NJHRoWl` | `2026-10-23 00:12:49.286155` | —                            |

The edit codes are fixture data from a throwaway database, published on purpose so the test can
prove each v0.1.6 code still authorizes edits after the upgrade.
