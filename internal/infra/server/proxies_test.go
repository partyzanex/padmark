package server

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParseTrustedProxies covers the trust boundary used to decide whether an X-Forwarded-For
// header may be honoured. A parsing mistake here either over-trusts (spoofable client IP) or
// under-trusts (legitimate proxy ignored), so every shape is pinned: empty input, explicit
// CIDRs, bare IPs expanded to host CIDRs (/32, /128), whitespace handling, and rejection of
// malformed entries.
func TestParseTrustedProxies(t *testing.T) {
	t.Run("empty input yields nil", func(t *testing.T) {
		nets, err := parseTrustedProxies("")

		require.NoError(t, err)
		require.Nil(t, nets)
	})

	t.Run("explicit cidr is parsed", func(t *testing.T) {
		nets, err := parseTrustedProxies("10.0.0.0/8")

		require.NoError(t, err)
		require.Len(t, nets, 1)
		require.True(t, nets[0].Contains(net.ParseIP("10.1.2.3")))
		require.False(t, nets[0].Contains(net.ParseIP("11.0.0.1")))
	})

	t.Run("bare ipv4 becomes /32 host route", func(t *testing.T) {
		nets, err := parseTrustedProxies("192.168.1.5")

		require.NoError(t, err)
		require.Len(t, nets, 1)
		ones, bits := nets[0].Mask.Size()
		require.Equal(t, 32, ones)
		require.Equal(t, 32, bits)
		require.True(t, nets[0].Contains(net.ParseIP("192.168.1.5")))
		require.False(t, nets[0].Contains(net.ParseIP("192.168.1.6")))
	})

	t.Run("bare ipv6 becomes /128 host route", func(t *testing.T) {
		nets, err := parseTrustedProxies("2001:db8::1")

		require.NoError(t, err)
		require.Len(t, nets, 1)
		ones, bits := nets[0].Mask.Size()
		require.Equal(t, 128, ones)
		require.Equal(t, 128, bits)
		require.True(t, nets[0].Contains(net.ParseIP("2001:db8::1")))
	})

	t.Run("comma-separated list with whitespace is parsed", func(t *testing.T) {
		nets, err := parseTrustedProxies(" 10.0.0.0/8 , 192.168.1.1 , ")

		require.NoError(t, err)
		require.Len(t, nets, 2)
	})

	t.Run("invalid bare ip is rejected", func(t *testing.T) {
		_, err := parseTrustedProxies("not-an-ip")

		require.Error(t, err)
	})

	t.Run("invalid cidr is rejected", func(t *testing.T) {
		_, err := parseTrustedProxies("10.0.0.0/99")

		require.Error(t, err)
	})
}
