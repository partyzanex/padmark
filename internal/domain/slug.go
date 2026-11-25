package domain

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
)

// slugRe matches a custom slug: one or more path segments joined by "/", each starting with an
// alphanumeric and otherwise containing alphanumerics, dot, hyphen or underscore. This allows
// path-like slugs such as "project/GUIDE.md" while rejecting "..", "//", leading/trailing "/"
// and dotfiles (path-traversal-ish shapes). Overall length is bounded separately by SlugMaxLen.
var slugRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(?:/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)

// SlugMaxLen bounds the total slug length of a note, a short link or a file.
const SlugMaxLen = 100

// SlugMaxSegments bounds the number of "/"-separated path segments in a custom slug. Generous
// enough for realistic paths (e.g. "docs/api/v2/reference.md") while ruling out pathologically
// deep slugs that stay under SlugMaxLen only by using many single-character segments.
const SlugMaxSegments = 10

// ReservedSlugPrefixes returns the first path segments that name built-in routes. A custom slug
// must not start with one, or its URL would resolve to that route (e.g. "edit/foo" → the editor)
// instead of a note or link. internal/adapters/web.buildNamedRoutes uses this as the single
// source of truth for which first segments are routes rather than note/link slugs — shared here
// (not left in the notes usecase package) because both notes.Manager and links.Manager need it,
// and a usecase package may never import another usecase package.
func ReservedSlugPrefixes() map[string]struct{} {
	return map[string]struct{}{
		"login": {}, "setup": {}, "logout": {}, "totp-login": {},
		"admin": {}, "api": {}, "success": {}, "healthz": {}, "readyz": {},
		"notes": {}, "edit": {}, "change-password": {}, "static": {},
		"links": {}, "r": {}, "files": {},
		// Served file, registered per exact segment ("llms.txt", not "llms") so agent docs
		// stay reachable without blocking the "llms" segment for note slugs.
		"llms.txt": {},
		// Browsers request /favicon.ico on pages that declare no icon (raw text/markdown views).
		"favicon.ico": {},
	}
}

const (
	slugChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	// slugLength balances short, friendly URLs against URL guessing: the random slug is the only
	// secret in the URL of a public record. 10 chars over a 62-char alphabet ≈ 60 bits: safe
	// against online URL guessing, and offline guessing from the HashSlug values of a stolen
	// database costs years. Only legacy content envelopes derive their key from the slug.
	slugLength = 10

	editCodeChars  = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	editCodeLength = 12
)

func newSlug() (string, error) {
	return randomString(slugChars, slugLength)
}

// NewEditCode generates a random edit code for a newly created note or link.
func NewEditCode() (string, error) {
	return randomString(editCodeChars, editCodeLength)
}

// CustomSlugSetting is the value of the custom slug setting. Its underlying type is bool, so
// the setting is a named type and not a bare bool parameter.
type CustomSlugSetting bool

const (
	// CustomSlugsDisabled rejects user-supplied slugs.
	CustomSlugsDisabled CustomSlugSetting = false
	// CustomSlugsEnabled accepts user-supplied slugs that pass ValidCustomSlug.
	CustomSlugsEnabled CustomSlugSetting = true
)

// ResolveSlug validates or generates a slug, returning the plaintext value of the URL. HashSlug of
// it is the record ID. A user-supplied candidate is only accepted when custom is
// CustomSlugsEnabled; otherwise a non-empty candidate is rejected so callers cannot pick a short,
// guessable URL. An empty candidate always generates a fresh random slug, regardless of custom
// (custom aliases are optional, not mandatory).
func ResolveSlug(candidate string, custom CustomSlugSetting) (string, error) {
	if candidate == "" {
		slug, err := newSlug()
		if err != nil {
			return "", fmt.Errorf("generate slug: %w", err)
		}

		return slug, nil
	}

	if custom == CustomSlugsDisabled {
		return "", ErrCustomSlugDisabled
	}

	if !ValidCustomSlug(candidate) {
		if first, reserved := FirstSegmentReserved(candidate); reserved {
			return "", fmt.Errorf("slug starts with reserved route name %q (reserved: %s): %w",
				first, strings.Join(SortedReservedSlugPrefixes(), ", "), ErrInvalidSlug)
		}

		return "", ErrInvalidSlug
	}

	return candidate, nil
}

// ValidCustomSlug reports whether a user-supplied slug is acceptable: bounded length, path-like
// shape (see slugRe), a bounded number of path segments (SlugMaxSegments), and a first segment
// that does not collide with a reserved route name.
func ValidCustomSlug(slug string) bool {
	if slug == "" || len(slug) > SlugMaxLen {
		return false
	}

	if !slugRe.MatchString(slug) {
		return false
	}

	if strings.Count(slug, "/")+1 > SlugMaxSegments {
		return false
	}

	_, reserved := FirstSegmentReserved(slug)

	return !reserved
}

// FirstSegmentReserved reports whether slug's first path segment collides with a reserved route
// name, returning that segment so callers can name it in error messages.
func FirstSegmentReserved(slug string) (string, bool) {
	first, _, _ := strings.Cut(slug, "/")

	_, reserved := ReservedSlugPrefixes()[first]

	return first, reserved
}

// SortedReservedSlugPrefixes returns ReservedSlugPrefixes' keys sorted, for stable error messages.
func SortedReservedSlugPrefixes() []string {
	prefixes := ReservedSlugPrefixes()
	names := make([]string, 0, len(prefixes))

	for name := range prefixes {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}

// randomString runs on every note/link create (via newSlug/NewEditCode), not just at startup,
// so a crypto/rand failure here must propagate as an error like any other request-time failure —
// not panic. See notes.Manager.Create/links.Manager.Create's fail-hard-with-error handling of
// the returned error.
func randomString(chars string, length int) (string, error) {
	size := big.NewInt(int64(len(chars)))
	buf := make([]byte, length)

	for idx := range length {
		nn, err := rand.Int(rand.Reader, size)
		if err != nil {
			return "", fmt.Errorf("crypto/rand unavailable: %w", err)
		}

		buf[idx] = chars[nn.Int64()]
	}

	return string(buf), nil
}
