package rendercache

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestCache_SetGet_RoundTrip(t *testing.T) {
	t.Parallel()

	cache := New(4, time.Minute, discardLogger())
	stamp := time.Now()
	cache.Set("abc", stamp, "<p>hi</p>")

	got, found := cache.Get("abc", stamp)
	require.True(t, found)
	require.Equal(t, "<p>hi</p>", got)
}

func TestCache_Get_MissesOnDifferentUpdatedAt(t *testing.T) {
	t.Parallel()

	cache := New(4, time.Minute, discardLogger())
	stamp := time.Now()
	cache.Set("abc", stamp, "<p>hi</p>")

	_, found := cache.Get("abc", stamp.Add(time.Nanosecond))
	require.False(t, found, "a note updated since the render must not be served from the cache")
}

func TestCache_Get_MissesOnUnknownID(t *testing.T) {
	t.Parallel()

	_, found := New(4, time.Minute, discardLogger()).Get("missing", time.Now())
	require.False(t, found)
}

func TestCache_Delete(t *testing.T) {
	t.Parallel()

	cache := New(4, time.Minute, discardLogger())
	stamp := time.Now()
	cache.Set("abc", stamp, "<p>hi</p>")

	cache.Delete("abc")
	cache.Delete("never-set")

	_, found := cache.Get("abc", stamp)
	require.False(t, found)
}

func TestCache_BoundedByLRU(t *testing.T) {
	t.Parallel()

	cache := New(2, time.Minute, discardLogger())
	stamp := time.Now()
	cache.Set("a", stamp, "1")
	cache.Set("b", stamp, "2")
	cache.Set("c", stamp, "3")

	_, found := cache.Get("a", stamp)
	require.False(t, found, "the least recently used entry is evicted once the cache is full")

	_, found = cache.Get("c", stamp)
	require.True(t, found)
}

func TestCache_ExpiresAfterTTL(t *testing.T) {
	t.Parallel()

	cache := New(4, 20*time.Millisecond, discardLogger())
	stamp := time.Now()
	cache.Set("abc", stamp, "<p>hi</p>")

	require.Eventually(t, func() bool {
		_, found := cache.Get("abc", stamp)

		return !found
	}, time.Second, 10*time.Millisecond)
}
