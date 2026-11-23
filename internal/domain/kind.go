package domain

// RecordKind is the kind of a record that is not a note but has a slug: notes, short links and
// files share one slug namespace. The values are the resource names of the slug_namespace table.
type RecordKind string

const (
	// KindLink is a short link.
	KindLink RecordKind = "links"
	// KindFile is a file.
	KindFile RecordKind = "files"
)

// OtherKindError tells that no note has a slug, but a short link or a file has it. The note read
// finds the kind in the same query (notes.Storage.Get), so GET /{id} calls the Manager of that
// kind and makes no other lookup. The error matches ErrNotFound with errors.Is: a caller that
// asks only for a note answers as for a missing note, and no client sees the kind.
type OtherKindError struct {
	Kind RecordKind
}

// Error returns the message of ErrNotFound and the kind.
func (e *OtherKindError) Error() string {
	return ErrNotFound.Error() + ": the slug names a record of kind " + string(e.Kind)
}

// Unwrap returns ErrNotFound.
func (*OtherKindError) Unwrap() error {
	return ErrNotFound
}

// NoteNotFound returns the error of a note read that found no note. holder is the slug_namespace
// resource of the record ID, or "" when no record has the ID. A short link or a file gives an
// *OtherKindError; each other value gives ErrNotFound.
func NoteNotFound(holder string) error {
	switch kind := RecordKind(holder); kind {
	case KindLink, KindFile:
		return &OtherKindError{Kind: kind}
	default:
		return ErrNotFound
	}
}
