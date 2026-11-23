# Changelog

All notable changes to this project. The format is inspired by
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## v0.5.0 — unreleased

v0.5.0 follows **v0.4.0**.

### Added

- A wide table in a note scrolls horizontally in its own box on every screen width; the page
  never scrolls sideways. Cells are at least 9em wide and do not break words; a narrow table
  takes the width of its content, and its frame wraps it. The editor preview shows the same box.
- `padmark-cli update-link` and the MCP tool `update_link` change the destination, expiry,
  privacy and redirect setting of a short link; an omitted option keeps its value.
- `tests/storage` runs one contract suite per repository role on SQLite and PostgreSQL, and
  `tests/arch` checks the layer imports with `go list`.
- File publishing in S3-compatible storage (S3: Simple Storage Service): `POST /files`
  (multipart upload), `GET /{id}` and `GET /files/{id}/download` (attachment),
  `GET /files/{id}` (metadata) and `DELETE /files/{id}` (`X-Edit-Code` or the owner). Files
  have the same slug namespace, privacy levels, TTL, edit codes and burn rule
  (`burn_after_download`) as notes and links.
- The server encrypts each file with its own key (AES-256-GCM in 64 KiB chunks); the database
  keeps the key and the file name sealed with the installation keyring. Key rotation includes
  files.
- New settings: `--s3-endpoint`, `--s3-bucket`, `--s3-region`, `--s3-path-style`,
  `--max-file-size`, and the environment-only `PADMARK_S3_ACCESS_KEY` / `PADMARK_S3_SECRET_KEY`.
- The server creates the `--s3-bucket` bucket at startup when it does not exist. The key then
  needs `s3:CreateBucket`.
- `PUT /files/{id}` changes the expiry, the privacy and the `burn_after_download` rule of a
  file. `ttl` counts from the change, `"ttl": null` removes the expiry, and an omitted field
  keeps its value. The content, the name and the owner do not change. The edit code comes only
  from `X-Edit-Code`; a signed-in owner can omit it. The edit-code lockout applies.
- `GET /{id}` of a file gives a browser (`Accept: text/html`) a file page with a **Download**
  button. The page does not count a download and does not delete a `burn_after_download` file.
  Other clients get the content, also `curl` with `Accept: */*`.
- Web pages for files: the upload page `/files` (drop area, progress bar, **Cancel upload**),
  the success page for a file and the edit page `/edit/{id}`. The header shows a **File**
  tab only when files are on. The pages show each file error, for example a file that is too
  large or a storage failure.
- `openapi.yaml` describes the `/files` operations. `/llms.txt` and `/api/reference.md` have a
  files part. A streaming handler serves `/files`, not the generated ogen server
  ([ADR-0002](docs/adr/0002-files-outside-ogen.md)).
- `pkg/client` adds `UploadFile` and `DownloadFile`. They stream the file and do not keep it in
  memory. The generated `CreateFile` sends the form fields after the file, so the server does
  not see them, and `GetFileContent` reads the full body into memory. Do not use these two.
  The other client changes only add names.
- CLI (command-line interface) commands `upload`, `download`, `file-info`, `update-file` and
  `delete-file`. `upload -` reads stdin and needs `--name`. `download` accepts an ID or a URL,
  writes to a temporary file, renames it after a full download, and replaces a file only with
  `--force`. `-o -` writes to stdout. For `upload` and `download`, `--timeout` limits only the
  wait for the response headers.
- MCP (Model Context Protocol) tools `upload_file`, `get_file`, `update_file` and
  `delete_file`. `upload_file` reads only regular files under `--upload-root` /
  `PADMARK_MCP_UPLOAD_ROOT`; an empty value (the default) refuses all uploads. The session
  vault keeps file edit codes as for notes and links.
- File metrics on the `--metrics-addr` listener: `padmark_files_uploads_total`,
  `padmark_files_downloads_total`, `padmark_files_deleted_total{reason}` (`user`, `burn`,
  `expired`), the `padmark_files_upload_bytes` histogram and
  `padmark_files_storage_errors_total{op}` (`put`, `stat`, `get`, `delete`).
- A background sweep removes expired files and their objects every `--files-sweep-interval` /
  `PADMARK_FILES_SWEEP_INTERVAL` (default `1h`; `0` turns it off). Each upload also removes
  expired files. The shutdown manager stops the loop before the database closes.
- File transfer limits: `--file-idle-timeout` / `PADMARK_FILE_IDLE_TIMEOUT` (default `1m`)
  stops an upload or a download that moves no data for that time; a stalled upload gets `408`.
  Each chunk of data moves the deadline forward, up to a total limit of one hour.
  `--max-uploads` / `PADMARK_MAX_UPLOADS` (default `8`) limits the uploads at the same time
  (`503`), and `--max-uploads-per-client` / `PADMARK_MAX_UPLOADS_PER_CLIENT` (default `4`)
  limits them for one client IP address (`429`). Both answers have `Retry-After: 10`. `0`
  selects the default; a negative value stops startup.
- Documentation: "Files setup" in the README (bucket policy, lifecycle rule for incomplete
  multipart uploads, reverse proxy limits), commented `PADMARK_S3_*` lines in
  `docker-compose.yml`, and the first architecture decision records (ADRs) in `docs/adr/`.
