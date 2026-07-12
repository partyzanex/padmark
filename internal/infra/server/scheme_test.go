package server

import "testing"

// TestParsePublicScheme covers the --public-scheme override: empty means "auto-detect",
// http/https are accepted verbatim, and anything else is rejected rather than silently ignored.
func TestParsePublicScheme(t *testing.T) {
	t.Run("empty input yields empty (auto-detect)", func(t *testing.T) {
		got, err := parsePublicScheme("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got != "" {
			t.Fatalf("want empty scheme, got %q", got)
		}
	})

	t.Run("http is accepted", func(t *testing.T) {
		got, err := parsePublicScheme("http")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got != "http" {
			t.Fatalf("want http, got %q", got)
		}
	})

	t.Run("https is accepted", func(t *testing.T) {
		got, err := parsePublicScheme("https")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got != "https" {
			t.Fatalf("want https, got %q", got)
		}
	})

	t.Run("invalid value is rejected", func(t *testing.T) {
		_, err := parsePublicScheme("ftp")
		if err == nil {
			t.Fatal("want error for invalid scheme, got nil")
		}
	})
}
