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

func TestBuildUpdateReq_Private_SetsPrivateTrue(t *testing.T) {
	t.Parallel()

	req := buildUpdateReqFor(t, "--private")

	assert.True(t, req.Private.IsSet())
	assert.True(t, req.Private.Value)
}

func TestBuildUpdateReq_PrivateFalse_ClearsPrivate(t *testing.T) {
	t.Parallel()

	req := buildUpdateReqFor(t, "--private=false")

	assert.True(t, req.Private.IsSet(), "explicit --private=false must still be sent")
	assert.False(t, req.Private.Value)
}

func TestBuildUpdateReq_NoPrivateFlag_OmitsField(t *testing.T) {
	t.Parallel()

	req := buildUpdateReqFor(t)

	assert.False(t, req.Private.IsSet(), "private must be omitted when not passed, not sent as false")
}
