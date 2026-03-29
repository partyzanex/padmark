package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContentType_Valid(t *testing.T) {
	assert.True(t, ContentTypeMarkdown.Valid())
	assert.True(t, ContentTypePlain.Valid())
	assert.False(t, ContentType("text/html").Valid())
	assert.False(t, ContentType("").Valid())
}
