# Go Service Development Guide

Architecture and development conventions for the current Padmark repository. Operational
configuration and upgrades are documented in [README.md](../README.md); browser-test setup
is in [tests/ui/README.md](../tests/ui/README.md). The application and browser-test modules
both require Go 1.27.3.

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
| Usecases | Notes, links, accounts, content-key migration/rotation | Domain and consumer-defined interfaces |
| Infrastructure | Storage, encryption, rendering, caching, server composition | Implements usecase interfaces |
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
│   ├── domain/                 # Notes, links, accounts, errors and token envelopes
│   ├── usecases/
│   │   ├── notes/              # Note lifecycle, burn/read policy, rendering coordination
│   │   ├── links/              # Short-link lifecycle and redirect policy
│   │   ├── auth/               # Sessions, TOTP, onboarding, invites and API tokens
│   │   └── contentkeys/        # Installation-key migration and rotation
│   ├── infra/
│   │   ├── server/             # CLI configuration, composition and listeners
│   │   ├── storage/            # sqlite/, postgres/, contentkeys/
│   │   ├── crypto/             # Hashes, TOTP sealing, keyrings and content envelopes
│   │   ├── render/             # Markdown rendering and sanitization
│   │   ├── rendercache/        # Rendered-note cache
│   │   ├── linkcache/          # Decrypted public-link target cache
│   │   └── linkmetrics/        # Prometheus link counters
│   └── adapters/
│       ├── http/               # Handlers, router, templates/, static/, spec/
│       ├── cli/                # CLI commands and HTTP client setup
│       └── mcp/                # MCP tools and HTTP proxy
├── migrations/
│   ├── sqlite/                 # SQL files and embedded FS
│   └── postgres/               # SQL files and embedded FS
├── pkg/client/                 # Generated OpenAPI client/server types
├── tests/
│   ├── auth/, manager/, mcp/, encryption/  # Application integration suites
│   ├── upgrade/, fixtures/     # Real v0.1.6 upgrade path and database dumps
│   └── ui/                    # Separate Go module, browser tests and testdata/screenshots/
├── docs/
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

The full models are in [note.go](../internal/domain/note.go) and
[link.go](../internal/domain/link.go). Relevant rules include:

- `PrivacyPublic`, `PrivacyAuthenticated`, `PrivacyOwner` determine visibility.
- `OwnedBy` compares account IDs. Any valid session or API token for the creating account
  has the same owner identity; an administrator is not automatically another user's owner.
- `ValidateOwnership` rejects owner-only privacy without an owner.
- `ClearExpiry` is an update instruction, not a persisted column.
- Note content and title limits are shared with the OpenAPI contract.

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

Notes and links share the **same slug-lock instance** to serialize local preflight checks
and coordinate link-cache eviction. Cross-process uniqueness is enforced by the database:
resource triggers atomically reserve the hashed ID in `slug_namespace`, whose primary key
is shared by both resource types. A losing create returns `ErrSlugConflict` (HTTP 409).
Deleting or consuming a resource releases its reservation in the same transaction.

The auth package separates sessions, onboarding, account administration and API-token
operations. Content-key maintenance has its own manager and storage/cipher ports; it must
not make the note manager depend on infrastructure crypto implementations.

## 5. Infra Layer

### Storage and migrations

Production storage implementations are in `internal/infra/storage/sqlite` and `postgres`.
Both use Bun and have concrete constructors such as `NewNoteRepository`,
`NewLinkRepository` and `NewUserRepository`. There is no production in-memory storage backend.

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

`LinkHandler` serves the creation form at `/links`, the metadata-only editor at
`/links/{id}/edit`, and redirects at `/r/{id}`. Editor reads and saves do not consume burn links.
The `/api/openapi.yaml` response uses the same build version as `padmark-server --version`;
release container builds inject the Git-tag version through the `APP_VERSION` build argument.

