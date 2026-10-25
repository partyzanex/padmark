//go:build integration

package upgrade_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// legacyNote is one note of the v0.1.6 fixture (tests/fixtures/v0.1.6/README.md), exactly as
// the real v0.1.6 binary stored it.
type legacyNote struct {
	slug        string
	editCode    string
	title       string
	content     string
	contentType string
	createdAt   string // RFC 3339, as dumped
	expiresAt   string // RFC 3339; empty = no expiry
	burnTTL     int64
	views       int
	burn        bool
	private     bool
	// untaggedBySchema marks a note the schema migrations cannot recognise as v0.1.6 data.
	untaggedBySchema bool
}

// Scenario indexes (README.md "Scenario").
const (
	noteUnicode = iota
	noteCustomSlug
	notePrivate
	noteBurn
	noteFarFuture
	noteExpired
	noteViewed
	noteCount
)

const (
	customSlug     = "my-custom_Slug-1"
	customEditCode = "hunter2"
	// unicodeContent starts with an envelope-looking tag and ends with a base64 line: neither
	// may make the converter treat the plaintext as ciphertext.
	unicodeContent = "pmc2$p$fake is plain text that only looks like an envelope tag\n" +
		"# Привет, мир 🌍 — ünïcödé\n\nPrice: $42, shell: `echo $HOME`\n\n" +
		"SGVsbG8sIHBhZG1hcmsgdjAuMS42IHBsYWludGV4dCBub3Rl\n"
	// base64Content is entirely canonical standard base64: before the legacy tag it was
	// indistinguishable from raw legacy ciphertext.
	base64Content = "SGVsbG8sIHBhZG1hcmsgdjAuMS42IHBsYWludGV4dCBub3Rl"
	farFutureTTL  = 315360000
)

// dialectFixture holds the values the v0.1.6 server generated for one dump: random slugs,
// generated edit codes and timestamps.
type dialectFixture struct {
	path      string
	slugs     [noteCount]string
	editCodes [noteCount]string
	createdAt [noteCount]string
	expires5  string
	expires6  string
}

func sqliteFixture() dialectFixture {
	return dialectFixture{
		path: "../fixtures/v0.1.6/sqlite.sql",
		slugs: [noteCount]string{
			"hhyij667uv", customSlug, "82xn2a7pjg", "ew30mehhgc", "laswfgz6p8", "48fv04c55i", "vma86eg2i3",
		},
		editCodes: [noteCount]string{
			"rbHkuXQXtZ1t", customEditCode, "aXdEg4i0pMWF", "giy5UADOyaH0", "AuoENnsWIUns", "XpwoUj6sTQrK",
			"psG26TQ5Rxne",
		},
		createdAt: [noteCount]string{
			"2026-10-23T00:12:31.479833Z", "2026-10-23T00:12:31.499617Z", "2026-10-23T00:12:31.517539Z",
			"2026-10-23T00:12:31.534146Z", "2026-10-23T00:12:31.550259Z", "2026-10-23T00:12:31.568828Z",
			"2026-10-23T00:12:31.586433Z",
		},
		expires5: "2036-10-20T00:12:31.602118Z",
		expires6: "2026-10-23T00:12:32.614163Z",
	}
}

func postgresFixture() dialectFixture {
	return dialectFixture{
		path: "../fixtures/v0.1.6/postgres.sql",
		slugs: [noteCount]string{
			"humo6zl1b3", customSlug, "hlfqtq3hbu", "k18ti00u4c", "kbv4rbbyo3", "od34bxkz3h", "xjr0nb16kk",
		},
		editCodes: [noteCount]string{
			"k64ME2eRHxVG", customEditCode, "YbCBOeWRdZFq", "qX1zEDumDqQk", "NjU2rDbqn1x1", "4fbyuOALkCQk",
			"4lHg2NJHRoWl",
		},
		createdAt: [noteCount]string{
			"2026-10-23T00:12:49.175006Z", "2026-10-23T00:12:49.195397Z", "2026-10-23T00:12:49.214388Z",
			"2026-10-23T00:12:49.232445Z", "2026-10-23T00:12:49.252975Z", "2026-10-23T00:12:49.27025Z",
			"2026-10-23T00:12:49.286155Z",
		},
		expires5: "2036-10-20T00:12:49.299821Z",
		expires6: "2026-10-23T00:12:50.314342Z",
	}
}

