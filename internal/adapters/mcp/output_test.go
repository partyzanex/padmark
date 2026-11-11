package mcp

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

// TestLinkOutputFromLink_KeepsCreatedAt verifies that create_link and get_link results carry the
// link's creation time from the API response.
func TestLinkOutputFromLink_KeepsCreatedAt(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 10, 31, 12, 30, 0, 0, time.UTC)
	target, err := url.Parse("https://example.com")
	require.NoError(t, err)

	out := linkOutputFromLink(&padmark.LinkResponse{
		ID:             "link-id",
		TargetURL:      *target,
		Views:          0,
		BurnAfterClick: false,
		Privacy:        padmark.OptLinkResponsePrivacy{Value: "", Set: false},
		ExpiresAt:      padmark.OptNilDateTime{Value: time.Time{}, Set: false, Null: false},
		DirectRedirect: padmark.OptNilBool{Value: false, Set: false, Null: false},
		CreatedAt:      created,
	})

	assert.Equal(t, "2026-10-31T12:30:00Z", out.CreatedAt)
}