- TeX formulas in notes, as LLM (large language model) output writes them: `$…$` and `\(…\)` in
  a line of text, `$$…$$` in a line, and a block that starts a line with `$$` or `\[`. The note
  page and the editor preview draw them with KaTeX 0.19.1. KaTeX, its CSS and fonts are embedded
  in the binary and served from `/static/katex/`; a note without a formula does not load them.
  "$5 and $10" stays text. A formula that KaTeX cannot parse shows its TeX source.

### Changed

- Notes, links, files, sessions, invites, reveal tokens and API tokens share one expiry rule: a
  record is expired from its `expires_at` moment on, in the domain and in SQL on both databases.
- Slugs stay unique only through the `slug_namespace` table: `Create` no longer checks other
  tables first and retries a random slug up to five times (notes did not retry before); a custom
  slug first releases an expired holder of any kind. The in-process slug lock is removed
  (ADR-0003).
- `GET /{id}` and `/edit/{id}` read the note first. When no note has the slug, the same query
  gives the kind of the short link or file that has it (`slug_namespace`), and only that kind's
  Manager is called. A note view needs one database read; a short link or a file page needs two.
- Error messages name each operation once; account repository errors start with the database
  dialect, and file errors name the hashed record ID.
- The "On this page" sidebar follows the new design: numbered H2 and H3 rows, H4 rows without a
  number, only the current section expanded ("Expand all" expands all), two-line names, a list
  that follows scrolling, and "Back to top". On a phone the block stays at the top of the screen
  and the note has no card frame. Note text: h3 is 20px, h4 uses the interface font at 15px.
- Notes, short links and files check privacy, expiry and burn intent inside their usecase with
  one reader value. PUT and DELETE check privacy on the same stored record that they change; a
  hidden record gives `401`. Without accounts, the Edit link of an owner-only note shows and
  `GET /links/{id}` serves an owner-only link, as the other pages do. The `PUT /notes/{id}`
  response always has `expires_at` (`null` without expiry).
- padmark-cli and padmark-mcp share one package (`internal/clientauth`) for the token file,
  server trust, the plain-HTTP rule and the HTTP client. MCP tools report status 422 as
  `validation error`, as padmark-cli does. padmark-cli refuses the IDs `.` and `..` and a `--ttl`
  above 9223372036 before it sends a request. The unused `edit_code` field of the
  `create_links_bulk` output is removed.
- Invite and reveal token lifetimes and the cleanup of stale invites, reveal tokens and sessions
  moved from the repositories to the auth managers. The schema allows one admin, so the admin
  cannot be revoked.
- `--shutdown-timeout` is now the limit of the full shutdown sequence, not of each phase. Open
  requests get this time less one fifth (at most 2 seconds); then the server closes their
  connections, stops background work and closes the database. A forced close gives a warning in
  the log and exit code 0. The log records the signal and the duration of each phase.
  `docker-compose.yml` sets `stop_grace_period: 15s`.
- The image and CI build with Go 1.27.4, which fixes GO-2026-6859 (an HTTP/1 server crash in
  `net/http`). Rebuild the image.
- A short-link update (`PUT /links/{id}`) writes only the fields that the client sent: an omitted
  `ttl` or `direct_redirect` keeps the stored value, and `null` resets it. Two concurrent updates
  thus keep both changes.
- The note content limit is a setting: `--max-content-length` / `PADMARK_MAX_CONTENT_LENGTH`
  (characters). The default `0` uses the `--max-body-bytes` value (4 MiB), so
  `PADMARK_MAX_BODY_BYTES` alone raises both limits. Before, the limit was a fixed 1,048,576
  characters in the code and in the API spec, and no setting changed it. A longer note now gets
  `422` instead of `400`. The API spec has no `maxLength` for `content` any more, so
  `padmark-cli` and `padmark-mcp` do not refuse a long note before they send it.
- Short links and files follow the note flow. **Open** on the success page opens the page of the
  record; its **Edit** action opens the editor; **Save** returns to the page of the record.
  A short link gets a page at `/links/{id}` for a client that prefers `text/html`: the
  destination, the settings and the **Copy**, **Link** and **Edit** actions, in the layout of the
  note page. The page uses the privacy and expiry rules of `GET /links/{id}`, does not count a
  view and does not burn the link. `/{id}` stays the redirect, and other clients of
  `GET /links/{id}` still get JSON. The file page `/{id}` has the layout of the note page.
- The editor of each record is at `/edit/{id}`. `/links/{id}/edit` and `/files/{id}/edit`
  answer `301` to it. The success page of a link or a file no longer has an **Edit** button, as
  for a note. On a reader page, **New** opens the create form of the same kind.
- The link and file editors use the labels of the note editor: **Save**, "Paste your edit code",
  "Publish settings", and a busy text on the main button.
- `--disable-api` keeps the link pages `/links`, `/links/{id}` (HTML) and `/links/{id}/edit`
  open, as for the note and file pages. Before, the link form answered `503` with JSON.
- `files` is a reserved first slug segment. A note slug that starts with `files/` stops being
  reachable.
- The schema migration `20261118000001_files` adds the `files` table and rebuilds the SQLite
  `slug_namespace` table and its triggers. Back up the database before the upgrade.
