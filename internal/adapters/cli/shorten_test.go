package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	urcli "github.com/urfave/cli/v3"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// buildShortenReqFor runs shortenCommand's flag set with args and returns the resulting request,
// exercising the same flag parsing a real `shorten` invocation would. --target-url is always
// prepended since it is a required flag; pass extra flags via args.
func buildShortenReqFor(t *testing.T, args ...string) *padmark.CreateLinkRequest {
	t.Helper()

	var req *padmark.CreateLinkRequest

	app := &urcli.Command{
		Flags: shortenCommand().Flags,
		Action: func(_ context.Context, cmd *urcli.Command) error {
			var buildErr error

			req, buildErr = buildShortenReq(cmd)

			return buildErr
		},
	}

	fullArgs := append([]string{testBin, "--" + FlagTargetURL, "https://example.com/dest"}, args...)
	require.NoError(t, app.Run(t.Context(), fullArgs))

	return req
}

func TestBuildShortenReq_TargetURL(t *testing.T) {
	t.Parallel()

	req := buildShortenReqFor(t)

	assert.Equal(t, "https://example.com/dest", req.TargetURL.String())
}

func TestBuildShortenReq_Slug_SetsValue(t *testing.T) {
	t.Parallel()

	req := buildShortenReqFor(t, "--"+FlagSlug, "my-link")

	require.True(t, req.Slug.IsSet())
	assert.Equal(t, "my-link", req.Slug.Value)
}

func TestBuildShortenReq_NoSlugFlag_OmitsField(t *testing.T) {
	t.Parallel()

	req := buildShortenReqFor(t)

	assert.False(t, req.Slug.IsSet())
}

func TestBuildShortenReq_Privacy_SetsValue(t *testing.T) {
	t.Parallel()

	req := buildShortenReqFor(t, "--"+FlagPrivacy, "owner")

	require.True(t, req.Privacy.IsSet())
	assert.Equal(t, padmark.CreateLinkRequestPrivacyOwner, req.Privacy.Value)
}

func TestBuildShortenReq_NoPrivacyFlag_OmitsField(t *testing.T) {
	t.Parallel()

	req := buildShortenReqFor(t)

	assert.False(t, req.Privacy.IsSet(), "privacy must be omitted, not sent as public")
}

func TestBuildShortenReq_Burn_SetsTrue(t *testing.T) {
	t.Parallel()

	req := buildShortenReqFor(t, "--"+FlagBurn)

	require.True(t, req.BurnAfterClick.IsSet())
	assert.True(t, req.BurnAfterClick.Value)
}

func TestBuildShortenReq_NoBurnFlag_OmitsField(t *testing.T) {
	t.Parallel()

	req := buildShortenReqFor(t)

	assert.False(t, req.BurnAfterClick.IsSet())
}

func TestBuildShortenReq_TTL_SetsValue(t *testing.T) {
	t.Parallel()

	req := buildShortenReqFor(t, "--"+FlagTTL, "3600")

	require.True(t, req.TTL.IsSet())
	assert.Equal(t, int64(3600), req.TTL.Value)
}

func TestBuildShortenReq_EditCode_SetsValue(t *testing.T) {
	t.Parallel()

	req := buildShortenReqFor(t, "--"+FlagEditCode, "MySecretCode1")

	require.True(t, req.EditCode.IsSet())
	assert.Equal(t, "MySecretCode1", req.EditCode.Value)
}

func TestBuildShortenReq_DirectRedirect_ExplicitFalse_SetsValue(t *testing.T) {
	t.Parallel()

	req := buildShortenReqFor(t, "--"+FlagDirectRedirect+"=false")

	require.True(t, req.DirectRedirect.IsSet())
	assert.False(t, req.DirectRedirect.Value)
}

func TestBuildShortenReq_DirectRedirect_ExplicitTrue_SetsValue(t *testing.T) {
	t.Parallel()

	req := buildShortenReqFor(t, "--"+FlagDirectRedirect+"=true")

	require.True(t, req.DirectRedirect.IsSet())
	assert.True(t, req.DirectRedirect.Value)
}

func TestBuildShortenReq_NoDirectRedirectFlag_OmitsField(t *testing.T) {
	t.Parallel()

	req := buildShortenReqFor(t)

	assert.False(t, req.DirectRedirect.IsSet(), "direct_redirect must be omitted, inheriting the server default")
}

func TestHandleCreateLinkRes_SlugConflict_CleanMessage(t *testing.T) {
	t.Parallel()

	_, err := handleCreateLinkRes(&padmark.CreateLinkConflict{Message: "slug already exists"})
	require.Error(t, err)
	assert.Equal(t, "slug conflict: slug already exists", err.Error())
}

func TestHandleCreateLinkRes_Success(t *testing.T) {
	t.Parallel()

	link := padmark.CreateLinkResponse{ID: "abc"}

	got, err := handleCreateLinkRes(&padmark.CreateLinkResponseHeaders{Response: link})
	require.NoError(t, err)
	assert.Equal(t, &link, got)
}
