package client

import (
	"net/url"
	"strings"
)

// This file is not generated. Keep it out of the ogen output names (oas_*_gen.go): ogen --clean
// removes only those files.

// SplitFragment returns u with a "#fragment" in the Fragment field. Call it on each url.URL
// that the generated client decodes from a "uri" field, for example LinkResponse.TargetURL.
//
// The decoder of ogen calls url.ParseRequestURI, which does not split off a fragment. The
// fragment stays in Path, with RawPath set, or in RawQuery when the URL has a query.
// url.URL.String then writes a "#" of Path as "%23". The address changes: a browser asks for
// a path with an encoded hash sign. SplitFragment restores the address that the server sent
// and parses it again with url.Parse. The server does the same for the requests that it
// receives (requestTargetURL in internal/adapters/web).
//
// An encoded "%23" in the path stays encoded: ParseRequestURI sets RawPath only for a literal
// "#". A URL without a misplaced "#", for example a URL from url.Parse, is returned without
// a change. If the restored address does not parse, SplitFragment returns u.
func SplitFragment(u url.URL) url.URL {
	if u.Fragment != "" || !hasMisplacedHash(u) {
		return u
	}

	parsed, err := url.Parse(restoreAddress(u))
	if err != nil {
		return u
	}

	return *parsed
}

// hasMisplacedHash reports whether a "#" is in a component that is not the fragment. A literal
// "#" of the path is in RawPath, never only in Path (see SplitFragment).
func hasMisplacedHash(u url.URL) bool {
	return strings.Contains(u.RawPath, "#") ||
		strings.Contains(u.RawQuery, "#") ||
		strings.Contains(u.Opaque, "#")
}

// restoreAddress writes the address that the server sent. String is correct, except when a
// literal "#" is in the path: it writes that "#" as "%23". For that case, the path comes from
// RawPath, which has the "#" as the server sent it.
func restoreAddress(u url.URL) string {
	if !strings.Contains(u.RawPath, "#") {
		return u.String()
	}

	origin := u
	origin.Path, origin.RawPath, origin.RawQuery, origin.ForceQuery = "", "", "", false

	raw := origin.String() + u.RawPath
	if u.ForceQuery || u.RawQuery != "" {
		raw += "?" + u.RawQuery
	}

	return raw
}