- `--disable-api` also turns off the file API. The `GET`/`HEAD` pages `/files`,
  `/files/{id}/edit` (redirect) and `/files/{id}/download`, the download form
  `POST /files/{id}/download` and the file page at `/{id}` stay available; upload, change and
  delete are off.
- The footer text is "Markdown notes, short links & files" when files are on.

### Fixed

- An update of an expired note, short link or file answers `410 Gone` and changes nothing. Before,
  notes and links accepted it and made the record live again, and files answered `404`. The
  storage `UPDATE` repeats the expiry check, and `openapi.yaml` declares `410` for the three
  `PUT` routes.
- The note and link editors show "The note was deleted or its lifetime ended." and "The link was
  deleted or its lifetime ended." when a save gets `410`, as the file editor does, instead of the
  server word "expired".
- A database failure while a reveal token is used shows the 500 page instead of "Forbidden"; the
  note is not read or burned, and the token stays valid.
- The math renderer registers its goldmark node kinds once per process; two renderers made at
  the same time were a data race.
- A note larger than 64 MiB in bytes gets `422` before encryption; the server refuses a
  `--max-content-length` above 67108864 at startup. Before, such a note got `500`.
- Only slug collisions give "slug is already taken"; a duplicate file object key is an internal
  error. SQLite consumes reveal tokens with one conditional `UPDATE`, as PostgreSQL does, and a
  database failure is logged instead of looking like a wrong token.
- Account and link repositories use the configured logger, so `--log-level` and `--log-format`
  apply to their records.
- `padmark-cli` and `padmark-mcp` stop when they cannot write the cleartext-token warning,
  instead of sending the credential without the warning.
- The CSRF signature and the `Content-Disposition` encoding no longer ignore write errors.
- `docker-compose.yml` passes `PADMARK_ENABLE_ACCOUNTS` (default `false`) and, when set,
  `PADMARK_BOOTSTRAP_TOKEN` to the server. Before, the values in `.env` did not reach the
  container, so accounts stayed off and note privacy was not checked.
- An edit, delete, burn read or link expiry delete that passed its access check no longer changes
  a new note or link that took the same address after the check: the write also matches the
  stored edit-code hash and answers `404` when the record changed.
- An expired note or short link no longer keeps its address. A new note, link or file with the
  same custom slug is created at once; a record that its owner extended is never removed.
- Following an expired short link no longer deletes it when its owner extended it at the same
  time.
- Password change: the new password, the new session and the revocation of all other sessions
  and API keys are stored in one database transaction. A failure no longer leaves old sessions or
  API keys valid; the "contact an administrator" warning is removed.
- Files: after an unclear metadata write error, the server reads the record before it deletes the
  uploaded object. An expired file whose object delete fails no longer blocks later expired files,
  and the cleanup after an upload has one total limit of 3 seconds.
- A stalled request, a long file transfer or a connection without a request no longer holds the
  server stop after the deadline. The redirect and metrics listeners stop with the main listener.
- The note editor sends the text unchanged on publish and save: leading spaces, a leading newline
  and the final newline stay.
- When the browser blocks session storage, the result page of a new note, short link or file still
  shows the link and the edit code, and the form shows no false error.
- For a note that burns some time after the first read, the result page shows that delay. Before,
  it said "burns immediately".
- The editor preview of a `text/plain` note shows plain text, as the note page does.
- Cyrillic text uses the project fonts in all roles: Source Serif 4 and JetBrains Mono get
  self-hosted Cyrillic subsets, so Cyrillic words no longer show in Georgia or Times. A page
  downloads a Cyrillic font file only when it shows Cyrillic text in that font.
- The CLI (`expand`, `shorten`) and the MCP proxy (`get_link`, `create_link`,
  `create_links_bulk`) keep the `#fragment` of a short link target instead of `%23`.
  `POST /links/bulk` echoes each target in the form that the client sent. `pkg/client` has a new
  function, `SplitFragment`, that repairs a decoded `url.URL`.
- The MCP proxy `upload_file` no longer has a data race on the read error of the local file.
- The note page shows images that the Markdown embeds as base64 `data:` URIs (PNG, JPEG, GIF
  and WebP). Before, the server removed their source, and only the editor preview showed them.
  SVG and other types stay removed, and links cannot use `data:` URIs. In place of an image
  that the page will not show, the preview gives the reason. The server writes a `warn` log
  record with the note hash and the count of each reason.
- A short link keeps the `#fragment` of its target URL. Before, the server saved
  `https://example.com/page#part` as `https://example.com/page%23part`, and the redirect went to
  a page that does not exist. The cause was the `uri` decoder of ogen, which keeps the fragment in
  the path. Links that you saved before this fix keep `%23`: edit their target URL again.

### Security

- `padmark-cli setup` does not follow a redirect to a different server. Before, a `307` or `308`
  answer sent the password and the bootstrap token to the new address.
- With accounts on, a note, link or file change (`PUT` or `DELETE`) stops when the privacy check
  cannot read the record: a storage failure gives `500` and a missing record gives `404`. Before,
  the change continued without the privacy check.
- `POST /files`, `PUT /files/{id}` and `DELETE /files/{id}` reject a request with a foreign
  `Origin`, because a cross-site form can send a multipart body without a CORS (cross-origin
  resource sharing) preflight.
