package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

func TestHandleDeleteLinkRes_NotFound_CleanMessage(t *testing.T) {
	t.Parallel()

	err := handleDeleteLinkRes(&padmark.DeleteLinkNotFound{Message: "not found"})
	require.Error(t, err)
	assert.Equal(t, "link not found", err.Error())
}

func TestHandleDeleteLinkRes_Forbidden_CleanMessage(t *testing.T) {
	t.Parallel()

	err := handleDeleteLinkRes(&padmark.DeleteLinkForbidden{Message: "invalid edit code"})
	require.Error(t, err)
	assert.Equal(t, "forbidden: wrong edit code or --token is not the link's owner", err.Error())
}

func TestHandleDeleteLinkRes_Success(t *testing.T) {
	t.Parallel()

	err := handleDeleteLinkRes(&padmark.DeleteLinkNoContent{})
	require.NoError(t, err)
}
