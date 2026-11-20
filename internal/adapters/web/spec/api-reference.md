# Padmark API reference

Self-hosted markdown notes, short links and files. One URL, many consumers: a browser sees the same
`/{id}` path as a rendered page, a machine client that sends `Accept: application/json` gets
structured JSON. This reference describes release {{VERSION}}. The request/response schemas
are at [OpenAPI](/api/openapi.yaml), embedded in the same server build. The browser-facing
redirect and headless setup workflows are described below; they are outside OpenAPI's paths.
Use documentation from the target instance, rather than mixing versions from different servers.

The API base is the origin that served this document (scheme, host and optional port).
Resolve all root-relative URLs against that origin. When copying this text elsewhere, retain
the original API base explicitly. A decoded token envelope can also supply its `url`.

## Notes

- `POST /notes` — create a note. Body: `content` (required), optional `title`, `content_type`
  (`text/markdown` | `text/plain`), `slug` (custom slugs may be disabled on the instance),
  `burn_after_reading`, `ttl`, `privacy` (`public` | `authenticated` | `owner`), `edit_code`,
  `show_contents` (default `false`: `true` adds an "On this page" sidebar to the HTML view).
  With `burn_after_reading: true`, `ttl` is the post-read grace period in seconds (omitted or
  0 means immediate burn). Without it, a positive `ttl` is the note's lifetime counted from
  creation; omitted or 0 means no expiry. Responds `201` with the note and `Location: /{id}`; `409`
  when the slug is taken, `422` for an invalid slug or `privacy: owner` without a credential.
- `GET /notes/{id}` — read a note as JSON (send `Accept: application/json`). Incrementing
  `views` is a side effect of every read. A burn note with no grace period is destroyed by
  its first intentional read; with a positive burn `ttl`, that read starts the grace period.
  That burning read must be explicit: name the format in `Accept`
  (`application/json`, `text/markdown`, `text/plain` or `text/html`); a burn-after-reading note
  requested with only wildcards (`*/*`), with `HEAD` or as a browser subresource gets `409`
  (`code: burn_read_not_explicit`) and stays intact. For v0.1.6 compatibility,
  `GET /notes/{id}` without both `Accept` and `Sec-Fetch-Dest` also counts as an intentional
  read and can burn the note. Omitting `Accept` is not a safe discovery technique. `401` when the
  note's `privacy` requires a credential the request didn't supply, `404` for a wrong id, `410`
  for an expired note.
- `PUT /notes/{id}` — replace a note. Body: `content` (required), optional `title`,
  `content_type`, `burn_after_reading`, `ttl`, `privacy`, `show_contents` (omitted keeps the
  current value), `edit_code`; the slug cannot be
  changed. Pass the `edit_code` in the body, or omit it when authenticated as the note's
  owner. `401` for an anonymous caller on a note whose `privacy` requires a credential, `403`
  on a wrong edit code (or a non-owner without one). As in `POST`, a non-burn `ttl` is
  a hard expiry, here counted from the update: `ttl: N` sets it, `ttl: 0` expires
  immediately, omitted preserves the current expiry (except that disabling an existing burn
  timer clears its expiry), and `ttl: null` removes it. With burn, `ttl` is the post-read
  grace period; omitted, null or 0 means immediate burn.
- `DELETE /notes/{id}` — delete; send the edit code in the `X-Edit-Code` header, unless
  authenticated as the owner. Notes still accept a deprecated `edit_code` query parameter for
  v0.1.6 clients; the header takes precedence. New clients should always use the header.

Every new note returns `edit_code` **exactly once, at creation**. Generated codes use a salted
SHA-256 hash; caller-chosen codes use argon2id. A lost edit code cannot be recovered; use a
saved code or a session/API token for the owning account. Ownership follows the account ID,
so another session or API token for that same account also works. Omit `edit_code` on create
to get a random 12-character code; a
chosen one (notes and links alike) must be 8–128 characters with no leading/trailing whitespace
or control characters (`400` if too short/long, `422` otherwise).

## Links

- `POST /links` — create a short link (`target_url`, http/https only — anything else is
  `400`; optional `slug` (a single path segment), `burn_after_click`, `ttl` (hard expiry in
  seconds from creation, independent of `burn_after_click`), `privacy`, `edit_code`,
  `direct_redirect`). Responds `201` with link metadata and `Location: /{id}`; `409` when
  the slug is taken by a note or link.
