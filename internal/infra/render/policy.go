package render

import (
	"regexp"

	"github.com/microcosm-cc/bluemonday"
)

// newUGCPolicy mirrors bluemonday.UGCPolicy (v1.0.27) with exactly one deliberate difference:
// the global "id" attribute is restricted to the user-content- namespace instead of being
// accepted with an unanchored pattern.
//
// A copy is needed because bluemonday offers no way to withdraw a global attribute rule once
// UGCPolicy registered it: AllowAttrs(...).Globally() appends alternatives, so a stricter "id"
// rule added afterwards is ORed with the permissive one. TestPolicyMatchesUpstreamUGC compares
// the output of both policies on a rich sample so a bluemonday upgrade that changes the upstream
// element/attribute set shows up as a test failure here.
func newUGCPolicy() *bluemonday.Policy {
	policy := bluemonday.NewPolicy()

	// Global attributes: AllowStandardAttributes minus the permissive "id".
	policy.AllowAttrs("dir").Matching(bluemonday.Direction).Globally()
	policy.AllowAttrs("lang").Matching(regexp.MustCompile(`[a-zA-Z]{2,20}`)).Globally()
	policy.AllowAttrs("id").Matching(reUserContentID).Globally()
	policy.AllowAttrs("title").Matching(bluemonday.Paragraph).Globally()

	policy.AllowStandardURLs()

	policy.AllowElements("article", "aside")
	policy.AllowAttrs("open").Matching(regexp.MustCompile(`(?i)^(|open)$`)).OnElements("details")
	policy.AllowElements("figure", "section", "summary")
	policy.AllowElements("h1", "h2", "h3", "h4", "h5", "h6", "hgroup")

	policy.AllowAttrs("cite").OnElements("blockquote")
	policy.AllowElements("br", "div", "hr", "p", "span", "wbr")

	policy.AllowAttrs("href").OnElements("a")
	policy.AllowAttrs("name").Matching(regexp.MustCompile(`^([\p{L}\p{N}_-]+)$`)).OnElements("map")
	policy.AllowAttrs("alt").Matching(bluemonday.Paragraph).OnElements("area")
	policy.AllowAttrs("coords").Matching(regexp.MustCompile(`^(\d+,)+\d+$`)).OnElements("area")
	policy.AllowAttrs("href").OnElements("area")
	policy.AllowAttrs("rel").Matching(bluemonday.SpaceSeparatedTokens).OnElements("area")
	policy.AllowAttrs("shape").Matching(regexp.MustCompile(`(?i)^(default|circle|rect|poly)$`)).OnElements("area")
	policy.AllowAttrs("usemap").Matching(regexp.MustCompile(`(?i)^#[\p{L}\p{N}_-]+$`)).OnElements("img")

	policy.AllowElements("abbr", "acronym", "cite", "code", "dfn", "em",
		"figcaption", "mark", "s", "samp", "strong", "sub", "sup", "var")
	policy.AllowAttrs("cite").OnElements("q")
	policy.AllowAttrs("datetime").Matching(bluemonday.ISO8601).OnElements("time")
	policy.AllowElements("b", "i", "pre", "small", "strike", "tt", "u")

	policy.AllowAttrs("dir").Matching(bluemonday.Direction).OnElements("bdi", "bdo")
	policy.AllowElements("rp", "rt", "ruby")
	policy.AllowAttrs("cite").Matching(bluemonday.Paragraph).OnElements("del", "ins")
	policy.AllowAttrs("datetime").Matching(bluemonday.ISO8601).OnElements("del", "ins")

	policy.AllowLists()
	policy.AllowTables()

	policy.AllowAttrs("value", "min", "max", "low", "high", "optimum").Matching(bluemonday.Number).OnElements("meter")
	policy.AllowAttrs("value", "max").Matching(bluemonday.Number).OnElements("progress")

	policy.AllowImages()

	return policy
}
