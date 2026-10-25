package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// negotiateFor builds a GET request with the given Accept header and negotiates its format.
func negotiateFor(t *testing.T, accept string) format {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/notes/some-id", nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}

	return negotiate(req)
}

// TestNegotiate_RankedByQ pins the RFC 9110 ranking: the q parameter decides, not header
// position. Both inputs below previously resolved by first-match order (json, plain) while the
// client's weights said otherwise.
func TestNegotiate_RankedByQ(t *testing.T) {
	require.Equal(t, formatHTML, negotiateFor(t, "application/json;q=0.5, text/html;q=0.9"),
		"higher q must win over earlier position")
	require.Equal(t, formatJSON, negotiateFor(t, "text/plain;q=0.1, application/json;q=1"),
		"q must outrank position in the other direction too")
}

// TestNegotiate_TieBreaksBySpecificityThenOrder.
func TestNegotiate_TieBreaksBySpecificityThenOrder(t *testing.T) {
	require.Equal(t, formatHTML, negotiateFor(t, "*/*;q=0.8, text/html;q=0.8"),
		"an exact match beats a wildcard at equal q")
	require.Equal(t, formatHTML, negotiateFor(t, "text/html;q=1, application/json;q=1"),
		"header position is the final tie-breaker")
	require.Equal(t, formatJSON, negotiateFor(t, "application/json;q=1, text/html;q=1"),
		"header position is the final tie-breaker, the other way around")
}

// TestNegotiate_ZeroQualityIsUnacceptable — q=0 excludes a type, and the exclusion beats a
// wildcard that would otherwise cover it; a more specific acceptable entry lifts the veto.
func TestNegotiate_ZeroQualityIsUnacceptable(t *testing.T) {
	require.Equal(t, formatJSON, negotiateFor(t, "text/html;q=0, application/json"))
	require.Equal(t, formatJSON, negotiateFor(t, "text/html;q=0, */*"),
		"a q=0 refusal must not be resurrected by */*")
	require.Equal(t, formatJSON, negotiateFor(t, "text/*;q=0, */*;q=1"),
		"a partial-wildcard refusal beats a match-all too")

	require.Equal(t, formatHTML, negotiateFor(t, "text/*;q=0, text/html;q=1"),
		"an exact acceptable entry outranks the wildcard refusal")
}

// TestNegotiate_Wildcards covers the media ranges padmark documents for agents and browsers.
func TestNegotiate_Wildcards(t *testing.T) {
	require.Equal(t, formatJSON, negotiateFor(t, "*/*"))
	require.Equal(t, formatJSON, negotiateFor(t, "application/*"))
	require.Equal(t, formatHTML, negotiateFor(t, "text/*"),
		"formatOrder preference: HTML first when a range covers it equally with plain")
	require.Equal(t, formatPlain, negotiateFor(t, "text/*, text/plain"),
		"a more specific entry for plain wins over the range")
}

// TestNegotiate_ExactTypes — each servable format responds to its exact media types.
func TestNegotiate_ExactTypes(t *testing.T) {
	require.Equal(t, formatHTML, negotiateFor(t, "text/html"))
	require.Equal(t, formatJSON, negotiateFor(t, "application/json"))
	require.Equal(t, formatPlain, negotiateFor(t, "text/plain"))
	require.Equal(t, formatPlain, negotiateFor(t, "text/markdown"))
}

// TestNegotiate_ParametersIgnored — non-q parameters must never break the q parse.
func TestNegotiate_ParametersIgnored(t *testing.T) {
	require.Equal(t, formatHTML, negotiateFor(t, "text/html; charset=utf-8"))
	require.Equal(t, formatJSON, negotiateFor(t, "text/html; charset=utf-8; q=0.2, application/json"),
		"a q hidden behind other parameters is still honoured")
}

