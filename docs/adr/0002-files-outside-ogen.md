# ADR-0002: A manual HTTP handler for files, not ogen

**Status**: Accepted
**Date**: 2026-11-20
**Authors**: padmark maintainers

## Context

The root `openapi.yaml` is the only source of the API (application programming interface)
contract. ogen generates two packages from it: the server types in
`internal/adapters/web/ogenapi` and the client in `pkg/client`. The note and link routes use
the generated server.

Version v0.5.0 adds the `/files` operations. An upload is a `multipart/form-data` body that
can be as large as `--max-file-size`. A download sends the full file.

The generated server calls `ParseMultipartForm` before the handler starts. That call keeps
the body in memory or in temporary files on the server disk. The generated client has two
more problems:

- `CreateFile` writes the `file` part before the text fields. A streaming server reads only
  the fields before `file`, so it does not see `ttl`, `privacy`, `slug` or the other options.
- `GetFileContent` reads the full response body into memory before it returns.

## Decision

1. `openapi.yaml` describes all `/files` operations, so the API reference and the generated
   client types stay complete.
2. `FileHandler` in [file_handler.go](../../internal/adapters/web/file_handler.go) serves
   all `/files` routes. It reads the form fields before the `file` part, then sends the
   `file` part to `files.Manager` as a stream.
3. `registerOgenRoutes` in [router.go](../../internal/adapters/web/router.go) does not
   register a `/files` route. The generated file methods stay on
   `ogenapi.UnimplementedHandler`.
4. `TestFileRoutes_ServedByFileHandler` in `router_test.go` makes sure that each `/files`
   route goes to `FileHandler`.
5. `pkg/client/files_stream.go` adds the manual methods `UploadFile` and `DownloadFile`.
   `UploadFile` uses `io.Pipe` and `multipart.Writer` and writes the fields first.
   `DownloadFile` copies the body to an `io.Writer`. The client thus does not keep a full
   file in memory. Use these two methods, not the generated `CreateFile` and `GetFileContent`.
6. The generated `GetFile`, `UpdateFile` and `DeleteFile` stay in use, because their bodies
   are small JSON (JavaScript Object Notation) values.

## Alternatives

- **The generated server for all `/files` routes.** A large upload fills the memory or the
  temporary directory before the size check. Rejected.
- **A change to the generated code.** The next `make gen` removes the change. Rejected.
- **A second API description only for files.** Two contracts can become different. Rejected.
- **A JSON body with base64 content.** This makes the body larger by one third and needs the
  full body in memory. Rejected.

## Consequences

- `FileHandler` must apply the same rules as the generated routes: the error format
  `{"message", "code"}`, `RequireSameOrigin`, the edit-code lockout and the status codes.
  Tests of `FileHandler` check these rules.
- A change to a `/files` operation needs a change in two places: `openapi.yaml` and
  `FileHandler`. Run `make gen` after a change to `openapi.yaml`.
- The upload form must have the text fields before the `file` part. The API reference, the
  CLI (command-line interface), the client and the web page send the fields in that order.
- `FileHandler` sets its own body limit (`--max-file-size`) and one-hour read and write
  deadlines. The general `--max-body-bytes` limit does not apply to an upload.
