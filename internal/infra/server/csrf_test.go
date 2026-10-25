package server

import (
	"bytes"
	"testing"
)

// TestParseCSRFSecret covers the --csrf-secret flag: empty means "generate a random secret
// per process" (legacy behaviour), a value of at least minCSRFSecretLen bytes is passed
// through verbatim, and a short one is rejected at startup rather than accepted as a weak
// HMAC key.
func TestParseCSRFSecret(t *testing.T) {
	t.Run("empty input yields nil (random per-process secret)", func(t *testing.T) {
		got, err := parseCSRFSecret("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got != nil {
			t.Fatalf("want nil secret, got %d bytes", len(got))
		}
	})

	t.Run("value of minimum length is accepted", func(t *testing.T) {
		raw := bytes.Repeat([]byte("~"), minCSRFSecretLen)

		got, err := parseCSRFSecret(string(raw))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !bytes.Equal(got, raw) {
			t.Fatalf("want %q, got %q", raw, got)
		}
	})

	t.Run("short value is rejected", func(t *testing.T) {
		_, err := parseCSRFSecret("short")
		if err == nil {
			t.Fatal("want error for short secret, got nil")
		}
	})
}