- The S3 client has a 10-second dial, a 10-second TLS (Transport Layer Security) handshake,
  a 30-second wait for the response headers and 3 attempts. A storage server that does not
  respond thus does not hold a request for about 10 minutes, as with the `minio-go` defaults.
- Error texts of the S3 adapter replace the object key with `<object-key>`. Logs do not contain
  file names, object keys or edit codes.
- A download is an attachment of type `application/octet-stream` with
  `X-Content-Type-Options: nosniff` and `Cache-Control: private, no-store`. The
  `Content-Disposition` names cannot add a parameter or a header.
- An S3 failure before the response starts gives `503`, not `500`. When an object delete
  fails after the record is gone, the server logs a warning and increments
  `padmark_files_storage_errors_total{op="delete"}`.
- A plain `http://` S3 endpoint other than a loopback address gives a warning at startup.
- `POST /files` accepts only the five known text fields, each one time, and a maximum of
  64 KiB before the `file` part; other forms get `400`. The transfer deadlines start only after
  the fields are valid, so a client that sends no file part cannot hold memory.
- A browser downloads a `burn_after_download` file only through the CSRF-protected form
  `POST /files/{id}/download` of the file page. A `GET` with `Sec-Fetch-Site`, a prefetch or an
  `Accept` that prefers HTML never deletes such a file; a browser goes to the file page.
  Non-browser clients send `Accept: application/octet-stream`.
- An intentional abort of a download after the first byte (`http.ErrAbortHandler`) passes
  through `WithRecovery`; the server no longer logs it as an error "panic recovered".

### Tests

- `tests/files` runs the file life cycle through HTTP with SQLite and an S3 container, with a
  server restart and a content key rotation. `tests/manager`, `tests/cli`, `tests/mcp` and the
  `s3store` adapter tests also use `adobe/s3mock:3.12.0` and need Docker.
- `tests/upgrade` upgrades real v0.4.0 SQLite and PostgreSQL dumps to v0.5.0. The notes and
  links stay readable, and the record counts do not change.

## v0.4.0 — unreleased

v0.4.0 follows **v0.3.3** (`b32ab62`); summaries of the previously tagged v0.2.x/v0.3.x
releases are recorded below.

The upgrade comparison and cumulative feature overview use historical **v0.1.6** (`d2e5e8f`)
as their baseline because its real database dumps exercise the plaintext-to-encrypted upgrade.
That baseline is not the latest released version.

### Changes since v0.3.3

#### Added

- Account ownership and the `public`/`authenticated`/`owner` privacy model; configurable API
  disabling.
- Short links with browser forms for creating, editing and deleting them, including privacy,
  redirect mode, expiration and burn-after-click settings. The expiration has presets (1 hour,
  1, 7 and 30 days) beside a custom lifetime. Editing preserves expiry unless changed.
- Short links answer at `/{id}`, like notes: the server serves the note with that `id` or, when
  there is none, the link. `POST /links` returns `Location: /{id}`, and the web UI, CLI and MCP
  show the short URL. The older `/r/{id}` URLs keep working.
- Write / Split / Preview views in the note editor. The browser renders the preview with
  vendored marked 18.1.2 and sanitizes it with DOMPurify 3.4.17; no server endpoint is added.
  The last view is remembered per browser; phone width offers only Write and Preview.
- Optional "Contents sidebar" for notes: with `show_contents: true` the note view shows an
  "On this page" column with links to the h2/h3 headings and marks the current section. The
  editor has a "Contents sidebar" switch; `padmark-cli create`/`edit` accept `--contents`, and
  the MCP `create_note`/`update_note` tools accept `show_contents`. The field defaults to
  `false`, an update without it keeps the stored value, and responses add `show_contents`.
  The migration `20261110000001_notes_show_contents.sql` adds the column with `false` for
  existing notes.
- `POST /notes` accepts `ttl` without `burn_after_reading` as the note's lifetime from creation.
  `padmark-cli create` and `edit` accept `--ttl` without `--burn`, and the MCP `create_note`
  tool passes it through.
- A new note accepts a chosen edit code in the web UI, with a button that generates one.
- Headless account provisioning, the MCP proxy and `/llms.txt`.
- Installation-key content envelopes, key-management commands and resumable schema/content
  upgrade support, including conversion of tagged v0.1.6 plaintext notes.
- Automatic startup content migration with a persisted `pending`/`ready` state. A restart
  resumes unfinished rows and skips migration writes once ready. PostgreSQL containers
  generate their default keyring on the persistent `/data` volume; no in-container command
  is needed for a normal upgrade.
- Database-enforced slug uniqueness across notes and links, including concurrent creates
  from different server replicas. Reservations are released atomically on deletion or burn.
  If existing native records already collide, the schema migration stops and preserves both
  records; resolve the collision explicitly before retrying.
- API documentation reports the application release version, shared with `--version` and
  injected from the release tag when building container images.
- `padmark-cli create` and `edit` accept the v0.1.6 `--private` flag again as a deprecated
  alias: `--private` is `--privacy authenticated`, `--private=false` is `--privacy public`.

#### Changed