- `POST /links/bulk` — create up to 100 links; best-effort, `results[i]` corresponds to
  `links[i]` (per-element `error` or success fields).
- `GET /links/{id}` — link metadata; no `views` increment, no burn-after-click consumption.
  `401` when the link's `privacy` requires a credential, `404` unknown, `410` expired.
  Metadata and privacy are read from storage on every request across replicas. Only decrypted
  public targets may be reused from cache, after matching the current ciphertext.
- `PUT /links/{id}` — replace `target_url` (required, http/https only); pass `edit_code` in
  the body or omit it as the exact authenticated owner. Optional `ttl`, `privacy`, and
  `direct_redirect` preserve current values when omitted. TTL counts from this update;
  `ttl: null` removes expiry, `ttl: 0` expires immediately. `direct_redirect: null` restores
  the server default. Preserves slug, views, edit code, owner, creation time and burn setting.
- `DELETE /links/{id}` — send `X-Edit-Code`, or omit it as the authenticated owner.
  Unlike notes' legacy fallback, an `edit_code` query parameter is ignored.
- `GET /{id}` follows a link when no note has this `id` (the older `GET /r/{id}` also works):
  an immediate `301` when direct redirect is enabled, otherwise
  an HTML transparency page with a link to the destination. It is outside the JSON API.
  `/{id}` serves notes. A `burn_after_click` link is consumed only by a deliberate access (a
  browser navigation, or an `Accept` naming e.g. `text/html` exactly); without `Accept`, with
  `*/*`, with `HEAD` or as an embedded subresource it answers `409`
  (`code: burn_read_not_explicit`) and stays intact.

## Files

Files are optional. Without object storage on the server, each `/files` operation answers
`503`. A file uses the same `privacy` levels, edit codes and ownership rules as a note. A file
shares the slug namespace with notes and links. The server keeps each file encrypted.

- `POST /files` — upload one file as `multipart/form-data`, not JSON. Optional text fields:
  `ttl` (lifetime in seconds from the upload; omitted means no expiry), `privacy`,
  `burn_after_download` (`true` or `false`), `slug` (one path segment) and `edit_code`.
  Send these fields before the `file` part: the server ignores fields after `file`. The
  `filename` of the part becomes the file name (1–255 bytes). The server streams the file to
  storage, and `--max-file-size` limits its size. Responds `201` with the file description,
  the one-time `edit_code` and `Location: /{id}`. Errors: `400` for a form without a `file`
  part or a field that is not valid, `409` when the slug is taken, `413` above the size limit,
  `422` for an invalid slug, name or privacy level.
- `GET /files/{id}` — the file description as JSON: `id`, `url`, `name`, `size`, `privacy`,
  `downloads`, `burn_after_download`, `created_at` and `expires_at` (absent without expiry).
  It does not download the content and does not count a download. `401` when the file's
  `privacy` requires a credential, `404` for a wrong id, `410` for an expired file.
- `GET /files/{id}/download` — the content as `application/octet-stream` with
  `Content-Disposition: attachment` and `Content-Length`. Each download increments
  `downloads`. A `burn_after_download` file is deleted by its first deliberate download: send
  `Accept: application/octet-stream`. Without it, the server answers `409`
  (`code: burn_read_not_explicit`) and the file stays. `GET /{id}` also downloads the file.
  When the transfer stops early, the client gets fewer bytes than `Content-Length`; discard
  that data.
- `PUT /files/{id}` — change the access rules. JSON body with optional `ttl` (seconds from
  this change; `null` removes the expiry, `0` expires now), `privacy` and
  `burn_after_download`. An omitted field keeps its value. The content, name and owner do not
  change. Send the edit code only in the `X-Edit-Code` header, or omit it as the owner; an
  `edit_code` field in the body gets `400`. Responds `200` with the file description.
- `DELETE /files/{id}` — delete the file and its content. Send `X-Edit-Code`, or omit it as
  the owner. The server ignores an `edit_code` query parameter. Responds `204`.

`PUT` and `DELETE` use the same `401`/`403`/`429` rules as links. Upload a file with curl.
curl sends the parts in the order of the `-F` options, so put `-F file=@…` last:

```sh
curl --fail-with-body -H "Authorization: Bearer $PADMARK_TOKEN" \
  -F ttl=86400 -F privacy=authenticated -F file=@report.pdf "$PADMARK_URL/files"
```

Download it with the name from `Content-Disposition`:

```sh
curl --fail -OJ -H "Accept: application/octet-stream" "$PADMARK_URL/files/$FILE_ID/download"
```

