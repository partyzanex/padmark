// Package testutil provides shared integration-test fixtures.
package testutil

import (
	"bytes"
	"testing"

	"github.com/partyzanex/padmark/internal/infra/crypto"
)

// ContentKeyring returns the fixed, public test keyring behind ContentEncryptor, for tests
// that drive the content-key migration and then read through the notes/links managers.
// Never use this keyring in a running installation.
func ContentKeyring() *crypto.ContentKeyring {
	const (
		testKeySize     = 32
		testKeyFillByte = 42
	)

	return &crypto.ContentKeyring{
		Active: "test", Keys: map[string][]byte{"test": bytes.Repeat([]byte{testKeyFillByte}, testKeySize)},
	}
}

// ContentEncryptor uses a fixed, public test key so separate fixture instances can read
// the same test database. Never use this keyring in a running installation.
func ContentEncryptor(tb testing.TB, resource string) *crypto.ContentEncryptor {
	tb.Helper()

	enc, err := ContentKeyring().ForResource(resource)
	if err != nil {
		tb.Fatalf("content cipher fixture: %v", err)
	}

	return enc
}
