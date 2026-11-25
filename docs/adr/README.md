# Architecture decision records

An architecture decision record (ADR) gives one design decision and the reasons for it.
Each record has these sections: Context, Decision, Alternatives and Consequences.

| Number | Title | Status | Date |
|---|---|---|---|
| [0001](0001-file-encryption-format.md) | File encryption format `pmf1` | Accepted | 2026-11-20 |
| [0002](0002-files-outside-ogen.md) | A manual HTTP handler for files, not ogen | Accepted | 2026-11-20 |
| [0003](0003-storage-slug-uniqueness.md) | No slug pre-checks; storage uniqueness and retry | Accepted | 2026-11-22 |
| [0004](0004-note-read-gives-record-kind.md) | The note read gives the kind of another record | Accepted | 2026-11-22 |

## Rules

- Give each record the next four-digit number and a kebab-case title.
- Use one of these statuses: `Proposed`, `Accepted`, `Deprecated`, `Superseded by #N`.
- Do not change the decision of an accepted record. Write a new record that supersedes it.
- Add each new record to the table above.
