package render

import (
	"encoding/base64"
	"net/url"
	"regexp"

	"github.com/microcosm-cc/bluemonday"
)

// reLinkURL admits relative URLs and the http, https and mailto schemes. bluemonday checks the
// scheme of a URL with one rule for all elements, so this pattern keeps the data: scheme, which
// the policy admits for images, out of links and quotation sources.
var reLinkURL = regexp.MustCompile(`(?i)^\s*(?:(?:https?|mailto):|[^:/?#]*(?:[/?#]|$))`)

// reRasterDataURI admits the media types of a data: image. SVG is not in the list: it is a
// document that can hold scripts and expensive drawing instructions, and no SVG sanitizer is
// available here. bluemonday.AllowDataURIImages is not used because it admits SVG too.
var reRasterDataURI = regexp.MustCompile(`(?i)^image/(?:gif|jpeg|png|webp);base64,`)

// reRasterType finds a raster image type at the start of a data: URI, whatever follows it.
var reRasterType = regexp.MustCompile(`(?i)^image/(?:gif|jpeg|png|webp)[;,]`)

// reSVGType finds the SVG type at the start of a data: URI.
var reSVGType = regexp.MustCompile(`(?i)^image/svg\+xml[;,]`)

// Reasons why a note page does not show an image from a data: URI. The editor preview uses the
// same keys (editor-preview.js), and the server writes them to its log instead of the URI, which
// is user content.
const (
	// DataImageInvalid: the URI cannot be parsed.
	DataImageInvalid = "invalid"
	// DataImageAddress: the URI has a query, an empty one too, or a fragment.
	DataImageAddress = "address"
	// DataImageSVG: the image is SVG.
	DataImageSVG = "svg"
	// DataImageType: the URI holds something other than a GIF, JPEG, PNG or WebP image.
	DataImageType = "type"
	// DataImageEncoding: the image is not marked as base64 right after its type.
	DataImageEncoding = "encoding"
	// DataImageBase64: the base64 data cannot be decoded.
	DataImageBase64 = "base64"
)

// newUGCPolicy mirrors bluemonday.UGCPolicy (v1.0.27) with these deliberate differences:
//   - the global "id" attribute is restricted to the user-content- namespace instead of being
//     accepted with an unanchored pattern;
//   - an image can be a base64 data: URI of a raster type (allowRasterDataURI), so a note keeps
//     the images that its Markdown embeds; links and quotation sources cannot use data: URIs.
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

	policy.AllowAttrs("cite").Matching(reLinkURL).OnElements("blockquote")
	policy.AllowElements("br", "div", "hr", "p", "span", "wbr")

	policy.AllowAttrs("href").Matching(reLinkURL).OnElements("a")
	policy.AllowAttrs("name").Matching(regexp.MustCompile(`^([\p{L}\p{N}_-]+)$`)).OnElements("map")
	policy.AllowAttrs("alt").Matching(bluemonday.Paragraph).OnElements("area")
	policy.AllowAttrs("coords").Matching(regexp.MustCompile(`^(\d+,)+\d+$`)).OnElements("area")
	policy.AllowAttrs("href").Matching(reLinkURL).OnElements("area")
	policy.AllowAttrs("rel").Matching(bluemonday.SpaceSeparatedTokens).OnElements("area")
	policy.AllowAttrs("shape").Matching(regexp.MustCompile(`(?i)^(default|circle|rect|poly)$`)).OnElements("area")
	policy.AllowAttrs("usemap").Matching(regexp.MustCompile(`(?i)^#[\p{L}\p{N}_-]+$`)).OnElements("img")

	policy.AllowElements("abbr", "acronym", "cite", "code", "dfn", "em",
		"figcaption", "mark", "s", "samp", "strong", "sub", "sup", "var")
	policy.AllowAttrs("cite").Matching(reLinkURL).OnElements("q")
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
	policy.AllowURLSchemeWithCustomPolicy("data", allowRasterDataURI)

	return policy
}

// allowRasterDataURI accepts a data: URI that holds a base64 GIF, JPEG, PNG or WebP image.
func allowRasterDataURI(uri *url.URL) bool {
	return dataImageProblem(uri) == ""
}

// dataImageProblem returns the reason why a data: URI is not an image that a note page shows,
// or "" for a base64 GIF, JPEG, PNG or WebP image.
func dataImageProblem(uri *url.URL) string {
	switch {
	case uri.RawQuery != "" || uri.ForceQuery || uri.Fragment != "":
		return DataImageAddress
	case reSVGType.MatchString(uri.Opaque):
		return DataImageSVG
	case !reRasterType.MatchString(uri.Opaque):
		return DataImageType
	}

	prefix := reRasterDataURI.FindString(uri.Opaque)
	if prefix == "" {
		return DataImageEncoding
	}

	_, err := base64.StdEncoding.DecodeString(uri.Opaque[len(prefix):])
	if err != nil {
		return DataImageBase64
	}

	return ""
}
