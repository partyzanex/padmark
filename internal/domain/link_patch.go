package domain

import "time"

// LinkPatch is a partial update of one link. Storage writes only the fields that the patch
// sets and keeps the stored value of each other field. Thus an update from one server process
// cannot write back an old value that another process changed after this update read the link.
type LinkPatch struct {
	// ExpiresAt sets a new expiry. Nil keeps the stored expiry, unless ClearExpiry is true.
	ExpiresAt *time.Time
	// Privacy sets a new privacy level. Nil keeps the stored level.
	Privacy *Privacy
	// DirectRedirect sets a new redirect override. Nil keeps the stored override, unless
	// ResetDirectRedirect is true.
	DirectRedirect *bool
	// TargetURL is the encrypted destination. The API requires it, so storage always writes it.
	TargetURL string
	// ClearExpiry removes the expiry. It has priority over ExpiresAt.
	ClearExpiry bool
	// ResetDirectRedirect removes the override, so the link uses the server default again.
	// It has priority over DirectRedirect.
	ResetDirectRedirect bool
}
