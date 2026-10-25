package mcp

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVaultPutGet_RoundTrip(t *testing.T) {
	vault := newEditCodeVault()

	vault.Put("note-a", "code-a")

	assert.Equal(t, "code-a", vault.Get("note-a"))
	assert.Empty(t, vault.Get("note-b"), "entries are isolated per id")
	assert.Empty(t, vault.Get(""), "an empty id is just another key, not a wildcard")
}

func TestVault_ConcurrentAccessIsRaceClean(t *testing.T) {
	vault := newEditCodeVault()

	var wg sync.WaitGroup

	// Handlers may create and delete notes concurrently on the same vault.
	for range 50 {
		wg.Go(func() {
			vault.Put("shared", "code")
			vault.Get("shared")
			vault.Get("missing")
		})
	}

	wg.Wait()
}