- Web UI redesign from `docs/design/` (hand-off notes, tokens and screen mockups): one token
  set for the light/dim/dark themes, self-hosted Onest interface font, font sizes only from
  the scale 11/12/13/14/15/17/20/24/28/32, 44px main buttons, 36–40px fields and 12–14px card
  corners. The editor and short-link pages have a settings panel with privacy cards and
  switches. The short-link form is a card with labelled destination and slug fields and a line
  that shows the resulting link. The note header, success, sign-in and admin pages are new.
  The header shows "Sign in" or an account block with sign-out, "New" on reader pages and
  admin links in the admin panel. The success page masks the edit code until "Show". The
  privacy level `authenticated` is labelled "Login required". The note editor card fills the
  free screen height, with the card footer at the bottom; the note edit form shows its slug
  read-only.
- One page frame for all pages: the same 1200px page width, the same header height and the
  same top edge of the editor and article cards. The page keeps space for the vertical
  scrollbar, so the width does not change between short and long pages.
- Phone-width interface: a 52px header bar with one theme button and a menu, a fixed bar with
  "Settings" and the main action, the publish settings in a bottom sheet, the "On this page"
  list as a drop-down above the note, admin tables as lists, and 44px or larger tap targets.
- The note page shows the Edit link to every reader who can see the note, anonymous readers
  included: the author of an anonymous note edits it with its edit code, which the server checks.
- The note editor shows a publish or save error in a block above the main button and keeps it
  until the next attempt. A note without a title takes at most 80 characters of its first text
  line as the title; the title field accepts 500 characters at most.

#### Fixed

- Pasted text with a first line of more than 500 characters is published: the title that
  comes from that line no longer exceeds the server limit.
- An API error names only its cause: the reply no longer starts with the names of internal
  operations (for example `create note: slug is already taken` is now `slug is already taken`).
- A signed-in user who is not an admin gets the HTML error page on `/admin`, not a text line.

#### Security

- Bootstrap/step-up protection, session/key revocation, CSRF/lockout hardening, argon2 memory
  budgeting, content/rendering limits and operational configuration checks.

#### Tests

- The separate Go browser-test module, desktop/mobile PNG comparisons and CI integration.
- `TestPageFrame` compares the page frame of all pages; `TestErrors` opens each error of the
  web interface and checks that the page shows it in full.

### Upgrading v0.3.2/v0.3.3 to v0.4.0

Stop the old app container, retain the PostgreSQL database, and start the new image with a
persistent `/data` volume. Startup applies schema migrations, creates its default keyring
when missing, wraps old ciphertext, and persists `phase=ready` before accepting requests.
Subsequent starts reuse the key and skip migration writes. Compose provides the named
`content_keys` volume. An explicitly configured keyring remains supported and must exist.

Regression tests replay real v0.3.2/v0.3.3 SQLite/PostgreSQL dumps: notes, historical edit
codes, account passwords/TOTP and API keys stay usable. Existing browser sessions are
invalidated by the session-hashing migration, so users sign in again. Old and new binaries
must not run against the upgraded database simultaneously; binary-only rollback is unsupported.
If using the removed `--auth-tokens` setting, follow the account migration below.

### Compatibility with v0.1.6 — TL;DR

A v0.1.6 installation upgrades in place: existing notes are carried over, and every removed
option either keeps working with a warning or fails startup with a message that says what to do
instead. Stop old writers and retain the database and persistent key directory, then start
`serve`: it completes schema/content migrations automatically before opening its listeners.
Explicit maintenance commands remain available for preview and exceptional data repair.