// TestNegotiate_QClampingAndGarbage — out-of-range q clamps to [0,1]; unparseable q degrades to
// 0 so a malformed entry can never win the negotiation.
func TestNegotiate_QClampingAndGarbage(t *testing.T) {
	require.Equal(t, formatHTML, negotiateFor(t, "text/html;q=5, application/json;q=1.5"),
		"clamped q=1 for both; header position decides")
	require.Equal(t, formatJSON, negotiateFor(t, "text/html;q=abc, application/json"))
	require.Equal(t, formatJSON, negotiateFor(t, "text/html;q=-1, application/json"))
}

// TestNegotiate_Fallbacks — the default stays application/json when nothing applicable matches,
// exactly as before the q-aware rewrite.
func TestNegotiate_Fallbacks(t *testing.T) {
	require.Equal(t, formatJSON, negotiateFor(t, ""), "missing Accept header")
	require.Equal(t, formatJSON, negotiateFor(t, " , "), "only separators")
	require.Equal(t, formatJSON, negotiateFor(t, "audio/mp3"), "an unrecognised type")
	require.Equal(t, formatJSON, negotiateFor(t, "text/*;q=0"), "everything refused")
}

// TestNegotiate_CaseAndSpacing — media ranges are case-insensitive, and the parser tolerates
// arbitrary whitespace.
func TestNegotiate_CaseAndSpacing(t *testing.T) {
	require.Equal(t, formatHTML, negotiateFor(t, "TEXT/HTML"))
	require.Equal(t, formatPlain, negotiateFor(t, " Text/Markdown ;Q=0.5"))
	require.Equal(t, formatJSON,
		negotiateFor(t, "application/json;q=0.9 , text/plain;q=0.8"))
}

// TestNegotiate_BrowsersAndMachines pins the two real-world caller shapes the privacy gate and
// the setup branch rely on: a full Chrome Accept keeps the HTML path, curl's default and an
// explicit machine Accept keep the JSON path.
func TestNegotiate_BrowsersAndMachines(t *testing.T) {
	browser := "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif," +
		"image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7"

	require.Equal(t, formatHTML, negotiateFor(t, browser))
	require.Equal(t, formatJSON, negotiateFor(t, "*/*"))
	require.Equal(t, formatJSON, negotiateFor(t, "application/json, text/plain, */*"))
}

// TestNegotiate_RawQueryWins covers the documented override: ?raw=1 forces formatPlain whatever
// the caller asks for.
func TestNegotiate_RawQueryWins(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/notes/some-id?raw=1", nil)
	req.Header.Set("Accept", "text/html")

	require.Equal(t, formatPlain, negotiate(req))
}

// TestExplicitRead: only a GET that names a served format exactly, from a document navigation,
// a fetch or a non-browser client, carries the intent a burn-after-reading read needs.
func TestExplicitRead(t *testing.T) {
	cases := []struct {
		name   string
		method string
		accept string
		dest   string
		want   bool
	}{
		{"json", http.MethodGet, "application/json", "", true},
		{"markdown", http.MethodGet, "text/markdown", "", true},
		{"plain with params", http.MethodGet, "text/plain; charset=utf-8", "", true},
		{"browser navigation", http.MethodGet, "text/html,application/xhtml+xml,*/*;q=0.8", "document", true},
		{"fetch with json", http.MethodGet, "application/json", "empty", true},
		{"missing Accept", http.MethodGet, "", "", false},
		{"match-all", http.MethodGet, "*/*", "", false},
		{"partial wildcard", http.MethodGet, "text/*", "", false},
		{"refused exact type", http.MethodGet, "application/json;q=0, */*", "", false},
		{"unserved exact type", http.MethodGet, "image/png", "", false},
		{"img subresource", http.MethodGet, "image/avif,image/webp,*/*;q=0.8", "image", false},
		{"iframe even with text/html", http.MethodGet, "text/html", "iframe", false},
		{"HEAD", http.MethodHead, "application/json", "", false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			req := httptest.NewRequest(testCase.method, "/notes/some-id", nil)
			if testCase.accept != "" {
				req.Header.Set("Accept", testCase.accept)
			}

			if testCase.dest != "" {
				req.Header.Set("Sec-Fetch-Dest", testCase.dest)
			}

			require.Equal(t, testCase.want, explicitRead(req))
		})
	}
}
