package cli

import (
	url "net/url"
	"testing"
	time "time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	padmark "github.com/partyzanex/padmark/pkg/client"
)

func TestHandleGetLinkRes_NotFound_CleanMessage(t *testing.T) {
	t.Parallel()

	_, err := handleGetLinkRes(&padmark.GetLinkNotFound{
		Message: "not found",
		Code:    padmark.OptString{Value: "", Set: false},
	})
	require.Error(t, err)
	assert.Equal(t, "link not found", err.Error())
}

func TestHandleGetLinkRes_Gone_CleanMessage(t *testing.T) {
	t.Parallel()

	_, err := handleGetLinkRes(&padmark.GetLinkGone{
		Message: "link has expired",
		Code:    padmark.OptString{Value: "", Set: false},
	})
	require.Error(t, err)
	assert.Equal(t, "link has expired", err.Error())
}

func TestHandleGetLinkRes_Success(t *testing.T) {
	t.Parallel()

	link := &padmark.LinkResponse{
		ID: "abc",
		TargetURL: url.URL{
			Scheme: "", Opaque: "", User: nil, Host: "", Path: "", Fragment: "",
			RawQuery: "", RawPath: "", RawFragment: "", ForceQuery: false, OmitHost: false,
		},
		Views: 0, BurnAfterClick: false,
		Privacy:        padmark.OptLinkResponsePrivacy{Value: "", Set: false},
		ExpiresAt:      padmark.OptNilDateTime{Value: time.Time{}, Set: false, Null: false},
		DirectRedirect: padmark.OptNilBool{Value: false, Set: false, Null: false},
		CreatedAt:      time.Time{},
	}

	got, err := handleGetLinkRes(link)
	require.NoError(t, err)
	assert.Equal(t, link, got)
}
