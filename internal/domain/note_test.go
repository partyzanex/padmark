package domain

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashSlug_DeterministicHex64(t *testing.T) {
	first := HashSlug("my-slug")
	second := HashSlug("my-slug")

	require.Equal(t, first, second, "HashSlug must be deterministic")
	require.Len(t, first, 64, "sha256 hex digest is 64 chars")
	require.NotEqual(t, HashSlug("my-slug"), HashSlug("other-slug"))

	for _, ch := range first {
		require.Contains(t, "0123456789abcdef", string(ch), "must be lowercase hex")
	}
}

func TestContentType_Valid(t *testing.T) {
	assert.True(t, ContentTypeMarkdown.Valid())
	assert.True(t, ContentTypePlain.Valid())
	assert.False(t, ContentType("text/html").Valid())
	assert.False(t, ContentType("").Valid())
}

func TestNote_Validate_TitleOptional(t *testing.T) {
	// Title is intentionally optional — an empty title must validate.
	note := &Note{Title: "", Content: "body"}

	require.NoError(t, note.Validate())
}

func TestNote_Validate_TitleTooLong(t *testing.T) {
	note := &Note{Title: strings.Repeat("a", MaxTitleLength+1)}

	require.ErrorIs(t, note.Validate(), ErrTitleTooLong)
}

func TestNote_Validate_InvalidContentType(t *testing.T) {
	ct := ContentType("text/html")
	note := &Note{Title: "ok", ContentType: &ct}

	require.ErrorIs(t, note.Validate(), ErrInvalidContentType)
}

func TestPrivacy_Valid(t *testing.T) {
	assert.True(t, PrivacyPublic.Valid())
	assert.True(t, PrivacyAuthenticated.Valid())
	assert.True(t, PrivacyOwner.Valid())
	assert.False(t, Privacy("secret").Valid())
	assert.False(t, Privacy("").Valid())
}

func TestNote_Validate_InvalidPrivacy(t *testing.T) {
	p := Privacy("secret")
	note := &Note{Title: "ok", Privacy: &p}

	require.ErrorIs(t, note.Validate(), ErrInvalidPrivacy)
}

func TestNote_ValidateOwnership(t *testing.T) {
	owner := uuid.New()
	ownerLevel, publicLevel := PrivacyOwner, PrivacyPublic

	require.ErrorIs(t, (&Note{Privacy: &ownerLevel}).ValidateOwnership(), ErrOwnerPrivacyRequiresOwner,
		"owner-only privacy on a note with no OwnerID must be rejected")
	assert.NoError(t, (&Note{Privacy: &ownerLevel, OwnerID: &owner}).ValidateOwnership(),
		"owner-only privacy on a note with an OwnerID is fine")
	assert.NoError(t, (&Note{Privacy: &publicLevel}).ValidateOwnership(),
		"non-owner privacy never requires an OwnerID")
	assert.NoError(t, (&Note{}).ValidateOwnership(), "nil Privacy defaults to public, never requires an OwnerID")
}

func TestNote_EffectivePrivacy(t *testing.T) {
	assert.Equal(t, PrivacyPublic, (&Note{}).EffectivePrivacy(), "nil Privacy defaults to public")

	owner := PrivacyOwner
	assert.Equal(t, PrivacyOwner, (&Note{Privacy: &owner}).EffectivePrivacy())
}

func TestNote_VisibleTo(t *testing.T) {
	owner := uuid.New()
	other := uuid.New()

	public, authenticated, ownerOnly := PrivacyPublic, PrivacyAuthenticated, PrivacyOwner

	tests := []struct {
		name          string
		note          *Note
		callerID      uuid.UUID
		authenticated bool
		want          bool
	}{
		{"public visible to anonymous", &Note{Privacy: &public}, uuid.Nil, false, true},
		{"public visible to authenticated", &Note{Privacy: &public}, other, true, true},
		{"nil privacy defaults to public", &Note{}, uuid.Nil, false, true},
		{"authenticated hides from anonymous", &Note{Privacy: &authenticated}, uuid.Nil, false, false},
		{
			"authenticated visible to any authenticated caller",
			&Note{Privacy: &authenticated, OwnerID: &owner}, other, true, true,
		},
		{"owner hides from anonymous", &Note{Privacy: &ownerOnly, OwnerID: &owner}, uuid.Nil, false, false},
		{"owner hides from other authenticated caller", &Note{Privacy: &ownerOnly, OwnerID: &owner}, other, true, false},
		{"owner visible to the owner", &Note{Privacy: &ownerOnly, OwnerID: &owner}, owner, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.note.VisibleTo(tt.callerID, tt.authenticated))
		})
	}
}

func TestNote_OwnedBy(t *testing.T) {
	owner := uuid.New()
	other := uuid.New()
	note := &Note{OwnerID: &owner}

	assert.True(t, note.OwnedBy(owner), "the exact owner must match")
	assert.False(t, note.OwnedBy(other), "a different user must not match")
	assert.False(t, note.OwnedBy(uuid.Nil), "an anonymous caller must never match")
}

func TestNote_OwnedBy_AnonymousNote(t *testing.T) {
	note := &Note{} // OwnerID nil: created anonymously, or accounts disabled

	assert.False(t, note.OwnedBy(uuid.New()), "an unowned note must never match, even an authenticated caller")
	assert.False(t, note.OwnedBy(uuid.Nil))
}