The generated OpenAPI handlers serve single-segment API paths; handwritten handlers also
support path-like note slugs. Keep their status codes, validation and ownership behavior
aligned. Edit codes for deletion come from `X-Edit-Code`; the deprecated `edit_code` query
parameter is a fallback for v0.1.6 clients only (`deleteEditCode`).

`domain.ReservedSlugPrefixes()` is the source of truth for named first-segment routes.
Registering a new prefix without reserving it can send that route into note resolution.

### Content negotiation

`negotiate(r)` ranks supported `Accept` formats by quality, specificity and header order.
JSON is the default for a missing/wildcard header; `?raw=1` selects raw content. Do not
replace the parser with substring checks.

Burn reads additionally require deliberate intent. Missing or wildcard-only `Accept`, `HEAD`
and browser subresource requests do not consume a note/link. Browser note reads first show
a confirmation form. A raw-query flag alone does not prove intent.

### CLI and MCP

`internal/adapters/cli` and `mcp` are clients of the HTTP API; they do not open application
databases. Both accept the API-token envelope format and use the same token/URL resolution
rules. `/llms.txt` is embedded machine-facing documentation; update it with API behavior.

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
account/admin forms and health endpoints remain available.

## 8. Dependency Injection

Manual wiring lives in [server.go](../internal/infra/server/server.go), chiefly
`serverAction`, `buildHandlers` and `buildRouter`:

1. Validate startup configuration/secrets and initialize logging.
2. Load the keyring, open storage, apply schema migrations and validate content encryption.
3. Construct repositories, ciphers, renderer, shared slug lock, caches and metrics.
4. Build note/link/auth managers and inject them into the HTTP handlers/router.
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

The separate `--metrics-addr` listener is disabled by default. It also exposes standard
Go/process collectors; metrics are not served on the public HTTP listener.
Use mocks with `.AnyTimes()` or a no-op metrics implementation when metric call counts are
not the subject of a test.

## 11. Graceful Shutdown

The server registers listener shutdown with `github.com/partyzanex/shutdown` and releases
its infrastructure resources. Keep shutdown bounded and handle `http.ErrServerClosed` as
the normal outcome of closing an HTTP server.

Background workers should make repeated close calls safe, for example with `sync.Once`.
Do not hold a mutex while waiting for network I/O, and do not store `context.Context` in
long-lived structs: pass a context to each operation instead.

## 12. Testing

### Application unit and integration tests

Unit tests live beside their application packages and use `testify/suite`, `require` and
`go.uber.org/mock`. Generate mocks from the consumer interface's actual directive; an HTTP
mock may target `http_test` rather than the implementation package.

Application integration suites live in `tests/auth`, `manager`, `mcp`, `encryption` and
`upgrade`, with `//go:build integration`. PostgreSQL repository tests use that tag too.
SQLite suites can use temporary/in-memory databases; PostgreSQL suites use testcontainers
and need Docker. Upgrade suites load dumps produced by the real v0.1.6 binary.

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

The build toolchain is Go 1.27.3. Use `for range N`, `t.Context()` / `s.T().Context()` and
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
`golang:1.27.3-alpine` and `CGO_ENABLED=0`, then runs it as UID/GID 65534 in scratch.
It contains no shell, CLI/MCP executable or browser-test environment.

The container works in `/data`; persist that volume for its generated keyring and SQLite data.
An explicitly configured keyring must already exist and be readable by the service UID.
Compose uses PostgreSQL with `db_data` and `content_keys` named volumes and requires
`POSTGRES_PASSWORD`. Startup migrates schema/content without commands inside the container.
Retain the same keys on every restart/replica; stop old-version writers during the upgrade.

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
| Content / account secrets | Installation-key AES-GCM envelopes; argon2id; TOTP |
| Unit/integration tests | testify, gomock, testcontainers |
| Browser tests | playwright-go, Go testing/require, Go pixelmatch |
