package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func accessFile(privacy *Privacy, owner *uuid.UUID) *File {
	return &File{
		CreatedAt: time.Now(), ExpiresAt: nil, Privacy: privacy, OwnerID: owner,
		SealedMeta: "", ObjectKey: "", ID: "", Name: "", EditCode: "", ContentKey: nil,
		Size: 0, Downloads: 0, BurnAfterDownload: false,
	}
}

func TestFile_OwnedBy(t *testing.T) {
	t.Parallel()

	owner := uuid.New()

	assert.True(t, accessFile(nil, &owner).OwnedBy(owner))
	assert.False(t, accessFile(nil, &owner).OwnedBy(uuid.New()), "another user")
	assert.False(t, accessFile(nil, nil).OwnedBy(owner), "an anonymous file has no owner")

	nilOwner := uuid.Nil
	assert.False(t, accessFile(nil, &nilOwner).OwnedBy(uuid.Nil), "the nil ID never owns a file")
}

func TestFile_ValidateOwnership(t *testing.T) {
	t.Parallel()

	owner := uuid.New()

	require.ErrorIs(t, accessFile(new(PrivacyOwner), nil).ValidateOwnership(), ErrOwnerPrivacyRequiresOwner)
	require.NoError(t, accessFile(new(PrivacyOwner), &owner).ValidateOwnership())
	require.NoError(t, accessFile(new(PrivacyAuthenticated), nil).ValidateOwnership())
	require.NoError(t, accessFile(nil, nil).ValidateOwnership(), "no privacy is public")
}

func TestFile_Expired(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	file := accessFile(nil, nil)

	assert.False(t, file.Expired(now), "a nil expiry never expires")

	past := now.Add(-time.Second)
	file.ExpiresAt = &past
	assert.True(t, file.Expired(now))

	file.ExpiresAt = &now
	assert.False(t, file.Expired(now), "the expiry moment itself is not expired yet")

	future := now.Add(time.Second)
	file.ExpiresAt = &future
	assert.False(t, file.Expired(now))
}

func TestFile_VisibleTo(t *testing.T) {
	t.Parallel()

	owner := uuid.New()
	other := uuid.New()

	cases := []struct {
		name          string
		privacy       *Privacy
		owner         *uuid.UUID
		caller        uuid.UUID
		authenticated bool
		want          bool
	}{
		{"nil privacy is public", nil, nil, uuid.Nil, false, true},
		{"public to anonymous", new(PrivacyPublic), nil, uuid.Nil, false, true},
		{"authenticated to anonymous", new(PrivacyAuthenticated), nil, uuid.Nil, false, false},
		{"authenticated to user", new(PrivacyAuthenticated), nil, other, true, true},
		{"owner to owner", new(PrivacyOwner), &owner, owner, true, true},
		{"owner to other user", new(PrivacyOwner), &owner, other, true, false},
		{"owner to anonymous", new(PrivacyOwner), &owner, uuid.Nil, false, false},
		{"owner file without owner", new(PrivacyOwner), nil, uuid.Nil, true, false},
		{"unknown value fails closed", new(Privacy("secret")), &owner, owner, true, false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			file := accessFile(testCase.privacy, testCase.owner)
			assert.Equal(t, testCase.want, file.VisibleTo(testCase.caller, testCase.authenticated))
		})
	}
}
