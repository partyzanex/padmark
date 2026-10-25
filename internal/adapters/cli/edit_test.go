package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	urcli "github.com/urfave/cli/v3"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// buildUpdateReqFor runs editCommand's flag set with args and returns the resulting request,
// exercising the same flag parsing a real `edit` invocation would.
func buildUpdateReqFor(t *testing.T, args ...string) *padmark.UpdateNoteRequest {
	t.Helper()

	var req *padmark.UpdateNoteRequest

	app := &urcli.Command{
		Flags: editCommand().Flags,
		Action: func(_ context.Context, cmd *urcli.Command) error {
			req = buildUpdateReq(cmd, "content", "edit-code")

			return nil
		},
	}
	require.NoError(t, app.Run(t.Context(), append([]string{testBin}, args...)))

	return req
}

func TestBuildUpdateReq_Privacy_SetsValue(t *testing.T) {
	t.Parallel()

	req := buildUpdateReqFor(t, "--privacy", "owner")

	require.True(t, req.Privacy.IsSet())
	assert.Equal(t, padmark.UpdateNoteRequestPrivacyOwner, req.Privacy.Value)
}

func TestBuildUpdateReq_PrivacyPublic_ExplicitlySent(t *testing.T) {
	t.Parallel()

	req := buildUpdateReqFor(t, "--privacy", "public")

	require.True(t, req.Privacy.IsSet(), "explicit --privacy=public must still be sent")
	assert.Equal(t, padmark.UpdateNoteRequestPrivacyPublic, req.Privacy.Value)
}

func TestBuildUpdateReq_NoPrivacyFlag_OmitsField(t *testing.T) {
	t.Parallel()

	req := buildUpdateReqFor(t)

	assert.False(t, req.Privacy.IsSet(), "privacy must be omitted when not passed, not sent as public")
}
