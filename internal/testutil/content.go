// Package testutil provides shared integration-test fixtures.
package testutil

import (
	"bytes"
	"testing"

	"github.com/partyzanex/padmark/internal/infra/crypto"
)

// ContentEncryptor uses a fixed, public test key so separate fixture instances can read
// the same test database. Never use this keyring in a running installation.
func ContentEncryptor(tb testing.TB, resource string) *crypto.ContentEncryptor {
	tb.Helper()

	const testKeySize = 32

	keyring := crypto.ContentKeyring{
		Active: "test", Keys: map[string][]byte{"test": bytes.Repeat([]byte{42}, testKeySize)},
	}

	enc, err := keyring.ForResource(resource)
	if err != nil {
		tb.Fatalf("content cipher fixture: %v", err)
	}

	return enc
}
