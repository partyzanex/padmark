package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// The server's error body carries message="not found"; the CLI must surface a single clean
// sentence, not echo the server message behind a redundant "not found:" prefix (was: the doubled
// "not found: not found").
func TestHandleGetNoteRes_NotFound_CleanMessage(t *testing.T) {
	_, err := handleGetNoteRes(&padmark.GetNoteNotFound{Message: "not found"})
	require.Error(t, err)
	assert.Equal(t, "note not found", err.Error())
}

func TestHandleGetNoteRes_Gone_CleanMessage(t *testing.T) {
	_, err := handleGetNoteRes(&padmark.GetNoteGone{Message: "note has expired"})
	require.Error(t, err)
	assert.Equal(t, "note has expired or was already consumed", err.Error())
}

func TestHandleGetNoteRes_Success(t *testing.T) {
	note := &padmark.NoteResponse{ID: "abc"}

	got, err := handleGetNoteRes(note)
	require.NoError(t, err)
	assert.Equal(t, note, got)
}

func TestHandleDeleteNoteRes_NotFound_CleanMessage(t *testing.T) {
	err := handleDeleteNoteRes(&padmark.DeleteNoteNotFound{Message: "not found"})
	require.Error(t, err)
	assert.Equal(t, "note not found", err.Error())
}
