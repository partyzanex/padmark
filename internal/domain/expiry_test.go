package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestExpired_OneBoundaryForAllRecords pins the one expiry rule of notes, links, files and API
// tokens: a record is expired from its ExpiresAt moment on, and live one nanosecond before it.
// The SQL conditions of the repositories use the same rule (tests/storage pins them).
func TestExpired_OneBoundaryForAllRecords(t *testing.T) {
	t.Parallel()

	expires := time.Date(2026, time.November, 20, 12, 0, 0, 0, time.UTC)
	records := map[string]func(*time.Time, time.Time) bool{
		"note": func(at *time.Time, now time.Time) bool {
			note := new(Note)
			note.ExpiresAt = at

			return note.Expired(now)
		},
		"link": func(at *time.Time, now time.Time) bool {
			link := new(Link)
			link.ExpiresAt = at

			return link.Expired(now)
		},
		"file": func(at *time.Time, now time.Time) bool {
			file := new(File)
			file.ExpiresAt = at

			return file.Expired(now)
		},
		"api token": func(at *time.Time, now time.Time) bool {
			token := new(APIToken)
			token.ExpiresAt = at

			return token.Expired(now)
		},
	}

	for name, isExpired := range records {
		assert.False(t, isExpired(&expires, expires.Add(-time.Nanosecond)), "%s: live before ExpiresAt", name)
		assert.True(t, isExpired(&expires, expires), "%s: expired at ExpiresAt", name)
		assert.True(t, isExpired(&expires, expires.Add(time.Nanosecond)), "%s: expired after ExpiresAt", name)
		assert.False(t, isExpired(nil, expires), "%s: a nil ExpiresAt never expires", name)
	}
}
