package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	urcli "github.com/urfave/cli/v3"

	padmark "github.com/partyzanex/padmark/pkg/client"
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

// buildCreateReqFor runs createCommand's flag set with args and returns the resulting request,
// exercising the same flag parsing a real `create` invocation would.
func buildCreateReqFor(t *testing.T, args ...string) *padmark.CreateNoteRequest {
	t.Helper()

	var req *padmark.CreateNoteRequest

	app := &urcli.Command{
		Flags: createCommand().Flags,
		Action: func(_ context.Context, cmd *urcli.Command) error {
			req = buildCreateReq(cmd, "content")

			return nil
		},
	}
	require.NoError(t, app.Run(t.Context(), append([]string{testBin}, args...)))

	return req
}

func TestBuildCreateReq_Privacy_SetsValue(t *testing.T) {
	t.Parallel()

	req := buildCreateReqFor(t, "--privacy", "owner")

	require.True(t, req.Privacy.IsSet())
	assert.Equal(t, padmark.CreateNoteRequestPrivacyOwner, req.Privacy.Value)
}

func TestBuildCreateReq_NoPrivacyFlag_OmitsField(t *testing.T) {
	t.Parallel()

	req := buildCreateReqFor(t)

	assert.False(t, req.Privacy.IsSet(), "privacy must be omitted, not sent as public")
}
