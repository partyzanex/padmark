package http_test

import (
	"crypto/sha256"
	"encoding/hex"
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
)

// redocBundlePath is where the vendored Redoc bundle is served from (same origin, global CSP).
const redocBundlePath = "/static/redoc.standalone.js"

var (
	// scriptSrcAttrRe captures the src attribute of every <script> tag on a page.
	scriptSrcAttrRe = regexp.MustCompile(`<script[^>]*\ssrc="([^"]*)"`)
	// cspNonceRe matches the per-request nonce source so two CSPs can be compared structurally.
	cspNonceRe = regexp.MustCompile(`'nonce-[^']*'`)
	// scriptSrcDirectiveRe captures the script-src directive of a CSP.
	scriptSrcDirectiveRe = regexp.MustCompile(`(?:^|;\s*)script-src ([^;]*)`)
)

func (s *HandlerSuite) serve(path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	return rec
}

// TestAPIDocs_ScriptsAreSameOrigin verifies GET /api pulls every script from the app's own
// /static/ tree: a third-party CDN (or its `latest` tag) must not be able to run same-origin JS
// on a page an admin visits with a session cookie.
func (s *HandlerSuite) TestAPIDocs_ScriptsAreSameOrigin() {
	rec := s.serve("/api")
	s.Require().Equal(http.StatusOK, rec.Code)

	body := rec.Body.String()
	s.NotContains(body, `<script src="http`, "/api must not load scripts over an absolute URL")
	s.NotContains(body, "https://", "/api must not reference any external origin")
	s.NotContains(body, "//cdn", "/api must not reference a CDN")

	srcs := scriptSrcAttrRe.FindAllStringSubmatch(body, -1)
	s.Require().NotEmpty(srcs, "/api must load the Redoc bundle via <script src>")

	for _, match := range srcs {
		s.True(strings.HasPrefix(match[1], "/static/"),
			"script src %q must be a same-origin /static/ path", match[1])
	}

	s.Contains(body, `src="`+redocBundlePath+`?p=`+s.redocBundleVersion()+`"`,
		"/api must load the vendored Redoc bundle with a content-hash cache-busting query")
}

// redocBundleVersion returns the cache-busting token for the served Redoc bundle: the first 12
// hex chars of its sha256, the same scheme as style.css?p=.
func (s *HandlerSuite) redocBundleVersion() string {
	rec := s.serve(redocBundlePath)
	s.Require().Equal(http.StatusOK, rec.Code)

	sum := sha256.Sum256(rec.Body.Bytes())

	return hex.EncodeToString(sum[:])[:12]
}

// TestCSP_APIDocsMatchesGlobal verifies /api is served under the exact global CSP: no
// third-party origin and script-src restricted to 'self' plus the per-request nonce. It replaces
// the former TestCSP_RedocScopedToAPIDocs, which asserted the opposite (that /api widened
// script-src with https://cdn.redoc.ly) before Redoc was vendored into static/.
func (s *HandlerSuite) TestCSP_APIDocsMatchesGlobal() {
	apiRec := s.serve("/api")
	homeRec := s.serve("/")

	apiCSP := apiRec.Header().Get("Content-Security-Policy")
	homeCSP := homeRec.Header().Get("Content-Security-Policy")

	s.Require().NotEmpty(apiCSP, "/api CSP must be set")
	s.Require().NotEmpty(homeCSP, "global CSP must be set")

	s.NotContains(apiCSP, "cdn.redoc.ly", "/api CSP must not allow the Redoc CDN")
	s.NotContains(homeCSP, "cdn.redoc.ly", "global CSP must not allow the Redoc CDN")
	s.Equal(cspNonceRe.ReplaceAllString(homeCSP, "'nonce-N'"), cspNonceRe.ReplaceAllString(apiCSP, "'nonce-N'"),
		"/api CSP must equal the global CSP (modulo the per-request nonce)")

	scriptSrc := scriptSrcDirectiveRe.FindStringSubmatch(apiCSP)
	s.Require().Len(scriptSrc, 2, "/api CSP must have a script-src directive")

	nonce := cspNonceRe.FindString(scriptSrc[1])
	s.Require().NotEmpty(nonce, "script-src must carry a nonce")
	s.Equal("'self' "+nonce, strings.TrimSpace(scriptSrc[1]), "script-src must be exactly 'self' + nonce")

	nonceValue := strings.TrimSuffix(strings.TrimPrefix(nonce, "'nonce-"), "'")
	wantTag := `src="` + redocBundlePath + `?p=` + s.redocBundleVersion() + `" nonce="` + nonceValue + `"`
	// html/template entity-encodes '+' in attributes (&#43;); the browser decodes it back.
	s.Contains(html.UnescapeString(apiRec.Body.String()), wantTag, "the Redoc bundle must carry the CSP nonce")
}

// TestStatic_RedocBundle verifies the vendored Redoc bundle is embedded and served from /static/.
func (s *HandlerSuite) TestStatic_RedocBundle() {
	rec := s.serve(redocBundlePath)
	s.Require().Equal(http.StatusOK, rec.Code)

	contentType := rec.Header().Get("Content-Type")
	s.True(strings.HasPrefix(contentType, "text/javascript") ||
		strings.HasPrefix(contentType, "application/javascript"),
		"unexpected Content-Type %q", contentType)

	body := rec.Body.String()
	s.Require().NotEmpty(body)

	const banner = "/*! For license information please see redoc.standalone.js.LICENSE.txt */\n!function("
	s.True(strings.HasPrefix(body, banner), "body must be the Redoc standalone bundle")
}
