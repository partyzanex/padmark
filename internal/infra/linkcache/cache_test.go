package linkcache

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/bluele/gcache"
	"github.com/stretchr/testify/require"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestCache_SetGet_RoundTrip(t *testing.T) {
	t.Parallel()

	cache := New(10, time.Minute, discardLogger())
	cache.Set(context.Background(), "abc", "ciphertext", "https://example.com")

	got, found := cache.Get("abc", "ciphertext")
	require.True(t, found)
	require.Equal(t, "https://example.com", got)
}

func TestCache_Get_Miss(t *testing.T) {
	t.Parallel()

	cache := New(10, time.Minute, discardLogger())

	_, found := cache.Get("missing", "ciphertext")
	require.False(t, found)
}

func TestCache_Delete(t *testing.T) {
	t.Parallel()

	cache := New(10, time.Minute, discardLogger())
	cache.Set(context.Background(), "abc", "ciphertext", "https://example.com")

	cache.Delete("abc")

	_, found := cache.Get("abc", "ciphertext")
	require.False(t, found)
}

func TestCache_Delete_MissingKey_NoPanic(t *testing.T) {
	t.Parallel()

	cache := New(10, time.Minute, discardLogger())

	require.NotPanics(t, func() { cache.Delete("never-set") })
}

// Even if an old snapshot is refilled after a remote update, it must miss
// when the manager supplies the current ciphertext from storage.
func TestCache_RejectsDifferentCiphertext(t *testing.T) {
	t.Parallel()

	cache := New(10, time.Minute, discardLogger())
	cache.Set(t.Context(), "id", "old ciphertext", "https://example.com/old")
	_, found := cache.Get("id", "new ciphertext")
	require.False(t, found)
	cache.Set(t.Context(), "id", "new ciphertext", "https://example.com/new")
	target, found := cache.Get("id", "new ciphertext")
	require.True(t, found)
	require.Equal(t, "https://example.com/new", target)

	_, found = cache.Get("id", "old ciphertext")
	require.False(t, found)
}

func TestCache_TTLExpiry(t *testing.T) {
	t.Parallel()

	// A fake clock replaces the wait for real time: the result does not depend on the load of
	// the machine.
	const ttl = 10 * time.Millisecond

	clock := gcache.NewFakeClock()
	cache := newWithClock(10, ttl, clock, discardLogger())
	cache.Set(t.Context(), "abc", "ciphertext", "https://example.com")

	clock.Advance(ttl / 2)

	_, found := cache.Get("abc", "ciphertext")
	require.True(t, found, "entry must be present before its TTL has elapsed")

	clock.Advance(ttl)

	_, found = cache.Get("abc", "ciphertext")
	require.False(t, found, "entry must be gone once its TTL has elapsed")
}

func TestCache_LRUEviction_WhenFull(t *testing.T) {
	t.Parallel()

	cache := New(2, time.Minute, discardLogger())
	cache.Set(context.Background(), "a", "ciphertext", "a")
	cache.Set(context.Background(), "b", "ciphertext", "b")
	// Over capacity: this third insert must evict the least-recently-used entry ("a").
	cache.Set(context.Background(), "c", "ciphertext", "c")

	_, found := cache.Get("a", "ciphertext")
	require.False(t, found, "least-recently-used entry must be evicted once the cache is full")

	_, found = cache.Get("b", "ciphertext")
	require.True(t, found)

	_, found = cache.Get("c", "ciphertext")
	require.True(t, found)
}

func TestNewDefault_UsableCache(t *testing.T) {
	t.Parallel()

	cache := NewDefault(discardLogger())
	cache.Set(context.Background(), "abc", "ciphertext", "https://example.com")

	got, found := cache.Get("abc", "ciphertext")
	require.True(t, found)
	require.Equal(t, "https://example.com", got)
}
