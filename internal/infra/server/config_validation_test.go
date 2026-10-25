package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServerRejectsInvalidSecurityConfigurationBeforeOpeningDB(t *testing.T) {
	const (
		flagBoundsErr = "cost must be between 1 and "
		argon2Err     = "validate argon2 configuration: argon2 cost outside supported bounds"
	)

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "unknown log level", args: []string{"--log-level", "debig"}, wantErr: `invalid log level "debig"`},
		{name: "negative memory", args: []string{"--argon2-memory", "-1"}, wantErr: flagBoundsErr + "262144"},
		{name: "memory above max", args: []string{"--argon2-memory", "262145"}, wantErr: flagBoundsErr + "262144"},
		{name: "time overflowing uint32", args: []string{"--argon2-time", "4294967297"}, wantErr: flagBoundsErr + "10"},
		{name: "threads above max", args: []string{"--argon2-threads", "257"}, wantErr: flagBoundsErr + "16"},
		{name: "zero threads", args: []string{"--argon2-threads", "0"}, wantErr: flagBoundsErr + "16"},
		{
			name:    "memory below 8 KiB per thread",
			args:    []string{"--argon2-memory", "1", "--argon2-threads", "2"},
			wantErr: argon2Err,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			dsn := filepath.Join(dir, "padmark.db")
			args := append([]string{"padmark", "serve", "--dsn", dsn}, testCase.args...)

			err := NewApp().Run(t.Context(), args)

			require.Error(t, err)
			require.ErrorContains(t, err, testCase.wantErr)
			require.NotContains(t, err.Error(), "keyring", "configuration error must precede keyring startup")
			require.NoFileExists(t, dsn, "configuration error must precede opening the database")

			entries, readErr := os.ReadDir(dir)
			require.NoError(t, readErr)
			require.Empty(t, entries, "no database (or journal) file may be created")
		})
	}
}
