package sqlite

// The SQL form of the expiry rule of domain.Note.Expired, domain.Link.Expired and
// domain.File.Expired: a record is expired from its expires_at moment on. The parameter is the
// time of the caller's clock; storage never reads the clock itself.
const (
	// liveWhere selects a record that is not expired at the time parameter.
	liveWhere = "expires_at IS NULL OR expires_at > ?"
	// expiredWhere selects a record that is expired at the time parameter.
	expiredWhere = "expires_at IS NOT NULL AND expires_at <= ?"
)
