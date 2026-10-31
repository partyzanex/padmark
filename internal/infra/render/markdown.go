package render

import (
	"bytes"
	"fmt"
	"regexp"

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

// reChromaClass matches a class attribute consisting solely of space-separated chroma-* tokens
// (e.g. "chroma-chroma chroma-dark", "chroma-kd"). This blocks user Markdown from injecting
// arbitrary application classes (cosmetic UI spoofing) while preserving syntax highlighting.
var reChromaClass = regexp.MustCompile(`^chroma-[a-z0-9]+( chroma-[a-z0-9]+)*$`)

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

// Renderer converts Markdown to sanitized HTML.
type Renderer struct {
	md     goldmark.Markdown
	policy *bluemonday.Policy
}

// NewRenderer returns a Renderer with tables, strikethrough, autolinks and syntax highlighting enabled.
func NewRenderer() *Renderer {
	gmd := goldmark.New(
		// Guarded default parsers: render time stays linear in note size (see parse_guards.go).
		goldmark.WithParser(parser.NewParser(guardedParserOptions()...)),
		goldmark.WithExtensions(
			extension.Table,
			strikethroughExtension{},
			linkifyExtension{},
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
	policy.AllowAttrs("class").Matching(reChromaClass).OnElements("span", "code", "pre", "div")

	return &Renderer{
		md:     gmd,
		policy: policy,
	}
}

// Render converts markdown content to sanitized HTML.
func (r *Renderer) Render(content string) (string, error) {
	var buf bytes.Buffer

	// A fresh context per call: heading IDs are unique per document, generated in linear time and
	// namespaced with the user-content- prefix against DOM clobbering.
	pctx := parser.NewContext(parser.WithIDs(prefixedIDs{inner: newHeadingIDs()}))

	err := r.md.Convert([]byte(content), &buf, parser.WithContext(pctx))
	if err != nil {
		return "", fmt.Errorf("render markdown: %w", err)
	}

	return r.policy.Sanitize(buf.String()), nil
}
