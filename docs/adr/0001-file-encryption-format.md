# ADR-0001: File encryption format `pmf1`

**Status**: Accepted
**Date**: 2026-11-20
**Authors**: padmark maintainers

## Context

Version v0.5.0 adds file publishing. The server keeps the file content in an S3-compatible
bucket. S3 is the Simple Storage Service protocol. The operator of the bucket can be a
different party, so the bucket must not get the plaintext.

These requirements apply:

- The server must not keep a full file in memory. The default limit is 100 MiB, and the
  operator can set a larger limit with `--max-file-size`.
- A download must find each change to the stored data: a changed byte, a removed chunk, a
  changed chunk order, a cut end and extra data at the end.
- The installation keyring must stay the root of trust, as for notes and links.
- Key rotation (`encryption rewrap`) must not read or write the objects again.

The note and link envelopes encrypt a full value in one AES-GCM operation. AES-GCM is the
Advanced Encryption Standard in Galois/Counter Mode. One operation needs the full value in
memory, so that format does not satisfy the first requirement.

## Decision

The file cipher is `crypto.FileCipher` in
[filestream.go](../../internal/infra/crypto/filestream.go). It uses the STREAM construction
with AES-256-GCM.

1. `files.Manager` makes a random 32-byte content key for each file.
2. The object starts with the 4-byte header `pmf1`. The digit is the format version.
3. The cipher divides the plaintext into chunks of 64 KiB. Only the last chunk can be smaller.
   An empty file has one empty last chunk.
4. Each chunk is the AES-256-GCM ciphertext of the chunk and its 16-byte tag.
5. The 12-byte nonce of a chunk has these parts:
   - Bytes 0–7: the chunk counter, big-endian, from 0.
   - Bytes 8–10: zero.
   - Byte 11: 1 for the last chunk, else 0.
6. The additional authenticated data (AAD) of each chunk is the constant `padmark-file-v1`.
   The `pmf1` header is not part of the AAD. The reader compares the header with `pmf1` and
   stops on a different value, but the chunk tags do not include the header.
7. The content key and the file name go into one JSON (JavaScript Object Notation) value
   `{"name", "key"}`. The installation keyring seals that value as the `file` resource.
   The database keeps the result in the `files.sealed_meta` column.
8. The envelope of `sealed_meta` binds the resource byte 3 and the record ID, which is
   `domain.HashSlug(slug)`. A sealed value thus does not open for a different record.
9. The object key in the bucket is a random UUID (universally unique identifier). It does not
   contain the slug or the name.

A nonce never repeats under one key, because each file has its own key and the counter
increases. The counter in the nonce stops a change of chunk order. The last-chunk flag stops
a cut at a chunk boundary and extra chunks after the last chunk.

## Alternatives

- **One AES-GCM operation for the full file.** This needs the full file in memory. It also
  cannot give data to the reader before the full check. Rejected.
- **AES-CTR with one HMAC for the full file.** AES-CTR is counter mode without
  authentication, and HMAC is a hash-based message authentication code. The reader finds a
  change only at the end of the stream, after the server sent most of the plaintext.
  Rejected.
- **Server-side encryption of the S3 server (SSE-S3 or SSE-C).** The bucket operator then
  has the key or sees it in each request. The format also depends on the S3 product.
  Rejected.
- **Encryption with the installation key directly.** Key rotation would then read and write
  each object again. A wrapped key for each file lets rotation change only `sealed_meta`.
  Rejected.
- **A random nonce for each chunk.** It adds 12 bytes to each chunk and does not stop a
  change of chunk order. Rejected.

## Consequences

- The server keeps one chunk of 64 KiB for the cipher and one S3 upload part of 16 MiB for
  each transfer. Memory use does not depend on the file size.
- The size overhead is 4 bytes for the header and 16 bytes for each 64 KiB chunk.
- A reader of the bucket alone gets no content and no names. A reader of the database alone
  gets no slugs and no names.
- Some data stays in clear. The `files` table keeps the exact size, the creation time, the
  expiry, the owner ID, the privacy, the download count, the burn rule and the object key.
  The length of `sealed_meta` follows the length of the file name. The bucket shows the
  object key, the object size and the object times. The bucket gets no slug and no name.
- Back up the database and the keyring separately, as for notes and links. Back up the bucket
  too. A file needs all three: the object, the `sealed_meta` value and the key that wraps it.
  A copy that holds the database and the keyring together gives all names and content keys.
- When a key is lost, the files of that key cannot be read. This is the same as for notes.
- Key rotation (`encryption rewrap`) changes only the wrapping of `sealed_meta`. The content
  key of each file stays the same, and the objects do not change. Thus a person with an old
  keyring and an old copy of the database can still decrypt each object that is still in the
  bucket. Rotation alone does not stop this after a keyring leak. To stop it, upload each
  affected file again: the new upload gets a new content key and a new object, and the delete
  of the old file removes the old object. Then rotate the key and remove the old database
  backups, or keep them where the leaked keyring cannot reach them. padmark has no command
  that encrypts the stored objects again.
- Decryption sends the plaintext of each good chunk before it checks the next chunk. A bad
  chunk in the middle stops the download. The client then has part of the file and must
  discard it. `padmark-cli download` writes to a temporary file and renames it only after a
  full download.
- A new format needs a new header, for example `pmf2`, and a decoder for each header. It also
  needs a new AAD constant, for example `padmark-file-v2`. The chunk tags do not include the
  header, so with the same AAD a decoder for the new format could accept `pmf1` chunks.
