package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanFileName(t *testing.T) {
	t.Parallel()

	valid := map[string]string{
		"report.pdf":             "report.pdf",
		`C:\Users\me\report.pdf`: "report.pdf",
		"../../etc/passwd":       "passwd",
		"  отчёт 2026.xlsx ":     "отчёт 2026.xlsx",
		"a\r\nb\x00.txt":         "ab.txt",
	}
	for input, want := range valid {
		got, err := CleanFileName(input)
		require.NoError(t, err, input)
		assert.Equal(t, want, got, input)
	}

	invalid := []string{"", "  ", "dir/", "..", ".", "\xff.txt", strings.Repeat("a", MaxFileNameLength+1)}
	for _, input := range invalid {
		_, err := CleanFileName(input)
		assert.ErrorIs(t, err, ErrInvalidFileName, input)
	}
}