| What | Effect when upgrading from v0.1.6 |
|---|---|
| **Existing notes are converted, not lost.** v0.1.6 plaintext notes are tagged by schema migration and converted to hashed IDs, encrypted content and hashed edit codes; URL, content, usable edit code, burn and expiry are preserved. | `serve` completes this automatically and persists `phase=ready` before listening. Interrupted runs resume. The optional `encryption migrate --dry-run` previews counts. A 64-hex custom slug with an edit code that exactly matches a stored hash remains ambiguous (`corrupt_raw`): use `mark-legacy` before conversion or pruning. Invalid data blocks startup and is never deleted automatically. |
| **Schema migrations: 19 new, several irreversible** (drop `private`, token hashing). They run on `serve` and on `padmark-server migrate`. | SQLite: when the database already has applied migrations and new ones are pending, a copy `<db>.bak-<YYYYMMDD-HHMMSS>` (mode `0600`) is written next to it first; startup fails if the copy cannot be made. `--skip-migration-backup` / `PADMARK_SKIP_MIGRATION_BACKUP` opts out. PostgreSQL: a warning lists the pending versions; take `pg_dump` yourself. |
| **A persistent keyring is required for content encryption.** | `serve` creates a missing default file with mode `0600`: beside the SQLite DB or in the PostgreSQL working directory (`/data` in the image). Compose preserves it on `content_keys`. Explicit paths and in-memory SQLite require an existing keyring. Maintenance commands never generate keys implicitly. Preserve and back up the keyring separately from the DB. |
| **`--auth-tokens` / `PADMARK_AUTH_TOKENS` is removed**; static bearer tokens are replaced by the account system (`--enable-accounts`, TOTP, API tokens). | Startup **fails closed** whenever the flag or the variable is set: the error names `--enable-accounts`, `PADMARK_BOOTSTRAP_TOKEN` and `/api/tokens`; the token value is never echoed. Without accounts the server is fully public, as v0.1.6 was without `--auth-tokens`, so unset the variable only after deciding whether you need accounts. |
| **`--cookie-max-age` / `PADMARK_COOKIE_MAX_AGE` is deprecated** in favour of `--session-ttl` (seconds, default 30 days). | Still accepted: its value becomes the session TTL with a warning. If both are set, `--session-ttl` wins and the old flag is ignored with a warning. |
| **API: `private: bool` is deprecated** in favour of `privacy: public\|authenticated\|owner`. | Requests may still send `private`: `true` maps to `authenticated`, `false` to `public`; sending both with contradicting values is a `400`. Responses carry both `privacy` and the read-only `private` (`true` for anything other than `public`). The `private` column is dropped irreversibly by migration `20260723000001`; the field lives on in the API only. |
| **Privacy levels need the account system.** Without `--enable-accounts` nothing enforces `authenticated` or `owner`. | `privacy: authenticated` or `owner` → `422`; the editor offers only Public (and Owner only only to a signed-in user). `private: true` (v0.1.6) is still accepted and, as in v0.1.6 without `--auth-tokens`, not enforced until accounts are enabled. Existing v0.1.6 private notes stay readable, editable and deletable with their edit codes. |
| **`edit_code` on creation: 8–128 characters.** | `POST /notes` with a shorter code → `400/422`. Converted v0.1.6 notes keep their old codes whatever their length; verification is unchanged. `DELETE /notes/{id}?edit_code=` still works but is deprecated: it is used only without `X-Edit-Code` and logs a warning; send the header, as the v0.1.6 `padmark-cli` already does. |
| **Note content: at most 1 MiB** (1,048,576 characters) on create and update. v0.1.6 accepted up to the 4 MiB request body. | Larger content → `400`. Existing larger notes are converted and stay readable unchanged; to edit one, shorten its content to 1 MiB or less. Rendered HTML of notes up to 256 KiB is cached in memory for 2 minutes. |
| **Burn-after-reading reads need intent.** | `GET /notes/{id}` with no `Accept` header at all (the v0.1.6 `padmark-cli` and `pkg/client`) still reads and burns the note. An `Accept` of only `*/*` (`curl`), `HEAD`, a browser subresource, and the short `/{id}` URL without an exact `Accept` → `409 burn_read_not_explicit`; the note stays intact. Browsers get a reveal page first. |
| **Custom slugs stay on by default** (`--custom-slugs`, `PADMARK_CUSTOM_SLUGS`), as in v0.1.6. | `POST /notes` with `slug` (`padmark-cli create --slug`) works as before. New: a slug may contain `/` and `.` (up to 10 segments), and a first segment that is a reserved route name (`admin`, `setup`, `change-password`, …) → `422`. Set `PADMARK_CUSTOM_SLUGS=false` to issue only random slugs (`422 custom slugs are disabled on this server`). Existing notes with custom slugs stay readable and editable either way. |
| **`docker-compose.yml` changed.** `POSTGRES_PASSWORD` and `PADMARK_CONTENT_KEYRING_FILE` are required; the image is `postgres:17-alpine`; the database port is published on `127.0.0.1:5432`. | An existing volume keeps its old password (`padmark` in the v0.1.6 file), so set `POSTGRES_PASSWORD=padmark` or change the password in the database first. The v0.1.6 file (`postgres:18-alpine` with the volume on `/var/lib/postgresql/data`) does not start with current PostgreSQL 18 images. If your data directory is PostgreSQL 18, keep a PostgreSQL 18 image: PostgreSQL 17 cannot open it. Remove the port mapping if port 5432 is already in use on the host. |
| **The SQLite file is tightened to `0600`** on open, including `-wal`/`-shm`. | A pre-existing file with group/other bits is chmod-ed with an INFO log entry; if chmod fails, startup continues with a WARN instead of refusing. Other processes that read the file through group permissions lose access. |
| **PostgreSQL DSN must be a URL** (`postgres://`, `postgresql://`, `unix://`). | A key=value DSN is rejected at startup with an error that never contains the password. Previously such a DSN crashed the process with the password in the panic output. |
| **Go 1.27.3, image `golang:1.27.3-alpine` → `scratch`.** | Rebuild the image. `make lint` needs golangci-lint v2.15+. |
| Security headers and CSP are tightened; `/api` no longer loads Redoc from a CDN. | Check proxies that rewrite CSP. Redoc search needs `worker-src blob:`, which is not granted, so search in the docs page does not work (it did not before either). |

What did **not** change: addresses, default limits (`--rate-limit 10`, `--rate-burst 20`, timeouts,
4 MB body), an empty `--allowed-hosts` keeps the old behaviour, `--enable-accounts` is off by
default, so without opting in the server stays fully public, exactly like v0.1.6 without
`--auth-tokens`.

### Cumulative feature overview since v0.1.6

**Accounts and access control** (`--enable-accounts`)
- Users with a password (argon2id) and TOTP; the first admin is created through `/setup` with
  `PADMARK_BOOTSTRAP_TOKEN` (≥32 characters, environment only). Invites are single-use and live
  24 hours. Cookie sessions, `--session-ttl` defaults to 30 days.
- `/admin`: invites, API keys (Bearer tokens for the CLI/MCP, `/api/tokens`), user revocation with
  cascade. `/change-password` requires TOTP and signs out every other session and revokes the
  account's keys. Signing in again revokes the previous session; `/admin` and `/change-password`
  have a sign-out control.
