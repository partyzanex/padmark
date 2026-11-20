// Package cli implements padmark-cli, the command-line client for the Padmark HTTP API.
//
// The commands are in groups:
//   - Notes: create, get, edit and delete.
//   - Short links: shorten, expand and delete-link.
//   - Files: upload, download, file-info, update-file and delete-file. These commands need a
//     server with object storage (--s3-endpoint); otherwise the server answers 503.
//   - Server and accounts: ping, setup, create-token, create-invite and trust.
//
// upload and download stream the file body. --timeout limits only the wait for the response
// headers of these two commands, so a large file is not stopped during the transfer.
package cli
