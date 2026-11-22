package domain

import (
	"time"

	"github.com/google/uuid"
)

// Reader identifies the caller of a read, an update or a delete of a note, a short link or a
// file. The adapter fills it once for each request; the usecase applies the access rules with it.
type Reader struct {
	// CallerID is the account ID of the caller, or uuid.Nil for an anonymous caller.
	CallerID uuid.UUID
	// Authenticated is true when the caller has a session or an API token.
	Authenticated bool
	// AllowBurn is true for a deliberate access. Only such an access can destroy a burn record
	// or start the burn timer of a note. Other accesses get ErrBurnIntentRequired.
	AllowBurn bool
	// SkipPrivacy turns off the privacy check. The server sets it when the account system is
	// off: then nobody can sign in, and no privacy level applies. It does not change the owner
	// and edit-code checks of an update or a delete.
	SkipPrivacy bool
}

// Sees reports whether the reader can see a record with the privacy level privacy and the owner
// ownerID. PrivacyOwner needs the owner, PrivacyAuthenticated needs a signed-in caller, and
// PrivacyPublic permits each caller. An unknown stored value denies access (fail-closed).
func (r Reader) Sees(privacy Privacy, ownerID *uuid.UUID) bool {
	if r.SkipPrivacy {
		return true
	}

	switch privacy {
	case PrivacyPublic:
		return true
	case PrivacyAuthenticated:
		return r.Authenticated
	case PrivacyOwner:
		return ownedBy(ownerID, r.CallerID)
	default:
		return false
	}
}

// ownedBy reports whether userID is the owner ownerID. An anonymous caller (uuid.Nil) or a
// record without an owner never match.
func ownedBy(ownerID *uuid.UUID, userID uuid.UUID) bool {
	return userID != uuid.Nil && ownerID != nil && *ownerID == userID
}

// expired reports whether expiresAt is before now. A nil expiresAt never expires. Notes, links
// and files use this one comparison.
func expired(expiresAt *time.Time, now time.Time) bool {
	return expiresAt != nil && now.After(*expiresAt)
}
