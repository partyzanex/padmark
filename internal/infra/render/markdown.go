package render

import (
	"bytes"
	"fmt"
	stdhtml "html"
	"net/url"
	"regexp"
	"strings"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

// chromaClassPrefix namespaces every chroma syntax-highlighting class so the sanitizer can
// allow exactly those classes and nothing else. The generated highlight CSS in style.css is
// produced with the same prefix and must stay in sync (regenerate via chromahtml.WriteCSS).
const chromaClassPrefix = "chroma-"

// reContentClass matches a class attribute consisting solely of space-separated chroma-* tokens
// (e.g. "chroma-chroma chroma-dark", "chroma-kd"), or one formula class (math.go). This blocks
// user Markdown from injecting arbitrary application classes (cosmetic UI spoofing) while
// preserving syntax highlighting and formulas.
var reContentClass = regexp.MustCompile(`^(chroma-[a-z0-9]+( chroma-[a-z0-9]+)*|` +
	mathInlineClass + `|` + mathDisplayClass + `)$`)

// userContentIDPrefix is the namespace for every element ID that originates from user content
// (auto-generated heading IDs and raw-HTML id attributes). Application elements never use it, so
// user Markdown cannot shadow them via DOM clobbering (<h1 id="raw-content"> would otherwise win
// document.getElementById over, or replace, an element the page scripts rely on).
const userContentIDPrefix = "user-content-"

// reUserContentID admits only IDs inside the user-content- namespace.
var reUserContentID = regexp.MustCompile(`^` + userContentIDPrefix + `[a-zA-Z0-9_-]+$`)

// prefixedIDs wraps goldmark's default heading-ID generator so every generated ID lands in the
// user-content- namespace. De-duplication stays in the wrapped generator, which sees unprefixed
// values, so repeated headings still get unique suffixes.
type prefixedIDs struct {
	inner parser.IDs
}

// Generate implements parser.IDs.
func (p prefixedIDs) Generate(value []byte, kind ast.NodeKind) []byte {
	return append([]byte(userContentIDPrefix), p.inner.Generate(value, kind)...)
}

// Put implements parser.IDs.
func (p prefixedIDs) Put(value []byte) {
	p.inner.Put(value)
}

// reDataScheme finds a data: URI anywhere in the converted HTML, so a note without one skips the
// image inspection.
var reDataScheme = regexp.MustCompile(`(?i)data:`)

// reDataImageSource takes the source of each image that the inspection policy keeps.
var reDataImageSource = regexp.MustCompile(`<img src="([^"]*)"`)

// Renderer converts Markdown to sanitized HTML.
type Renderer struct {
	md      goldmark.Markdown
	policy  *bluemonday.Policy
	inspect *bluemonday.Policy
}

// NewRenderer returns a Renderer with tables, strikethrough, autolinks, TeX formulas and syntax
// highlighting enabled.
func NewRenderer() *Renderer {
	gmd := goldmark.New(
		// Guarded default parsers: render time stays linear in note size (see parse_guards.go).
		goldmark.WithParser(parser.NewParser(guardedParserOptions()...)),
		goldmark.WithExtensions(
			extension.Table,
			strikethroughExtension{},
			linkifyExtension{},
			newMathExtension(),
			highlighting.NewHighlighting(
				highlighting.WithStyle("github-dark"),
				highlighting.WithFormatOptions(
					chromahtml.WithClasses(true),
					chromahtml.WithModeClasses(true),
					chromahtml.ClassPrefix(chromaClassPrefix),
					chromahtml.WithLineNumbers(false),
				),
			),
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			html.WithHardWraps(),
			html.WithXHTML(),
			html.WithUnsafe(),
		),
	)

	policy := newUGCPolicy()
	policy.RequireNoFollowOnLinks(true)
	policy.AddTargetBlankToFullyQualifiedLinks(true)
	policy.AllowAttrs("class").Matching(reContentClass).OnElements("span", "code", "pre", "div")

	// The inspection policy keeps only images with a data: source and does not check URLs, so
	// Render can tell why the sanitizing policy removed a source.
	inspect := bluemonday.NewPolicy()
	inspect.RequireParseableURLs(false)
	inspect.AllowAttrs("src").Matching(regexp.MustCompile(`(?i)^\s*data:`)).OnElements("img")

	return &Renderer{
		md:      gmd,
		policy:  policy,
		inspect: inspect,
	}
}

// Render converts markdown content to sanitized HTML. droppedImages has one reason (DataImage*)
// for each image whose data: source the page does not show; the sanitized HTML keeps such an
// image without its source.
func (r *Renderer) Render(content string) (rendered string, droppedImages []string, err error) {
	var buf bytes.Buffer

	// A fresh context per call: heading IDs are unique per document, generated in linear time and
	// namespaced with the user-content- prefix against DOM clobbering.
	pctx := parser.NewContext(parser.WithIDs(prefixedIDs{inner: newHeadingIDs()}))

	err = r.md.Convert([]byte(content), &buf, parser.WithContext(pctx))
	if err != nil {
		return "", nil, fmt.Errorf("render markdown: %w", err)
	}

	converted := buf.String()

	// The scroll boxes come after the sanitizer, so user HTML cannot make one (table_scroll.go).
	return wrapTables(r.policy.Sanitize(converted)), r.droppedDataImages(converted), nil
}

// droppedDataImages returns the reasons why the page does not show the data: images of the
// converted HTML. It reads each source as the sanitizing policy does: spaces around it and line
// breaks in it do not count.
func (r *Renderer) droppedDataImages(converted string) []string {
	if !reDataScheme.MatchString(converted) {
		return nil
	}

	var reasons []string

	for _, match := range reDataImageSource.FindAllStringSubmatch(r.inspect.Sanitize(converted), -1) {
		source := strings.TrimSpace(stdhtml.UnescapeString(match[1]))
		source = strings.NewReplacer("\r", "", "\n", "").Replace(source)

		uri, err := url.Parse(source)
		if err != nil {
			reasons = append(reasons, DataImageInvalid)

			continue
		}

		reason := dataImageProblem(uri)
		if reason != "" {
			reasons = append(reasons, reason)
		}
	}

	return reasons
}
