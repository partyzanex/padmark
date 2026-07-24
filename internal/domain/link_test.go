package domain

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLink_Validate_NilPrivacy(t *testing.T) {
	require.NoError(t, (&Link{}).Validate())
}

func TestLink_Validate_InvalidPrivacy(t *testing.T) {
	p := Privacy("secret")
	link := &Link{Privacy: &p}

	require.ErrorIs(t, link.Validate(), ErrInvalidPrivacy)
}

func TestLink_ValidateOwnership(t *testing.T) {
	owner := uuid.New()
	ownerLevel, publicLevel := PrivacyOwner, PrivacyPublic

	require.ErrorIs(t, (&Link{Privacy: &ownerLevel}).ValidateOwnership(), ErrOwnerPrivacyRequiresOwner,
		"owner-only privacy on a link with no OwnerID must be rejected")
	assert.NoError(t, (&Link{Privacy: &ownerLevel, OwnerID: &owner}).ValidateOwnership(),
		"owner-only privacy on a link with an OwnerID is fine")
	assert.NoError(t, (&Link{Privacy: &publicLevel}).ValidateOwnership(),
		"non-owner privacy never requires an OwnerID")
	assert.NoError(t, (&Link{}).ValidateOwnership(), "nil Privacy defaults to public, never requires an OwnerID")
}

func TestLink_EffectivePrivacy(t *testing.T) {
	assert.Equal(t, PrivacyPublic, (&Link{}).EffectivePrivacy(), "nil Privacy defaults to public")

	owner := PrivacyOwner
	assert.Equal(t, PrivacyOwner, (&Link{Privacy: &owner}).EffectivePrivacy())
}

func TestLink_EffectiveDirectRedirect(t *testing.T) {
	assert.True(t, (&Link{}).EffectiveDirectRedirect(true), "nil override falls back to server default (true)")
	assert.False(t, (&Link{}).EffectiveDirectRedirect(false), "nil override falls back to server default (false)")

	forceTrue, forceFalse := true, false
	assert.True(t, (&Link{DirectRedirect: &forceTrue}).EffectiveDirectRedirect(false),
		"link override wins over a false server default")
	assert.False(t, (&Link{DirectRedirect: &forceFalse}).EffectiveDirectRedirect(true),
		"link override wins over a true server default")
}

func TestLink_VisibleTo(t *testing.T) {
	owner := uuid.New()
	other := uuid.New()

	public, authenticated, ownerOnly := PrivacyPublic, PrivacyAuthenticated, PrivacyOwner
	garbage := Privacy("corrupted-value")

	tests := []struct {
		name          string
		link          *Link
		callerID      uuid.UUID
		authenticated bool
		want          bool
	}{
		{"public visible to anonymous", &Link{Privacy: &public}, uuid.Nil, false, true},
		{"public visible to authenticated", &Link{Privacy: &public}, other, true, true},
		{"nil privacy defaults to public", &Link{}, uuid.Nil, false, true},
		{"authenticated hides from anonymous", &Link{Privacy: &authenticated}, uuid.Nil, false, false},
		{
			"authenticated visible to any authenticated caller",
			&Link{Privacy: &authenticated, OwnerID: &owner}, other, true, true,
		},
		{"owner hides from anonymous", &Link{Privacy: &ownerOnly, OwnerID: &owner}, uuid.Nil, false, false},
		{"owner hides from other authenticated caller", &Link{Privacy: &ownerOnly, OwnerID: &owner}, other, true, false},
		{"owner visible to the owner", &Link{Privacy: &ownerOnly, OwnerID: &owner}, owner, true, true},
		{"owner with nil OwnerID hides from anonymous", &Link{Privacy: &ownerOnly}, uuid.Nil, false, false},
		{"owner with nil OwnerID hides from authenticated caller", &Link{Privacy: &ownerOnly}, other, true, false},
		{"unrecognized privacy value fails closed", &Link{Privacy: &garbage, OwnerID: &owner}, owner, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.link.VisibleTo(tt.callerID, tt.authenticated))
		})
	}
}

func TestLink_OwnedBy(t *testing.T) {
	owner := uuid.New()
	other := uuid.New()
	link := &Link{OwnerID: &owner}

	assert.True(t, link.OwnedBy(owner), "the exact owner must match")
	assert.False(t, link.OwnedBy(other), "a different user must not match")
	assert.False(t, link.OwnedBy(uuid.Nil), "an anonymous caller must never match")
}

func TestLink_OwnedBy_AnonymousLink(t *testing.T) {
	link := &Link{} // OwnerID nil: created anonymously, or accounts disabled

	assert.False(t, link.OwnedBy(uuid.New()), "an unowned link must never match, even an authenticated caller")
	assert.False(t, link.OwnedBy(uuid.Nil))
}
