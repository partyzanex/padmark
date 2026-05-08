package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFirstLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "plain text",
			content: "hello world",
			want:    "hello world",
		},
		{
			name:    "h1 heading",
			content: "# Title",
			want:    "Title",
		},
		{
			name:    "h2 heading",
			content: "## Section",
			want:    "Section",
		},
		{
			name:    "h6 heading",
			content: "###### Deep",
			want:    "Deep",
		},
		{
			name:    "heading without space is returned as-is",
			content: "#nospace",
			want:    "nospace",
		},
		{
			name:    "indented line preserves leading spaces after trim",
			content: "  indented",
			want:    "indented",
		},
		{
			name:    "empty lines skipped, returns first non-empty",
			content: "\n\n# Title\nother",
			want:    "Title",
		},
		{
			name:    "blank content returns Untitled",
			content: "",
			want:    "Untitled",
		},
		{
			name:    "only whitespace lines returns Untitled",
			content: "   \n\t\n",
			want:    "Untitled",
		},
		{
			name:    "heading followed by body returns heading text",
			content: "# My Note\n\nsome body",
			want:    "My Note",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, firstLine(tt.content))
		})
	}
}