// notes combines the dialect's generated values with the scenario both dumps share.
func (f *dialectFixture) notes() []legacyNote {
	notes := []legacyNote{
		{title: "1 unicode and dollars", content: unicodeContent, contentType: "text/markdown"},
		{title: "2 custom slug", content: "Custom slug, caller-chosen edit code.", contentType: "text/markdown"},
		{title: "3 private", content: "private <b>plain</b> text", contentType: "text/plain", private: true},
		{title: "4 burn after reading", content: "read me once", contentType: "text/markdown", burn: true},
		{
			title: "5 far future expiry", content: "expires in ten years", contentType: "text/markdown",
			burnTTL: farFutureTTL, expiresAt: f.expires5,
		},
		{
			title: "6 already expired", content: "expired before the dump", contentType: "text/markdown",
			burnTTL: 1, expiresAt: f.expires6,
		},
		{title: "7 viewed once", content: base64Content, contentType: "text/markdown", views: 1},
	}

	for i := range notes {
		notes[i].slug = f.slugs[i]
		notes[i].editCode = f.editCodes[i]
		notes[i].createdAt = f.createdAt[i]
	}

	return notes
}

// Review R1 scenario: v0.1.6 accepted any caller-chosen slug and edit code, so a custom slug of
// 64 hex characters (which looks like a hashed ID) can carry a code that looks like a stored hash.
// These notes are inserted into the dump with the v0.1.6 schema, before the schema migrations.
const (
	noteHexArgonPrefix = noteCount + iota // edit code "v1$...": tagged by the full-shape check
	noteHexFastPrefix                     // edit code "s1$...": tagged by the full-shape check
	noteHexHashShaped                     // edit code of the exact "s1$" hash shape: needs mark-legacy
	noteWeakEditCode                      // caller-chosen 12-char alphanumeric code: must use argon2id
)

func weakEditCodeNote() legacyNote {
	return legacyNote{
		slug: "weak-custom-edit-code", editCode: "password1234", title: "11 weak alphanumeric edit code",
		content: "Caller-chosen code with the same shape as a generated code.", contentType: "text/plain",
		createdAt: "2026-10-23T00:13:00.000004Z",
	}
}

func hexSlugNotes() []legacyNote {
	hashShaped := "s1$" + strings.Repeat("A", 22) + "$" + strings.Repeat("B", 43)

	return []legacyNote{
		{
			slug: strings.Repeat("a", 64), editCode: "v1$old-user-code", title: "8 hex slug, v1$ code",
			content: "64-hex custom slug, code starts like an argon2id hash", contentType: "text/markdown",
			createdAt: "2026-10-23T00:13:00.000001Z",
		},
		{
			slug: strings.Repeat("b", 64), editCode: "s1$old-user-code", title: "9 hex slug, s1$ code",
			content: "64-hex custom slug, code starts like a fast hash", contentType: "text/plain",
			createdAt: "2026-10-23T00:13:00.000002Z",
		},
		{
			slug: strings.Repeat("c", 64), editCode: hashShaped, title: "10 hex slug, hash-shaped code",
			content: "64-hex custom slug, code has the exact shape of a stored hash", contentType: "text/markdown",
			createdAt: "2026-10-23T00:13:00.000003Z", untaggedBySchema: true,
		},
	}
}

// insertV016Notes stores notes with the v0.1.6 schema exactly as the v0.1.6 server did: the slug
// as id and plaintext content and edit code.
func insertV016Notes(t *testing.T, db *bun.DB, notes []legacyNote) {
	t.Helper()

	for i := range notes {
		note := &notes[i]
		created := parseTime(t, note.createdAt)
		_, err := db.NewRaw(`INSERT INTO notes (id, title, content, content_type, edit_code, views,
 burn_after_reading, burn_ttl, expires_at, created_at, updated_at, private)
 VALUES (?, ?, ?, ?, ?, 0, ?, 0, NULL, ?, ?, ?)`,
			note.slug, note.title, note.content, note.contentType, note.editCode, note.burn, created, created,
			note.private).Exec(t.Context())
		require.NoError(t, err, note.title)
	}
}

func parseTime(t *testing.T, value string) time.Time {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339Nano, value)
	require.NoError(t, err)

	return parsed
}
