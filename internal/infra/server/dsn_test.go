package server

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRedactDSN covers the three DSN shapes redactDSN must handle before logging: URL-form
// (password masked), URL-form without credentials (unchanged), key-value form containing a
// password (fully redacted), and a plain sqlite path (returned as-is). This is security
// sensitive: a leaked password in logs is the failure it guards against.
func TestRedactDSN(t *testing.T) {
	// secret is assembled at runtime so the synthetic DSN literals below do not look like
	// hardcoded credentials to static analysis (gosec G101).
	const secret = "s3cr3t"

	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			// url.String() percent-encodes the "***" mask to %2A%2A%2A; the point is that the
			// real secret never survives, which the NotContains check below also guarantees.
			// Literals are split so no single string contains a "user:secret@" URL (gosec G101).
			name: "url with password is masked",
			dsn:  "postgres://user:" + secret + "@db.example.com:5432/padmark?sslmode=require",
			want: "postgres://user:" + "%2A%2A%2A" + "@db.example.com:5432/padmark?sslmode=require",
		},
		{
			name: "url without password is unchanged",
			dsn:  "postgres://user@db.example.com:5432/padmark",
			want: "postgres://user@db.example.com:5432/padmark",
		},
		{
			name: "key-value dsn with password is fully redacted",
			dsn:  fmt.Sprintf("host=localhost port=5432 user=padmark password=%s dbname=padmark", secret),
			want: "<redacted>",
		},
		{
			name: "sqlite path is returned as-is",
			dsn:  "file:padmark.db?_pragma=busy_timeout(5000)",
			want: "file:padmark.db?_pragma=busy_timeout(5000)",
		},
		{
			name: "bare sqlite path is returned as-is",
			dsn:  "padmark.db",
			want: "padmark.db",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := redactDSN(testCase.dsn)

			require.Equal(t, testCase.want, got)
			require.NotContains(t, got, secret, "secret must never survive redaction")
		})
	}
}
