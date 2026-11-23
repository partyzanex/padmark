package ui_test

import (
	"time"

	"github.com/stretchr/testify/require"
)

const (
	// noteExpiredMessage and linkExpiredMessage are the texts of the note and link editors for the
	// 410 reply to an update of an expired record. The server message is the word "expired".
	noteExpiredMessage = "The note was deleted or its lifetime ended."
	linkExpiredMessage = "The link was deleted or its lifetime ended."
	// expiringCode is the edit code of the records of testExpiredEditErrors.
	expiringCode = "Expiring-Code-42!"
	// expiringTTL is the lifetime of these records. It must be longer than the load of the editor.
	expiringTTL = 5 * time.Second
)

// testExpiredEditErrors: the editor of a note and of a short link stays open after the record
// expired. Save then gets 410, and the form shows the reason instead of a silent failure.
func testExpiredEditErrors(sess *uiSession) {
	sess.post("/notes", map[string]any{
		"content": "Short life", "slug": "expiring-note", "ttl": int(expiringTTL.Seconds()), "edit_code": expiringCode,
	})
	sess.saveAfterExpiry("/edit/expiring-note", time.Now().Add(expiringTTL), "#noteError", noteExpiredMessage)

	sess.post("/links", map[string]any{
		"target_url": "https://example.com/short", "slug": "expiring-link", "ttl": int(expiringTTL.Seconds()),
		"edit_code": expiringCode,
	})
	sess.saveAfterExpiry("/edit/expiring-link", time.Now().Add(expiringTTL), "#linkError", linkExpiredMessage)
}

// saveAfterExpiry opens the editor at path while the record is live, enters its edit code, waits
// until expiredAt and saves. The server set the expiry before its reply to the create request,
// so the record is expired after expiredAt. The form must show the reason message in
// errorSelector.
func (sess *uiSession) saveAfterExpiry(path string, expiredAt time.Time, errorSelector, message string) {
	sess.t.Helper()
	sess.gotoPage(path)
	sess.openSettings()
	sess.fill("#editCodeInput", expiringCode)
	sess.closeSettings()
	sess.awaitTime(expiredAt)
	sess.button("Save")
	sess.expectError(errorSelector, message)
}

// awaitTime waits until the clock passes moment. The records expire on the server clock, and
// the server runs on the same machine. A read would delete an expired note, so the test waits
// for the clock and does not poll the record.
func (sess *uiSession) awaitTime(moment time.Time) {
	sess.t.Helper()
	require.Eventually(sess.t, func() bool { return time.Now().After(moment) }, expiryWait, expiryPoll)
}
