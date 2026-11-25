package domain

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoteNotFound_LinkOrFileGivesOtherKindError(t *testing.T) {
	t.Parallel()

	for _, kind := range []RecordKind{KindLink, KindFile} {
		err := NoteNotFound(string(kind))

		other, _ := errors.AsType[*OtherKindError](err)
		require.NotNil(t, other, kind)
		assert.Equal(t, kind, other.Kind)
		assert.ErrorIs(t, err, ErrNotFound, "a caller that asks only for a note sees a missing note")
	}
}

func TestNoteNotFound_NoOtherRecordGivesErrNotFound(t *testing.T) {
	t.Parallel()

	// "notes" cannot occur: the trigger writes the namespace row in the transaction of the note.
	for _, holder := range []string{"", "notes", "unknown"} {
		err := NoteNotFound(holder)

		assert.Same(t, ErrNotFound, err, holder)
	}
}
