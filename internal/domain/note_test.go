package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContentType_Valid(t *testing.T) {
	assert.True(t, ContentTypeMarkdown.Valid())
	assert.True(t, ContentTypePlain.Valid())
	assert.False(t, ContentType("text/html").Valid())
	assert.False(t, ContentType("").Valid())
}

func TestNote_Validate_TitleOptional(t *testing.T) {
	// Title is intentionally optional — an empty title must validate.
	note := &Note{Title: "", Content: "body"}

	require.NoError(t, note.Validate())
}

func TestNote_Validate_TitleTooLong(t *testing.T) {
	note := &Note{Title: strings.Repeat("a", MaxTitleLength+1)}

	require.ErrorIs(t, note.Validate(), ErrTitleTooLong)
}

func TestNote_Validate_InvalidContentType(t *testing.T) {
	ct := ContentType("text/html")
	note := &Note{Title: "ok", ContentType: &ct}

	require.ErrorIs(t, note.Validate(), ErrInvalidContentType)
}

func TestNote_Validate_ContentAtLimitOK(t *testing.T) {
	note := &Note{Content: strings.Repeat("a", MaxContentLength)}

	require.NoError(t, note.Validate())
}

func TestNote_Validate_ContentTooLong(t *testing.T) {
	note := &Note{Content: strings.Repeat("a", MaxContentLength+1)}

	require.ErrorIs(t, note.Validate(), ErrContentTooLong)
}