- Headless provisioning: `padmark-cli setup`, `create-token`, `create-invite`, JSON `/setup`,
  `/api/tokens`, `/api/invites`.
- Privacy levels `privacy: public | authenticated | owner` for notes and links. The owner edits
  and deletes their notes without an `edit_code`. Anonymous mutations are limited to what an
  anonymous caller could read.
- `--disable-api`: kill switch for the REST/JSON API (`503`). The editor becomes read-only:
  publishing/saving use those API endpoints. Note views, link redirects, account/admin forms
  and health checks remain available.

**Short links**
- Redirects check privacy on the snapshot supplying the URL. Burn/expiry deletion and click
  increments also check the reader's current access in SQL, including across replicas.
- `/links`, `/links/bulk`, `GET/PUT/DELETE /links/{id}`, redirect at `/r/{id}`. The same
  edit_code / ownership / privacy / burn-after-click model as notes.
- An interstitial page instead of an instant redirect for anonymously created links by default
  (`--direct-redirect false`), so the instance cannot serve as an open redirector. Prometheus
  metrics on a separate `--metrics-addr` listener.

**Content encryption at rest**
- Note content and link target URLs are encrypted with AES-256-GCM under a random data key that
  is wrapped by the active installation key from the keyring. Slugs no longer take part in key
  derivation, which enables `--custom-slugs` (human-readable slugs with `/` and `.`).
- Key-management subcommands under `encryption`: `keygen`, `key-add`, `key-activate`,
  `key-remove`, `register`, `status`, `migrate`, `rewrap`, `convert` and `prune`.
  `serve` verifies the key registry and the envelopes before opening
  listeners. `encryption migrate` also converts v0.1.6 plaintext notes in place (hashed slug,
  encrypted content, hashed edit code); `tests/upgrade` runs that path against database dumps
  produced by the real v0.1.6 binary on SQLite and PostgreSQL.
  Every nonempty legacy edit code uses argon2id: its length and alphabet cannot establish
  that it was server-generated. The original code remains valid after migration.
- Session IDs, invite tokens and burn-after-reading reveal tokens are stored as SHA-256 digests
  only.

**Agent surface**
- `padmark-mcp`: a stdio MCP proxy to the server. `/llms.txt` as a machine-readable description.
  `padmark-cli` gained `--allow-insecure-http`; by default a token is never sent over plain HTTP
  to a non-loopback host.

**Operations**
- `--public-scheme https` marks cookies `Secure` and enables HSTS. `--allowed-hosts` now applies
  to the main listener too (`421`), with `/healthz` and `/readyz` exempt. `--trusted-proxies`
  warns about misconfiguration. `--csrf-secret` is configurable.
- `padmark-server migrate` applies pending schema migrations and exits. On SQLite a `0600` copy of
  the database is written next to it before the first pending migration (`--skip-migration-backup`
  to opt out); on PostgreSQL the pending versions are logged with a reminder to `pg_dump`.
  Existing backups are preserved on filename collisions: a new copy gets a unique suffix,
  and a failed write removes only the file reserved for that attempt.
- `--argon2-memory/time/threads` and the new `--argon2-memory-budget` (128 MiB): a process-wide
  cap on the memory held by concurrent argon2 derivations. README gained a memory-sizing section;
  a 256 MB container needs `GOMEMLIMIT≈200MiB`.
- Fonts and Redoc are self-hosted, no external CDNs. docker-compose persists generated keys in a named volume;
  `.env.example` documents server/Compose configuration, keyring behavior and memory settings.

**Development and browser tests**
- Go browser tests in the separate `tests/ui` module, using playwright-go and the standard
  test runner: 19 scenarios at desktop/mobile sizes (38 checks). Forms, validation, keyboard
  actions, clipboard, themes, browser diagnostics and visual comparisons are covered.
- Browser dependencies, driver/cache, PNG references and reports live under `tests/ui`.
  Root npm/TypeScript manifests are removed; the Go binding installs its internal driver.
- `make test-ui-setup`, `test-ui`, `test-ui-headed`, `test-ui-debug`, `test-ui-update` and
  `test-ui-report` provide setup, execution, Inspector debugging, reference updates and reports.
  Root `make test` remains the application unit/integration command; it excludes the nested
  browser module. CI runs the browser suite separately before container publishing.
- README, development/agent guides and `/llms.txt` are aligned with current keyring defaults,
  migration steps, account-based ownership, header-only delete codes and Go UI-test commands.

### Security fixes (from the audit)

No critical findings. High:
- The toolchain moves from Go 1.26.x to 1.27.3, with refreshed dependencies.
- Burn-after-reading: HTML-only interstitial and reveal tokens, so a note no longer burns from an
  `<img src>` or an `Accept: */*` request.

Medium:
- Per-username lockout on `/totp-login` and login: 10 failures, 5 minutes, across all IPs. A lock
  can no longer be evicted from the cache by spraying 10,000 keys.
- Caller-chosen `edit_code`s are hashed with argon2id at the configured cost and must be at least
  8 characters. Wrong-code lockout is per resource/account, or per resource/client IP for an
  anonymous caller (IPv6 grouped by /64), rather than global per resource.
