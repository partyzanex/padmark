// Package httptestutil provides shared HTTP request fixtures.
package httptestutil

import (
	"net/http"
	"time"
)

// RequestCookie supplies a name/value pair for Request.AddCookie.
// AddCookie transmits only the name and value; response attributes stay on this object.
func RequestCookie(name, value string) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: value, Quoted: false, Path: "", Domain: "", Expires: time.Time{},
		RawExpires: "", MaxAge: 0, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Partitioned: false, Raw: "", Unparsed: nil,
	}
}
