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
	t.Parallel()

	owner := uuid.New()
	forceTrue, forceFalse := true, false

	cases := []struct {
		name          string
		link          Link
		serverDefault bool
		want          bool
	}{
		// Server default off (the default): only an authenticated owner earns the direct 301.
		{"anonymous link follows a false default", Link{}, false, false},
		{"anonymous override true is ignored without the flag", Link{DirectRedirect: &forceTrue}, false, false},
		{"owned link redirects directly", Link{OwnerID: &owner}, false, true},
		{"owned link may opt back into the interstitial", Link{OwnerID: &owner, DirectRedirect: &forceFalse}, false, false},
		{"owned override true stays direct", Link{OwnerID: &owner, DirectRedirect: &forceTrue}, false, true},
		// Operator turned the flag on: the old behaviour for everyone, per-link override included.
		{"anonymous link follows a true default", Link{}, true, true},
		{"anonymous override false wins over a true default", Link{DirectRedirect: &forceFalse}, true, false},
		{"anonymous override true with the flag on", Link{DirectRedirect: &forceTrue}, true, true},
		{"owned link with the flag on", Link{OwnerID: &owner}, true, true},
		{"owned override false wins over a true default", Link{OwnerID: &owner, DirectRedirect: &forceFalse}, true, false},
	}

	for _, testCase := range cases {
		assert.Equal(t, testCase.want, testCase.link.EffectiveDirectRedirect(testCase.serverDefault), testCase.name)
	}
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
