package domain

import (
	"time"

	"github.com/google/uuid"
)

// Link is a short URL redirect. ID is HashSlug(slug); the plaintext slug is not persisted.
// New TargetURL values use an installation-key envelope; old values use slug-derived keys
// until migrated. Predictable slugs can still be guessed against the ID hash.
type Link struct {
	CreatedAt time.Time
	ExpiresAt *time.Time
	Privacy   *Privacy
	// OwnerID is the ID of the user who created the link while authenticated (session or API
	// token); nil for anonymous links. Lets Update/Delete bypass EditCode for the exact
	// creator — see links.Manager.Update/Delete, mirroring notes.Manager.Update/Delete.
	OwnerID *uuid.UUID
	// DirectRedirect overrides the redirect mode for this one link: nil means "inherit" (the
	// server flag, or direct for an owned link), non-nil forces GET /r/{id} into 301 mode (true) or
	// interstitial mode (false). An anonymous link's true is ignored unless the operator enabled
	// --direct-redirect. See EffectiveDirectRedirect.
	DirectRedirect *bool
	ID             string
	TargetURL      string // ciphertext in storage, plaintext in domain after Decrypt
	EditCode       string
	Views          int
	BurnAfterClick bool
}

// Validate checks that the link fields satisfy business rules.
func (l *Link) Validate() error {
	if l.Privacy != nil && !l.Privacy.Valid() {
		return ErrInvalidPrivacy
	}

	return nil
}

// ValidateOwnership checks that PrivacyOwner is only used on a link that actually has an owner —
// otherwise no caller, including the creator, could ever use it again (see VisibleTo/OwnedBy).
// Deliberately NOT part of Validate() — see Note.ValidateOwnership's doc comment for why: OwnerID
// is only in its final state immediately for Create, and in links.Manager.Update only after
// OwnerID is pinned from the existing link.
func (l *Link) ValidateOwnership() error {
	if l.EffectivePrivacy() == PrivacyOwner && l.OwnerID == nil {
		return ErrOwnerPrivacyRequiresOwner
	}

	return nil
}

// OwnedBy reports whether userID is the authenticated user who created l — see OwnerID. A nil
// userID (anonymous caller, uuid.Nil) or an unowned link (created anonymously) never match.
func (l *Link) OwnedBy(userID uuid.UUID) bool {
	return userID != uuid.Nil && l.OwnerID != nil && *l.OwnerID == userID
}

// EffectivePrivacy returns l.Privacy, defaulting to PrivacyPublic when unset.
func (l *Link) EffectivePrivacy() Privacy {
	if l.Privacy == nil {
		return PrivacyPublic
	}

	return *l.Privacy
}

// EffectiveDirectRedirect reports whether GET /r/{id} should answer with an immediate 301 rather
// than the transparency interstitial.
//
// serverDefault is the operator's --direct-redirect flag, which defaults to false so that an
// anonymously created link cannot turn the instance into an open redirector with the operator's
// domain reputation. The rules:
//
//   - flag on: the link's own override wins when set, otherwise 301 (the original behaviour);
//   - flag off: a link created by an authenticated owner (OwnerID set) is accountable, so it
//     redirects directly unless its own override says otherwise; an anonymous link always gets
//     the interstitial, whatever override its creator asked for.
func (l *Link) EffectiveDirectRedirect(serverDefault bool) bool {
	allowed := serverDefault || l.OwnerID != nil
	if l.DirectRedirect != nil && allowed {
		return *l.DirectRedirect
	}

	return allowed
}

// Expired reports whether l's TTL has elapsed as of now. A nil ExpiresAt means the link never
// expires.
//
// Every path that surfaces a link to a caller must consult this, because links.Manager.Peek
// deliberately returns expired links as-is (it is the pre-auth metadata read, and must not apply
// policy). Keeping the comparison here rather than repeating it per call site is what stops the
// redirect path and the API path from drifting apart on when a link counts as gone.
func (l *Link) Expired(now time.Time) bool {
	return l.ExpiresAt != nil && now.After(*l.ExpiresAt)
}

// VisibleTo reports whether a caller may resolve l given its privacy level — identical semantics
// to Note.VisibleTo, including the fail-closed default for an unrecognized stored value.
func (l *Link) VisibleTo(callerID uuid.UUID, authenticated bool) bool {
	switch l.EffectivePrivacy() {
	case PrivacyPublic:
		return true
	case PrivacyAuthenticated:
		return authenticated
	case PrivacyOwner:
		return l.OwnedBy(callerID)
	default:
		return false
	}
}
