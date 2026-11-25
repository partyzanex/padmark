package auth

import "time"

// testNow is the clock of the tests: it always returns the same time. Tests compute expiry
// times from it, so their results do not depend on the real clock.
func testNow() time.Time {
	return time.Date(2026, time.November, 20, 12, 0, 0, 0, time.UTC)
}