- A keyring with permissions wider than `0600` is refused. The SQLite file is tightened to `0600`
  on open (a failed chmod on a pre-existing file is a warning, not a refusal).
- Process-wide argon2 memory budget: without it, 4 concurrent `POST /notes` with an `edit_code`
  OOM-killed the server in a 256 MB container.
- CSRF tokens are bound to the session; forms check `Origin`/`Sec-Fetch-Site`. Duplicate
  `padmark_session`/`padmark_csrf` cookies (cookie tossing from a sibling subdomain) are treated
  as ambiguous and rejected.
- The PostgreSQL DSN password never reaches errors, panics or logs on a malformed DSN.

Low and other:
- CSP: `base-uri 'none'`, `form-action 'self'`, `frame-ancestors 'none'`, `object-src 'none'`,
  nonces on scripts. Markdown element IDs get a `user-content-` prefix against DOM clobbering.
- The `DELETE` query-string `edit_code` is deprecated: it is used only without `X-Edit-Code`
  and logs a warning (kept for v0.1.6 clients). Usernames are unique
  case-insensitively (the migration fails and changes nothing if collisions already exist).
  Stored argon2 costs above the accepted ceiling are refused before any allocation.
- Note content is capped at 1,048,576 characters, with the request-byte limit still applying;
  rendered HTML is cached (128 entries, 2 minutes). The goldmark
  parser is guarded against quadratic inputs (nested `>`, long `*`/`_`/`~` runs, `](` tails); a
  single 255 KiB document no longer pins a core for minutes.
- ogen decode errors no longer leak internals; `415` is documented. An authenticated intruder on
  a private note gets `403` instead of a redirect to `/login`. The slug TOCTOU race between notes
  and links is closed. SQLite transactions use `BEGIN IMMEDIATE`.

### Upgrade checklist from v0.1.6

1. Stop every server instance; the content conversion must not race with writers.
2. Back up the database yourself as well (`migrate` makes a SQLite copy, but not a PostgreSQL
   one).
3. Preserve an existing keyring. If this installation has never used one, `serve` creates the
   default automatically beside the SQLite DB or in the PostgreSQL working directory
   (`/data` in the container). Keep that directory on a persistent volume. An explicit path
   must already contain a keyring.
4. Start the new image: `serve` completes schema/content migration automatically. For an
   optional manual preview, use `migrate`, then `encryption migrate --dry-run`.
   `encryption status` must show `plaintext: 0`. If it shows
   `untagged_legacy` or `corrupt_raw` above 0, list the IDs with
   `encryption prune --corrupt --dry-run`. Tag each v0.1.6 note among them with
   `encryption mark-legacy --id <id>` and rerun `encryption migrate`. Do not run
   `encryption prune --corrupt` before that: it deletes every remaining `corrupt_raw` record.
5. Remove `--auth-tokens` / `PADMARK_AUTH_TOKENS`; startup fails while it is set. If you need
   authentication: `--enable-accounts`, `PADMARK_BOOTSTRAP_TOKEN`, then `/setup` or
   `padmark-cli setup`, then unset the bootstrap token. Replace `--cookie-max-age` with
   `--session-ttl` at your convenience.
6. Update API clients when convenient: `private` → `privacy`; new `edit_code`s need ≥ 8
   characters; content is limited to 1 MiB; send `X-Edit-Code` instead of `?edit_code=`.
7. In a memory-limited container set `GOMEMLIMIT` to about 75–80 % of the limit and, if needed,
   `PADMARK_ARGON2_MEMORY_BUDGET`.
8. Behind a reverse proxy: `--trusted-proxies`, `--public-scheme https`, `--allowed-hosts`.
9. Check readiness and the `stored content ready` log. The persisted phase must be `ready`;
   wrong keys or invalid records keep startup blocked. Retain the generated keyring with
   the persistent volume and back it up separately from the database.

## v0.3.3 — 2026-07-12

- Self-host Source Serif 4 and JetBrains Mono fonts and remove the Google Fonts CDN dependency.
- Use `MaxBodyBytes` as the note-size bound instead of the earlier fixed 1 MiB limit.
  v0.4.0 adds an explicit content-character limit and rendering guards.

## v0.3.2 — 2026-07-12

- Add `--public-scheme` to force the scheme used in absolute links behind a reverse proxy.

## v0.3.1 — 2026-07-10

- Add the CLI `--private` flag.

## v0.3.0 — 2026-07-10

- Issue account API tokens from the admin page and authenticate CLI requests with Bearer keys.
- Store API-token hashes, use self-contained token envelopes with the server URL, and enforce
  a per-user key cap.
- Update related migrations, security boundaries and token-issuance documentation.

## v0.2.1 — 2026-07-05

- Support immediate burn-after-reading and speed up edit-code hashing while retaining
  verification of existing hashes.

## v0.2.0 — 2026-06-08

- Encrypt note content at rest and hash edit codes.
- Add password/TOTP accounts, the admin interface and associated tests.
- Update burn-after-reading behavior and strengthen security/layer boundaries.

## v0.1.6 — 2026-05-10

Historical plaintext-note release with Markdown/JSON/raw content negotiation, `--auth-tokens`
and the `private` flag. Its real dumps are retained in `tests/fixtures/v0.1.6` for upgrade tests;
it is not the project's latest tagged release.