## Authentication

Access depends on each note/link's `privacy`: `public` permits anyone, `authenticated` permits
any signed-in account, and `owner` permits only the creating account across its sessions and
API tokens. With accounts disabled, all callers are anonymous and explicitly requesting
`authenticated` or `owner` is rejected with `422`. Public creation and public mutations with
an edit code do not require login. Edit codes do not bypass privacy restrictions.
An API token is a long-lived key issued by an account holder through the web UI; present it
as `Authorization: Bearer <key>`. The issued token may be a `pmk_…` envelope
(`pmk_` + base64url of `{"url": "<base url>", "token": "<key>"}`) — decode it, use `url` as
the API base and `token` in the Bearer header. Unknown or expired keys get `401`. Anonymous
`PUT`/`DELETE` on a note or link whose `privacy` requires a credential also get `401`, before
the edit code is checked.

### Account endpoints (require `--enable-accounts`)

- `POST /api/tokens` — issue a new API token **for the calling user themselves**; optional body
  `{"ttl_days"?, "password"?, "totp_code"?}` — `ttl_days` is the lifetime in days (0 or omitted
  = never expires, max 3650, else `422`). A browser session issues directly; a **bearer token
  must re-authenticate** with the account `password` and a current `totp_code` (each code works
  once), else `403` with `code: step_up_required` / `step_up_failed`. A key issued by a bearer is
  revoked together with that bearer. `201` → `{"token": "pmk_…"}`, shown once; `429` when the
  user's 20-token cap is reached. Revocation stays admin-panel-only; changing the account
  password revokes all of the user's keys.
- `POST /api/invites` — admin only. Optional body `{"password"?, "totp_code"?}`: the same
  step-up rule for bearer callers. `201` → `{"invite_url": …}`: a single-use signup link
  (24 h TTL). `403` for a non-admin authenticated caller or a missing/wrong step-up.

Both answer `401` without a valid credential and `503` when accounts are not enabled.

Headless bootstrap: `POST /setup` accepts JSON — send
`{"username", "password", "invite"?, "bootstrap_token"?}` with `Accept: application/json` (no
CSRF cookie is involved on this path; include an `invite` from `POST /api/invites` once the
first admin exists). Creating the first admin (no invite) requires `bootstrap_token` equal to
the server's `PADMARK_BOOTSTRAP_TOKEN`; without one configured that setup is disabled (`403`,
`code: bootstrap_disabled`), a missing or wrong token is `403` (`code: bootstrap_token_invalid`). `201` → `{"otp_auth_uri": "otpauth://totp/…", "username", "is_admin"}` — the URI (and
its secret) is shown exactly once. Errors: `400` for a malformed body or an empty
`username`, `415` unless the body is `Content-Type: application/json`, `403` for a
cross-site browser request or once any account exists without an invite (setup closed),
`409` taken username, `410` spent/expired invite, `422` weak password (≥ 12 chars with
upper/lower/digit/special) or invalid username (1–64 letters/digits/`._@-`, start with
a letter/digit, no whitespace or control characters).

## Errors

All machine responses use `{"message": string}` (message required, optional `code`), e.g.
`404 {"message": "not found"}`. Request bodies must be `application/json` (`415` otherwise)
and within the server's body limit (`413`). `429` means rate limiting or a repeated-failure
lockout is active (10 failed edit-code attempts within a rolling 5-minute window); edit-code
lockout is scoped to the resource and authenticated user ID (across sessions/tokens).
Anonymous attempts are scoped to the resource and client IP, with IPv6 grouped by /64.
`503` with `code: "api_disabled"` means the instance runs with `--disable-api` and the
REST surface is off. The editor cannot publish or save because it uses the REST API;
viewing notes and following short links at `/{id}`, account/admin forms and health checks still work.
Match `code`, not `message`; other `503` responses have other causes.

## Safe discovery

Fetching documentation and health endpoints does not consume a note or short link. For example,
with `PADMARK_URL` set to the target instance's API base:

```sh
curl --fail --silent --show-error "$PADMARK_URL/healthz"
```

Do not probe a note or follow a short URL just to test connectivity: those requests can increment
views or consume burn content. Do not download a file to test connectivity: a download
increments `downloads` and can delete a burn-after-download file. Authorization is enforced by the server, not by this document.

## Health

- `GET /healthz` — liveness, `200`.
- `GET /readyz` — readiness, `200` or `503`.
