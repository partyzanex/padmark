package linkcache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/padmark/internal/domain"
)

func TestCache_SetGet_RoundTrip(t *testing.T) {
	t.Parallel()

	cache := New(10, time.Minute)
	link := &domain.Link{ID: "abc", TargetURL: "https://example.com"}

	cache.Set("abc", link)

	got, found := cache.Get("abc")
	require.True(t, found)
	require.Equal(t, "https://example.com", got.TargetURL)
}

func TestCache_Get_Miss(t *testing.T) {
	t.Parallel()

	cache := New(10, time.Minute)

	_, found := cache.Get("missing")
	require.False(t, found)
}

func TestCache_Delete(t *testing.T) {
	t.Parallel()

	cache := New(10, time.Minute)
	cache.Set("abc", &domain.Link{TargetURL: "https://example.com"})

	cache.Delete("abc")

	_, found := cache.Get("abc")
	require.False(t, found)
}

func TestCache_Delete_MissingKey_NoPanic(t *testing.T) {
	t.Parallel()

	cache := New(10, time.Minute)

	require.NotPanics(t, func() { cache.Delete("never-set") })
}

// TestCache_Get_MutatingReturnedValue_DoesNotCorruptCache verifies Get returns a clone: a caller
// mutating its own copy (e.g. links.Manager.Redirect incrementing Views) must never leak back
// into the cached entry, or a later hit would return a wrong, already-mutated value.
func TestCache_Get_MutatingReturnedValue_DoesNotCorruptCache(t *testing.T) {
	t.Parallel()

	cache := New(10, time.Minute)
	cache.Set("abc", &domain.Link{TargetURL: "https://example.com", Views: 0})

	first, found := cache.Get("abc")
	require.True(t, found)

	first.Views = 999
	first.TargetURL = "mutated"

	second, found := cache.Get("abc")
	require.True(t, found)
	require.Equal(t, 0, second.Views, "mutating the first Get's result must not affect later Gets")
	require.Equal(t, "https://example.com", second.TargetURL)
}

// TestCache_Set_MutatingCallersValue_DoesNotCorruptCache is Set's side of the same guarantee:
// mutating the caller's own link after Set must not affect what's stored.
func TestCache_Set_MutatingCallersValue_DoesNotCorruptCache(t *testing.T) {
	t.Parallel()

	cache := New(10, time.Minute)
	link := &domain.Link{TargetURL: "https://example.com"}

	cache.Set("abc", link)
	link.TargetURL = "mutated-after-set"

	got, found := cache.Get("abc")
	require.True(t, found)
	require.Equal(t, "https://example.com", got.TargetURL)
}

func TestCache_TTLExpiry(t *testing.T) {
	t.Parallel()

	cache := New(10, 10*time.Millisecond)
	cache.Set("abc", &domain.Link{TargetURL: "https://example.com"})

	_, found := cache.Get("abc")
	require.True(t, found, "entry must be present immediately after Set")

	time.Sleep(30 * time.Millisecond)

	_, found = cache.Get("abc")
	require.False(t, found, "entry must be gone once its TTL has elapsed")
}

func TestCache_LRUEviction_WhenFull(t *testing.T) {
	t.Parallel()

	cache := New(2, time.Minute)
	cache.Set("a", &domain.Link{TargetURL: "a"})
	cache.Set("b", &domain.Link{TargetURL: "b"})
	cache.Set("c", &domain.Link{TargetURL: "c"}) // over capacity: evicts the least-recently-used ("a")

	_, found := cache.Get("a")
	require.False(t, found, "least-recently-used entry must be evicted once the cache is full")

	_, found = cache.Get("b")
	require.True(t, found)

	_, found = cache.Get("c")
	require.True(t, found)
}

func TestNewDefault_UsableCache(t *testing.T) {
	t.Parallel()

	cache := NewDefault()
	cache.Set("abc", &domain.Link{TargetURL: "https://example.com"})

	got, found := cache.Get("abc")
	require.True(t, found)
	require.Equal(t, "https://example.com", got.TargetURL)
}
