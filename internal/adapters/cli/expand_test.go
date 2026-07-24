package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

func TestHandleGetLinkRes_NotFound_CleanMessage(t *testing.T) {
	t.Parallel()

	_, err := handleGetLinkRes(&padmark.GetLinkNotFound{Message: "not found"})
	require.Error(t, err)
	assert.Equal(t, "link not found", err.Error())
}

func TestHandleGetLinkRes_Gone_CleanMessage(t *testing.T) {
	t.Parallel()

	_, err := handleGetLinkRes(&padmark.GetLinkGone{Message: "link has expired"})
	require.Error(t, err)
	assert.Equal(t, "link has expired", err.Error())
}

func TestHandleGetLinkRes_Success(t *testing.T) {
	t.Parallel()

	link := &padmark.LinkResponse{ID: "abc"}

	got, err := handleGetLinkRes(link)
	require.NoError(t, err)
	assert.Equal(t, link, got)
}
