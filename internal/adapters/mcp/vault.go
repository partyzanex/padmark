package mcp

import "sync"

// editCodeVault keeps edit codes returned by create-note/create-link for the lifetime of the
// live MCP session. The padmark contract returns an edit_code exactly once; storing it here
// lets update/delete tools act on the same resource later without the model carrying the
// secret in its transcript. Only memory is guarded by the mutex.
type editCodeVault struct {
	codes map[string]string
	mu    sync.RWMutex
}

func newEditCodeVault() *editCodeVault {
	return &editCodeVault{
		codes: make(map[string]string),
		mu:    sync.RWMutex{},
	}
}

func (v *editCodeVault) Put(id, code string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.codes[id] = code
}

func (v *editCodeVault) Get(id string) string {
	v.mu.RLock()
	defer v.mu.RUnlock()

	return v.codes[id]
}

// editCodeFor returns the effective edit code for a resource: the explicit input wins, then
// the code the proxy stored at creation.
func editCodeFor(v *editCodeVault, id string, explicit *string) string {
	if explicit != nil && *explicit != "" {
		return *explicit
	}

	return v.Get(id)
}
