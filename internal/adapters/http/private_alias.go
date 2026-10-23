package http

import (
	"errors"

	"github.com/partyzanex/padmark/internal/adapters/http/ogenapi"
	"github.com/partyzanex/padmark/internal/domain"
)

// errPrivateConflict is the 400 reply when the deprecated `private` boolean and `privacy` disagree.
var errPrivateConflict = errors.New(
	"private conflicts with privacy: private=true requires privacy authenticated or owner, " +
		"private=false requires public",
)

// resolvePrivacy folds the deprecated v0.1.6 `private` boolean into the privacy level.
//
// Without `private` the explicit level passes through unchanged (nil stays nil). `private: true`
// means authenticated and `private: false` means public, but only when `privacy` is absent; when
// both are sent they must agree (true with authenticated or owner, false with public), otherwise
// errPrivateConflict. Silently preferring one field would let an old client's `private: true`
// become a public note, which is the bug this alias exists to prevent.
func resolvePrivacy(private ogenapi.OptBool, privacy *domain.Privacy) (*domain.Privacy, error) {
	if !private.IsSet() {
		return privacy, nil
	}

	if privacy == nil {
		level := domain.PrivacyPublic
		if private.Value {
			level = domain.PrivacyAuthenticated
		}

		return &level, nil
	}

	if private.Value == (*privacy == domain.PrivacyPublic) {
		return nil, errPrivateConflict
	}

	return privacy, nil
}

// privateFlag derives the deprecated read-only `private` response field from the privacy level.
func privateFlag(note *domain.Note) bool {
	return note.EffectivePrivacy() != domain.PrivacyPublic
}
