# Go Service Development Guide

Architecture and development conventions for the current Padmark repository. Operational
configuration and upgrades are documented in [README.md](../README.md); browser-test setup
is in [tests/ui/README.md](../tests/ui/README.md). Build both the application and browser-test
modules with Go 1.27.4 or later: it fixes GO-2026-6859 in `net/http`.

## Contents

1. [Clean Architecture](#1-clean-architecture)
2. [Project Structure](#2-project-structure)
3. [Domain Layer](#3-domain-layer)
4. [Usecases Layer](#4-usecases-layer)
5. [Infra Layer](#5-infra-layer)
6. [Adapters Layer](#6-adapters-layer)
7. [CLI and Configuration](#7-cli-and-configuration)
8. [Dependency Injection](#8-dependency-injection)
9. [Error Handling](#9-error-handling)
10. [Metrics](#10-metrics)
11. [Graceful Shutdown](#11-graceful-shutdown)
12. [Testing](#12-testing)
13. [Security](#13-security)
14. [Modern Go](#14-modern-go)
15. [Anti-patterns](#15-anti-patterns)
16. [Deploy](#16-deploy)
17. [Cheatsheet](#17-cheatsheet)

## 1. Clean Architecture

Dependencies point inward: HTTP/CLI/MCP adapters and infrastructure depend on the usecases
and domain, rather than the domain depending on an HTTP server, database or crypto library.

| Layer | Responsibility | Dependencies |
|---|---|---|
| Domain | Entities, validation, ownership and sentinel errors | stdlib and `github.com/google/uuid` for account IDs |
| Usecases | Notes, links, files, accounts, content-key migration/rotation | Domain and consumer-defined interfaces |
| Infrastructure | Storage (SQL and S3, the Simple Storage Service protocol), encryption, rendering, caching, metrics, server composition | Implements usecase interfaces |
| Adapters | HTTP, CLI and MCP protocols | Their own interfaces to usecases |

Interfaces belong beside their consumers. Constructors return concrete types and take
required dependencies as arguments. Add an interface when it supports tests, multiple
implementations or a package boundary; do not add one only to mirror a concrete type.

Dependencies are recorded in `go.mod`/`go.sum`. `vendor/` is ignored and is not committed.
Browser dependencies are isolated in the nested `tests/ui` module.

## 2. Project Structure

```text
padmark/
├── cmd/
│   ├── padmark-server/          # serve, migrate, encryption subcommands
│   ├── padmark-cli/             # HTTP client CLI
│   └── padmark-mcp/             # stdio MCP proxy
├── internal/
│   ├── domain/                 # Notes, links, files, accounts, errors and token envelopes
│   ├── usecases/
│   │   ├── notes/              # Note lifecycle, burn/read policy, rendering coordination
│   │   ├── links/              # Short-link lifecycle and redirect policy
│   │   ├── files/              # File upload, download, update, delete and expiry sweep
│   │   ├── auth/               # Sessions, TOTP, onboarding, invites and API tokens
│   │   └── contentkeys/        # Installation-key migration and rotation
│   ├── infra/
│   │   ├── server/             # CLI configuration, composition and listeners
│   │   ├── storage/            # sqlite/, postgres/, contentkeys/, s3store/ (object storage)
│   │   ├── crypto/             # Hashes, TOTP sealing, keyrings, content envelopes, FileCipher
│   │   ├── render/             # Markdown rendering and sanitization
│   │   ├── rendercache/        # Rendered-note cache
│   │   ├── linkcache/          # Decrypted public-link target cache
│   │   ├── linkmetrics/        # Prometheus link counters
│   │   └── filemetrics/        # Prometheus file counters and upload-size histogram
│   └── adapters/
│       ├── web/                # Handlers, router, templates/, static/, spec/
│       ├── cli/                # CLI commands and HTTP client setup
│       └── mcp/                # MCP tools and HTTP proxy
├── migrations/
│   ├── sqlite/                 # SQL files and embedded FS
│   └── postgres/               # SQL files and embedded FS
├── pkg/client/                 # Generated OpenAPI client; files_stream.go streams files
├── tests/
│   ├── auth/, manager/, mcp/, cli/, encryption/  # Application integration suites
│   ├── files/                 # File life cycle with SQLite and an S3 test container
│   ├── upgrade/, fixtures/     # Real v0.1.6–v0.4.0 upgrade paths and database dumps
│   └── ui/                    # Separate Go module, browser tests and testdata/screenshots/
├── docs/                       # GUIDE.md, adr/ (decision records), design/, plans/
├── openapi.yaml                # API source, embedded into HTTP adapter via generation
├── Makefile                    # Project commands, includes downloaded go.mk
├── Dockerfile                  # Go builder → unprivileged scratch runtime
└── .github/workflows/ci.yml
```

Application unit tests remain beside the packages they test. New browser sources,
dependencies, configuration, tools and output belong under `tests/ui/`.

## 3. Domain Layer

The domain contains no persistence or transport interfaces. Notes use string identifiers;
the public slug is hashed with `domain.HashSlug` for storage. Account ownership uses
`*uuid.UUID`; an anonymous note/link has no owner.

The full models are in [note.go](../internal/domain/note.go),
[link.go](../internal/domain/link.go) and [file.go](../internal/domain/file.go). Relevant rules
include:

- `PrivacyPublic`, `PrivacyAuthenticated`, `PrivacyOwner` determine visibility.
- `OwnedBy` compares account IDs. Any valid session or API token for the creating account
  has the same owner identity; an administrator is not automatically another user's owner.
- `ValidateOwnership` rejects owner-only privacy without an owner.
- `ClearExpiry` is an update instruction, not a persisted column.
- `ShowContents` (the "On this page" sidebar) is a pointer: nil means not set. Create stores
  false; Update sends nil to storage, which keeps the stored column through `COALESCE`, as for
  `Privacy` and `ContentType`.
- Note content and title limits are shared with the OpenAPI contract.
- `domain.File` has the same `Privacy`, `OwnerID`, `ExpiresAt`, `OwnedBy`, `VisibleTo`,
  `Expired`, `Validate` and `ValidateOwnership` rules as a note. `Name` and `ContentKey` are
  set only after decryption: storage keeps them inside `SealedMeta`. `ObjectKey` is the random
  object name in the bucket.
- `CleanFileName` removes a directory part and control characters from a file name.
  `MaxFileNameLength` is 255 bytes.
- File errors are `ErrFileTooLarge` (413), `ErrInvalidFileName` (422) and
  `ErrStorageUnavailable` (503). `ErrFeatureNotSupported` (503) means that files are off.

Sentinel errors live in [errors.go](../internal/domain/errors.go), for example:

```go
var (
    ErrNotFound   = errors.New("not found")
    ErrExpired    = errors.New("expired")
    ErrSlugConflict = errors.New("slug is already taken")
)
```

Keep validation and ownership rules in the domain instead of reimplementing them per adapter.

## 4. Usecases Layer

Business logic lives in managers. Each manager defines the ports it consumes next to the
implementation. For example, the note storage port includes atomic operations used by
burn-after-reading, not just ordinary CRUD:

```go
// Excerpt of notes.Storage; see the full interface in manager.go.
type Storage interface {
    Create(ctx context.Context, note *domain.Note) error
    Get(ctx context.Context, id string) (*domain.Note, error)
    Consume(ctx context.Context, id string) (*domain.Note, error)
    SetBurnExpiry(ctx context.Context, id string, expiresAt time.Time) (*domain.Note, error)
    Update(ctx context.Context, id string, note *domain.Note) error
    Delete(ctx context.Context, id string) error
    IncrementViews(ctx context.Context, id string) error
}
```

[notes.NewManager](../internal/usecases/notes/manager.go) receives storage, a link-namespace
checker, the shared slug lock, a renderer, an encryptor, an edit-code hasher, logger and the
custom-slug setting. Its optional render cache defaults to a no-op implementation.
The link manager follows the same namespace/ownership model.

Notes, links and files share the **same slug-lock instance** to serialize local preflight checks
and coordinate link-cache eviction. Cross-process uniqueness is enforced by the database:
resource triggers atomically reserve the hashed ID in `slug_namespace`, whose primary key
is shared by all three resource types. A losing create returns `ErrSlugConflict` (HTTP 409).
Deleting or consuming a resource releases its reservation in the same transaction.

[files.Manager](../internal/usecases/files/manager.go) uses the same slug lock, the note and
link slug checkers and the edit-code hasher. Its other ports are `Storage` (records),
`ObjectStore` (the bucket), `StreamCipher` (`crypto.FileCipher`), `Encryptor` (the keyring for
`sealed_meta`) and `Metrics`. The default `Metrics` records nothing; `WithMetrics` attaches the
Prometheus implementation.

An upload has this sequence:

1. `Create` cleans the name and hashes the edit code.
2. `upload` makes a random content key. Then it connects the cipher and `ObjectStore.Put`
   through `io.Pipe`. The cipher goroutine encrypts the request body while `Put` sends it, so
   the server keeps no full file in memory.
   A size counter stops the stream at `--max-file-size` with `ErrFileTooLarge`.
3. `sweepOnUpload` removes expired files. A failure is fail-soft: it is only logged.
4. `insert` takes the slug lock only for the record insert, because the upload can be long.
   When the insert fails, the manager deletes the new object.

`Peek` reads a record without access rules. `Info` applies privacy and expiry. `Download`
checks that the object exists before it changes the record, so a missing object does not
consume a `burn_after_download` file. Without `AllowBurn` such a file stays, and `Download`
returns `ErrBurnIntentRequired`. `Update` changes only the expiry, privacy and burn rule.

A burn download first claims the record (`ClaimBurn` sets `burn_claim` and `burn_claim_until`,
70 minutes after the claim). Other readers do not see a claimed record and get 404.
`Download.Close` deletes the record and then the object only after a complete `Send`. After a
failed or canceled transfer, it releases the claim, so the next reader can download the file.
When the server stops during a transfer, the claim stays and the file is locked until
`burn_claim_until`, at most 70 minutes. `Send` stops 5 minutes before that time, so two readers
never get one burn file.

`Delete` (by the user) deletes the record first and the object second; an object delete error
is fail-soft: the manager logs it and increments the `delete` storage error counter.
`SweepExpired` and `ReleaseExpiredSlug` use the opposite order: the object first, then the
record with the same object key. Thus an object delete error keeps the record, and the next
sweep tries again. One sweep removes at most 10 batches of 100 files; the sweep at each upload
removes at most 8. The note and link managers call `ReleaseExpiredSlug`
(`WithExpiredFiles`), so an expired file that the sweep did not remove yet does not block a
new note or link with its slug.

The auth package separates sessions, onboarding, account administration and API-token
operations. Content-key maintenance has its own manager and storage/cipher ports; it must
not make the note manager depend on infrastructure crypto implementations.

## 5. Infra Layer

### Storage and migrations

Production storage implementations are in `internal/infra/storage/sqlite` and `postgres`.
Both use Bun and have concrete constructors such as `NewNoteRepository`,
`NewLinkRepository`, `NewUserRepository` and the file repository. There is no production
in-memory storage backend.

Translate storage errors to domain errors at the repository boundary. Preserve atomic
consume/expiry/update semantics across both backends; do not implement a destructive read
as an unprotected read followed by a separate delete.

SQL migrations and their embedded filesystems live in `migrations/sqlite/` and
`migrations/postgres/`. `serve` applies pending schema migrations; the `migrate` command
applies them and exits. Existing SQLite databases receive an automatic backup before pending
migrations unless opted out. PostgreSQL requires an operator-managed backup.

The `20261025000001_slug_namespace` migration backfills existing native resources. If a
note and a link already share a stored ID, the migration fails without removing either;
resolve that existing collision explicitly before retrying. Tagged v0.1.6 notes reserve
their final hashed IDs when content migration converts them, rather than claiming their
temporary plaintext IDs.

Schema migration and content migration are separate phases that `serve` completes
automatically before listening. The persisted `pending`/`ready` state resumes interrupted
work and skips writes on ready restarts. The optional `encryption migrate` command uses the
same conversion of v0.1.6 plaintext and wrapping of older ciphertext. See
[the upgrade checklist](../CHANGELOG.md#upgrade-checklist-from-v016) before changing that path.

The `20261118000001_files` migration adds the `files` table. It keeps the hashed ID,
`object_key`, `sealed_meta`, privacy, the edit-code hash, size, download count, burn rule,
expiry and owner. The migration adds `files` to the `resource` values of `slug_namespace`, and
the file triggers reserve and release IDs as for notes and links. SQLite cannot change a
`CHECK` constraint, so the SQLite migration builds `slug_namespace` again with its triggers.

### Object storage

[s3store](../internal/infra/storage/s3store/store.go) implements `files.ObjectStore` with
`minio-go` v7. It works with each S3-compatible server. `EnsureBucket` calls `BucketExists` at startup and
`MakeBucket` in `--s3-region` when the bucket is missing; `BucketAlreadyOwnedByYou` from a
parallel start is not an error.
`Put` always uses a multipart upload with 5 MiB parts, because the encrypted size is not known
in advance. Each upload keeps one part in memory. 5 MiB is the S3 minimum part size; with the
S3 limit of 10 000 parts, `s3store.MaxObjectSize` is about 48.8 GiB, so `--max-file-size` is at
most 48 GiB. `Stat` and `WriteTo` return `domain.ErrNotFound` for a missing object. A `HEAD`
response has no body, so `Stat` checks the bucket after a 404: a missing bucket is a storage
error, not a missing file.

The client limits are: a 10-second dial, a 10-second TLS handshake, a 30-second wait for the
response headers and 3 attempts. There is no total request timeout, because a large transfer
can be long. Error texts replace the object key with `<object-key>`, because the logs must not
show where a file is. The endpoint must be `scheme://host[:port]`: `New` refuses user info, a
path, a query and a fragment. A plain `http://` endpoint on a host other than `localhost` or a
loopback IP stops startup, unless the option `AllowInsecureHTTP` (`--s3-allow-insecure`) is
set; then the server logs a warning. `--s3-path-style=false` selects `minio.BucketLookupDNS`,
because `minio.BucketLookupAuto` selects the virtual host style only for Amazon, Google and
Aliyun.

`minio-go` cancels a failed multipart upload, but it ignores the result of the cancel request.
The README tells the operator to add an `AbortIncompleteMultipartUpload` lifecycle rule.

### Rendering and caches

`internal/infra/render` combines Goldmark, Chroma and Bluemonday. Plain-text notes use a
separate escaping/rendering path through the same renderer port.

Rendered notes are cached by hashed ID and update time; burn-after-reading notes bypass the
cache. Short-link caching reuses only decrypted public targets: metadata and access/expiry
checks still read current database state, including changes made by another replica.

## 6. Adapters Layer

### HTTP

The HTTP adapter defines its own `NoteManager`, `LinkManager` and auth interfaces.
`NewHandler(manager, linkManager, log)` receives those interfaces rather than concrete
usecase managers. `NewRouter(handler, ogenHandler, *RouterOptions)` returns `(http.Handler, error)`.
Tests without a database pinger use `NoPinger{}`.

`http/middleware/` owns authentication, CSRF/cookies, request context, proxy detection,
rate/guessing limits, security headers, logging and recovery. The router composes the chain;
middleware never imports the parent adapter. `http/response/` supplies the shared JSON error
format without introducing a dependency cycle. Resource privacy remains in the handlers/usecases.

`LinkHandler` serves the creation form at `/links`, the link page at `GET /links/{id}` and
redirects at `/r/{id}`. `Handler.GetNoteOrLink` serves the bare `/{id}`:
it reads the note first and calls the link redirect only when no note has the slug. Editor reads
and saves do not consume burn links.

Notes, short links and files have one user flow. The create form goes to `/success`; **Open**
goes to the page of the record; **Edit** on that page goes to `/edit/{id}`; **Save** goes back to
the page of the record. The page of a note or a file is `/{id}`. The page of a short link is
`/links/{id}`, because `/{id}` of a link is the redirect: `htmlOr` sends a client that prefers
`text/html` to `LinkHandler.LinkPage` and other clients to the ogen JSON read. The link page uses
`Peek`, so it does not count a view or burn the link, and it applies the privacy and expiry checks
of the JSON read. `Handler.EditPage` serves `/edit/{id...}` for the three kinds: it reads the note,
then the short link, then the file, like `GetNoteOrLink`. The old URLs `/links/{id}/edit` and
`/files/{id}/edit` answer `301` to `/edit/{id}`. `headerView.Record` sets the target of the
**New** button of a reader page to the create form of the same kind. With `--disable-api`,
`isLinkPage` and the `isLinkPageRequest` matcher keep the link pages open, as `isFilePage` keeps
the file pages.
The `/api/openapi.yaml` response uses the same build version as `padmark-server --version`;
release container builds inject the Git-tag version through the `APP_VERSION` build argument.

The generated OpenAPI handlers serve single-segment API paths; handwritten handlers also
support path-like note slugs. Keep their status codes, validation and ownership behavior
aligned. Edit codes for deletion come from `X-Edit-Code`; the deprecated `edit_code` query
parameter is a fallback for v0.1.6 clients only (`deleteEditCode`).

`FileHandler` serves all `/files` routes by hand, not through ogen
([ADR-0002](adr/0002-files-outside-ogen.md)):

| Route | Use |
|---|---|
| `GET /files` | Upload page (`file_editor.html`): drop area, `XMLHttpRequest` progress, cancel |
| `POST /files` | Multipart upload; the text fields must come before the `file` part |
| `GET /files/{id}` | Metadata in JSON; no download count |
| `GET /files/{id}/download` | Content as an attachment; consumes a burn file only for a non-browser client |
| `POST /files/{id}/download` | Download form of the file page for a burn file; CSRF guard and same-origin check |
| `GET /files/{id}/edit` | Old URL of the edit page: `301` to `/edit/{id}`, where `file_edit.html` changes expiry, privacy and burn rule, or deletes the file |
| `PUT /files/{id}` | JSON `ttl`, `privacy`, `burn_after_download`; edit code in `X-Edit-Code` |
| `DELETE /files/{id}` | Delete; edit code in `X-Edit-Code` or the owner |

`POST`, `PUT` and `DELETE` have `RequireSameOrigin`; `PUT` and `DELETE` also have the edit-code
lockout. The upload has its own body limit (`--max-file-size` plus 1 MiB for the form). A download
sends `application/octet-stream`, `Content-Disposition` with an ASCII name and
`filename*=UTF-8''…`, `nosniff` and `Cache-Control: private, no-store`. Without a file manager,
the API routes answer 503 and the pages show the "files are off" error. `--disable-api` keeps
only the `GET`/`HEAD` pages `/files`, `/files/{id}/edit` and `/files/{id}/download`, and the
download form `POST /files/{id}/download` (`isFilePage`).

`file_transfer.go` sets the limits of a transfer (`FileTransferLimits`, set by
`WithFileTransferLimits`). Uploads and downloads do not use `--read-timeout` and
`--write-timeout`. `transferDeadline` gives each transfer an idle deadline
(`--file-idle-timeout`, 60 seconds by default). Each chunk of data moves the deadline forward,
but never past a total limit of one hour from the start (`fileTransferTimeout`). An upload
that stops for the idle time answers 408. `uploadLimiter` accepts at most `--max-uploads`
uploads (8 by default) at the same time; more get 503. One client IP address (`ClientIP`,
which obeys `--trusted-proxies`) gets at most `--max-uploads-per-client` (4 by default); more
get 429. Both answers have `Retry-After: 10`. Each upload holds one 5 MiB part buffer, so the
limit also limits memory. When `Send` fails after the first byte, the handler panics with
`http.ErrAbortHandler`. `WithRecovery` panics again with that value, so `net/http` closes the
connection without an error log record and the client sees a broken transfer.

`GetNoteOrLink` calls `serveFileIfExists` for a single-segment slug. `wantsFilePage` sends a
browser to the file page (`file.html`), which has the article layout of the note page. Other
clients, `*/*` included, get the content. The success page has the kind `file`, and the header
shows the **File** tab only when files are on.

`domain.ReservedSlugPrefixes()` is the source of truth for named first-segment routes.
Registering a new prefix without reserving it can send that route into note resolution.

### Content negotiation

`negotiate(r)` ranks supported `Accept` formats by quality, specificity and header order.
JSON is the default for a missing/wildcard header; `?raw=1` selects raw content. Do not
replace the parser with substring checks.

Burn reads additionally require deliberate intent. Missing or wildcard-only `Accept`, `HEAD`
and browser subresource requests do not consume a note/link. Browser note reads first show
a confirmation form. A raw-query flag alone does not prove intent.

A file download has stricter rules (`explicitDownload`). A browser never consumes a
`burn_after_download` file with a `GET`. A `GET` with a `Sec-Fetch-Site` header, a prefetch
(`Sec-Purpose`, `Purpose` or `X-Moz`) or an `Accept` that prefers HTML keeps the file. A
browser downloads such a file only through the `POST /files/{id}/download` form of the file
page, which has the CSRF guard. When a browser sends a `GET` for such a file, the server
redirects it with 303 to the file page; other clients get 409. A non-browser client consumes
the file with a `GET` and an exact `Accept: application/octet-stream`, at `/{id}` or at
`/files/{id}/download`. An exact non-HTML format that `explicitRead` accepts, such as
`application/json`, also counts.

### CLI and MCP

`internal/adapters/cli` and `mcp` are clients of the HTTP API; they do not open application
databases. Both accept the API-token envelope format and use the same token/URL resolution
rules. `/llms.txt` is an embedded navigation index linking to `/api/reference.md` (the
detailed API guide) and `/api/openapi.yaml` (schemas). All three describe the same build;
update the reference with API behavior and keep the index's safety guidance current.

The CLI file commands are `upload`, `download`, `file-info`, `update-file` and `delete-file`.
The CLI has one generated client for its one server. Its HTTP client has no total timeout:
`--timeout` sets only the wait for the response headers. The other commands get the full-call
limit from a context deadline (`requestContext`), after they read their local input.
`upload` and `download` use `UploadFile` and `DownloadFile` of `pkg/client` without that
deadline, so a large file is not stopped. `download` writes to a temporary file and renames it
after a full download.

The MCP file tools are `upload_file`, `get_file`, `update_file` and `delete_file`.
`upload_file` reads only regular files under `--upload-root` (`PADMARK_MCP_UPLOAD_ROOT`); an
empty root refuses all uploads. A lexical root check runs before each file system call, and each
path outside the root gets the one error `ErrPathOutsideRoot`. Names that start with a dot are
always refused, before and after link resolution. `upload_file` sends the agent `slug` as
`create_note` and `create_link` do. Only the server flag `--custom-slugs` can refuse it.
With a token, the default privacy of an upload is `owner`. The session vault keeps file edit
codes as for notes and links. `mcp.NewManager` takes one generated client through the
`PadmarkClient` seam. Its HTTP client uses `--timeout` only for the response headers. Each tool
other than `upload_file` gets a context deadline of `--timeout` for the full call (`limitCall`).

## 7. CLI and Configuration

The server command is assembled in `internal/infra/server/app.go` with `serve`, `migrate`
and `encryption` subcommands. Omitting the subcommand still runs `serve`.

`cmd/padmark-server/main.go` calls `server.NewApp().Run(ctx, os.Args)` and reports an error.
Keep composition and operational logic in `internal/infra/server/`, not in `main`.

`flags.go` groups `Flag*`, `Env*` and `Default*` constants. Most flags have a `PADMARK_*`
environment source; the bootstrap token is environment-only. Consult
[the configuration table](../README.md#configuration-reference) and [.env.example](../.env.example).

For file-backed SQLite, an unset keyring path resolves to `padmark-keys.json` beside the DB.
PostgreSQL defaults to the working directory. Only `serve` creates missing default files.
Explicit paths must exist, and in-memory/temporary SQLite requires an explicit keyring;
encryption tools never create one.

`--disable-api` disables the REST endpoints, API docs and `/llms.txt`. The editor becomes
read-only because publish/save use the REST endpoints. Browser note views, link redirects,
account/admin forms and health endpoints remain available. The file pages and the download
stay available; upload, change and delete are off.

The file flags are in `fileStorageFlags()`:

| Flag | Environment variable | Default |
|---|---|---|
| `--s3-endpoint` | `PADMARK_S3_ENDPOINT` | empty: files are off |
| `--s3-bucket` | `PADMARK_S3_BUCKET` | none; necessary with `--s3-endpoint` |
| `--s3-region` | `PADMARK_S3_REGION` | `us-east-1` |
| `--s3-path-style` | `PADMARK_S3_PATH_STYLE` | `true` |
| `--s3-allow-insecure` | `PADMARK_S3_ALLOW_INSECURE` | `false`; permits plain `http://` to a host that is not loopback |
| `--max-file-size` | `PADMARK_MAX_FILE_SIZE` | `104857600` (100 MiB); maximum 48 GiB |
| `--files-sweep-interval` | `PADMARK_FILES_SWEEP_INTERVAL` | `1h`; `0` turns the sweep loop off |
| `--file-idle-timeout` | `PADMARK_FILE_IDLE_TIMEOUT` | `1m`; `0` selects the default |
| `--max-uploads` | `PADMARK_MAX_UPLOADS` | `8`; `0` selects the default |
| `--max-uploads-per-client` | `PADMARK_MAX_UPLOADS_PER_CLIENT` | `4`; `0` selects the default |

The S3 keys come only from `PADMARK_S3_ACCESS_KEY` and `PADMARK_S3_SECRET_KEY`. There are no
flags for them, because `ps` shows flags. The MCP proxy reads `--upload-root` /
`PADMARK_MCP_UPLOAD_ROOT`. The server flag `--custom-slugs` controls file slugs, as for notes
and links.

## 8. Dependency Injection

Manual wiring lives in [server.go](../internal/infra/server/server.go), chiefly
`serverAction`, `buildHandlers` and `buildRouter`:

1. Validate startup configuration/secrets and initialize logging.
2. Load the keyring, open storage, apply schema migrations and validate content encryption.
3. Construct repositories, ciphers, renderer, shared slug lock, caches and metrics.
4. Build note/link/auth managers and inject them into the HTTP handlers/router.
   `attachFiles` in [files.go](../internal/infra/server/files.go) builds `files.Manager` only
   when `--s3-endpoint` is set. A set but wrong S3 configuration stops startup (fail-hard).
   Without the endpoint, the server logs `file publishing is off`; other S3 settings without
   the endpoint give a warning with their names.
5. Start configured listeners and register shutdown/cleanup work.

Required dependencies use constructors; optional facilities can use explicit `With...`
methods or options. Use mocks/no-op implementations in tests instead of adding nil checks
around ordinary production operations.

## 9. Error Handling

Wrap errors with the operation while preserving the chain:

```go
return fmt.Errorf("create note: %w", err)
```

Repositories translate SQL errors into domain sentinels. HTTP's `domainErrStatus` maps
sentinels to status/message pairs; JSON, HTML and ogen responses derive from that mapping.
Do not duplicate status tables or return raw wrapped infrastructure errors to clients.

The machine error shape is `{"message": string, "code"?: string}`. Stable middleware codes
such as `api_disabled`, `burn_read_not_explicit` and `step_up_required` distinguish causes
that share an HTTP status. Unexpected internal errors are logged and return a generic message.

## 10. Metrics

[linkmetrics](../internal/infra/linkmetrics/metrics.go) implements the link manager's metrics
port. `NewRegistered(reg)` registers these counters with a supplied Prometheus registry:

- `padmark_links_created_total`
- `padmark_links_redirects_total`
- `padmark_links_collisions_total`

[filemetrics](../internal/infra/filemetrics/metrics.go) implements `files.Metrics` with the
same `New()` and `NewRegistered(reg)` pattern. The server registers it also when files are off:

- `padmark_files_uploads_total`
- `padmark_files_downloads_total`
- `padmark_files_deleted_total` with the label `reason`: `user`, `burn` or `expired`
- `padmark_files_upload_bytes`, a histogram with the bounds 1, 4, 16, 64 and 256 MiB and 1 GiB
- `padmark_files_storage_errors_total` with the label `op`: `put`, `stat`, `get` or `delete`

The separate `--metrics-addr` listener is disabled by default. It also exposes standard
Go/process collectors; metrics are not served on the public HTTP listener.
Use mocks with `.AnyTimes()` or a no-op metrics implementation when metric call counts are
not the subject of a test.

## 11. Graceful Shutdown

The server registers listener shutdown with `github.com/partyzanex/shutdown` and releases
its infrastructure resources. Keep shutdown bounded and handle `http.ErrServerClosed` as
the normal outcome of closing an HTTP server.

`--shutdown-timeout` (10 seconds by default) limits each shutdown phase. The shared manager
skips the closers that remain after its deadline, so work that must stop before the database
closes does not use it. `serverAction` makes a second LIFO manager for background work and
closes it through a deferred `stopBackground` call, before the deferred `closeServerDB` call.
`stopBackground` has its own deadline, so it runs also when a long file transfer kept
`srv.Shutdown` busy for the full time.

The file sweep loop (`startFileSweep`) adds a close function to that background manager. The
close function cancels the loop and waits for the current sweep until its deadline. The loop
sweeps once at startup and then at each tick. A sweep error is fail-soft: the loop logs it and
tries again at the next tick. A panic in a sweep is logged at the error level, and the loop
continues.

Background workers should make repeated close calls safe, for example with `sync.Once`.
Do not hold a mutex while waiting for network I/O, and do not store `context.Context` in
long-lived structs: pass a context to each operation instead.

## 12. Testing

### Application unit and integration tests

Unit tests live beside their application packages and use `testify/suite`, `require` and
`go.uber.org/mock`. Generate mocks from the consumer interface's actual directive; an HTTP
mock may target `web_test` rather than the implementation package.

Application integration suites live in `tests/auth`, `cli`, `files`, `manager`, `mcp`,
`encryption` and `upgrade`, with `//go:build integration`. PostgreSQL repository tests use that
tag too. SQLite suites can use temporary/in-memory databases; PostgreSQL suites use
testcontainers and need Docker. Upgrade suites load dumps produced by real released binaries,
from v0.1.6 to v0.4.0.

File tests need an S3 server. They start the container `adobe/s3mock:3.12.0` through
testcontainers, so they need Docker too. These tests use it:

- `internal/infra/storage/s3store`: the adapter against a real S3 API.
- `tests/files`: upload, `GET /{id}`, download, update and delete through HTTP; a server
  restart; a content key rotation.
- `tests/manager`: the file suite for SQLite and PostgreSQL.
- `tests/cli` and `tests/mcp`: the file commands and tools against a real server.

`tests/upgrade` checks the v0.4.0 to v0.5.0 upgrade with the real v0.4.0 dumps; it needs no S3
server. Unit tests of `files.Manager` and `FileHandler` use mockgen mocks and no container.

Use `t.Context()` / `s.T().Context()`, clean state between tests and use polling assertions
for asynchronous behavior. Do not store a context in a suite field or share mutable test data.

```bash
go test ./...                           # Root module, unit tests
make test                               # Root module, unit + integration, -race and coverage
go test -tags=integration ./tests/... ./internal/infra/storage/postgres
go generate ./...                       # Run checked-in generation directives
```

### Browser tests

`tests/ui/` is a separate Go module using playwright-go and the standard `testing` runner.
It owns its `go.mod`, `go.sum`, Makefile, screenshots, driver/browser cache and reports.
Root `go test ./...` and `make test` do **not** include it.

```bash
make test-ui-setup
make test-ui
make test-ui-headed
make test-ui UI_ARGS='-run TestEditor/desktop/Toolbar'
make test-ui UI_ARGS='-race'
```

The scenarios run at desktop/mobile sizes. Each gets a real server, isolated
SQLite/keyring and browser context. Uncaught JavaScript, console errors and failed resources
fail the case. Layout and PNG assertions detect visual changes. Only deliberately expected
HTTP errors are exempted, and missing static resources always fail.

`make test-ui-update` updates reference PNGs after an intentional design change; review the
diffs. `make test-ui-report` opens the report, and `make test-ui-debug` uses Playwright Inspector.
A separate npm/Node installation is not required: the Go binding downloads its internal
Node.js driver. See [the browser guide](../tests/ui/README.md) for authoring and artifacts.

### CI

[ci.yml](../.github/workflows/ci.yml) runs lint, unit tests, integration tests and the browser
module. Browser tests use a pinned Linux Playwright image matching the Go binding and upload
their report/failure artifacts. Docker publishing waits for all those jobs.

## 13. Security

### Secrets and stored content

- Keep real secrets out of source, logs and committed configuration. `.env` is ignored and
  used by `make run`; `.env.example` documents sample values. Isolated test credentials and
  published upgrade-fixture codes are not production secrets.
- Prefer environment variables, stdin and protected files to secret-valued flags visible
  in process listings. Token envelopes contain the raw token and are secrets too.
- Store and back up the installation keyring separately from the database. All replicas
  sharing data need the same keyring and a shared CSRF secret for interchangeable forms.
- Notes and link targets use installation-key content envelopes. Generated edit codes use
  salted SHA-256; caller-chosen codes use the configured argon2id cost. Verification uses
  recorded parameters within the accepted ceiling.
- File content uses the `pmf1` stream format with a random key for each file
  ([ADR-0001](adr/0001-file-encryption-format.md)). `files.sealed_meta` keeps that key and the
  name in a keyring envelope of the `file` resource, so key rotation rewraps files without a
  change to the objects. Object keys are random UUIDs (universally unique identifiers).
- Rotation does not change the content key of a file. An old keyring with an old database copy
  still decrypts the objects; after a keyring leak, upload the files again (ADR-0001). Size,
  times, owner, privacy, download count and object key stay in clear.
- `encryption prune --corrupt` never deletes a file record. A file record with unversioned
  `sealed_meta` is `corrupt_file`: it blocks `serve` and prune, and needs manual work.
- A download is always an attachment of type `application/octet-stream` with `nosniff`, so a
  browser never runs an uploaded HTML or SVG file. `attachmentDisposition` gives an ASCII name
  and a UTF-8 `filename*` name, as RFC 6266 recommends. A quote, a semicolon, a backslash or a
  line break in a name cannot add a parameter or a header.
- Logs do not contain file names, object keys, edit codes or S3 keys. `s3store` removes the
  object key from error texts.
- Stop writers before schema/content migration and key rotation. See
  [key management](../README.md#content-key-management-migration-and-rotation) for the supported
  order, legacy plaintext conversion, corrupt-record diagnostics and backup handling.

### Account tokens and ownership

An admin can issue a key for their own account from `/admin`; account API endpoints and the
CLI also support issuance. Bearer callers issuing keys/invites must re-authenticate with the
account password and a fresh TOTP code. A key issued by another key is revoked with its issuer;
password changes revoke the account's keys and other sessions. `/admin` requires a browser session.

The envelope is `pmk_` plus unpadded base64url JSON `{"url":"...","token":"..."}`.
The domain codec is shared by adapters; only the extracted raw key is sent in the Bearer
header. An explicit `--url` / `PADMARK_URL` overrides an embedded URL.

CLI token precedence is flag, `PADMARK_TOKEN`, then the config token file
(`~/.config/padmark/token`, honoring `XDG_CONFIG_HOME`). Keep that directory/file at
`0700`/`0600`. Non-HTTPS, non-loopback credential transmission is refused without
`--allow-insecure-http`; loopback HTTP produces a warning.

Ownership follows the account ID, not a particular session or token. Other users still need
the edit code and permission to read the record; an admin role alone grants no owner bypass.
Deletion uses `X-Edit-Code`. Anonymous edit-code lockout is per record and client IP (/64 for
IPv6), while authenticated attempts are per record and account ID.

## 14. Modern Go

The build toolchain is Go 1.27.4. Use `for range N`, `t.Context()` / `s.T().Context()` and
`sync.WaitGroup.Go` where they fit; use `log/slog` for structured logging. Run and review
`go fix ./...` when upgrading the toolchain rather than rewriting code mechanically by hand.

CI pins golangci-lint v2.15.0. [.golangci.yml](../.golangci.yml) controls exclusions,
line length, complexity and build tags. Its current `run.go: '1.25'` analysis setting is
separate from the build toolchain in `go.mod`.

## 15. Anti-patterns

- Storing `context.Context` in service, repository or suite fields.
- Making usecases import concrete SQL/crypto/HTTP implementations.
- Reusing another adapter's interface instead of defining a consumer port.
- Returning interfaces from constructors only to hide a single implementation.
- Creating interfaces without a testing, implementation or package-boundary need.
- Adding repeated nil checks for required dependencies instead of injecting them.
- Holding a mutex across network I/O.
- Duplicating privacy, burn, error-status or reserved-route rules across adapters.
- Adding browser manifests, downloads or artifacts to the repository root.

## 16. Deploy

Use the checked-in [Dockerfile](../Dockerfile) and [Compose file](../docker-compose.yml),
not a second build recipe in this guide. The Dockerfile builds the server with
`golang:1.27.4-alpine` and `CGO_ENABLED=0`, then runs it as UID/GID 65534 in scratch.
It contains no shell, CLI/MCP executable or browser-test environment.

The container works in `/data`; persist that volume for its generated keyring and SQLite data.
An explicitly configured keyring must already exist and be readable by the service UID.
Compose uses PostgreSQL with `db_data` and `content_keys` named volumes and requires
`POSTGRES_PASSWORD`. The container gets only the variables in the `environment` list of the
`padmark` service; the list passes `PADMARK_ENABLE_ACCOUNTS` (default `false`) and, when set,
`PADMARK_BOOTSTRAP_TOKEN` from `.env`. Startup migrates schema/content without commands inside
the container.
Retain the same keys on every restart/replica; stop old-version writers during the upgrade.

File publishing needs an S3 bucket and a key with `s3:ListBucket` on the bucket and
`s3:PutObject`, `s3:GetObject`, `s3:DeleteObject` and `s3:AbortMultipartUpload` on its
objects. When the bucket does not exist, the server creates it; that needs `s3:CreateBucket`. The Compose file starts RustFS, waits for its health check and sets `PADMARK_S3_*`,
with `PADMARK_S3_ALLOW_INSECURE=true` for the internal `http://s3:9000`; the keys come from
`.env` and are also the RustFS root keys. A reverse proxy must accept bodies up to
`--max-file-size` plus 1 MiB and must permit a transfer of up to one hour; padmark stops a
transfer that moves no data for `--file-idle-timeout`. See [Files setup](../README.md#files-setup)
for the policy, the lifecycle rule and an nginx example. Back up the database and the keyring separately, and back up the bucket too.

Schema/content maintenance and container upgrades need consistent backups. A PostgreSQL
data directory cannot be opened by an older major version. Consult the
[v0.1.6 checklist](../CHANGELOG.md#upgrade-checklist-from-v016) before upgrading existing data.

## 17. Cheatsheet

```bash
make build
make test                        # Application unit/integration; Docker required
make lint
make test-ui-setup
make test-ui                     # Separate browser module
make test-ui-headed
make test-ui-report
```

| Component | Implementation |
|---|---|
| CLI / logging / HTTP router | urfave/cli/v3 / log/slog / net/http ServeMux |
| HTTP contract | ogen + openapi.yaml |
| Markdown / sanitization | Goldmark + Chroma / Bluemonday |
| Persistence | Bun + pgdriver or modernc SQLite; embedded goose SQL migrations |
| File content | minio-go v7 (S3 API); AES-256-GCM `pmf1` stream |
| Content / account secrets | Installation-key AES-GCM envelopes; argon2id; TOTP |
| Unit/integration tests | testify, gomock, testcontainers, `adobe/s3mock` |
| Browser tests | playwright-go, Go testing/require, Go pixelmatch |
