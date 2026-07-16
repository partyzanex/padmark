package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashSlug_DeterministicHex64(t *testing.T) {
	first := HashSlug("my-slug")
	second := HashSlug("my-slug")

	require.Equal(t, first, second, "HashSlug must be deterministic")
	require.Len(t, first, 64, "sha256 hex digest is 64 chars")
	require.NotEqual(t, HashSlug("my-slug"), HashSlug("other-slug"))

	for _, ch := range first {
		require.Contains(t, "0123456789abcdef", string(ch), "must be lowercase hex")
	}
}

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
