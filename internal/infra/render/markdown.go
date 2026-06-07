package render

import (
	"bytes"
	"fmt"
	"regexp"

	highlighting "github.com/yuin/goldmark-highlighting/v2"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
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

// Renderer converts Markdown to sanitized HTML.
type Renderer struct {
	md     goldmark.Markdown
	policy *bluemonday.Policy
}

// NewRenderer returns a Renderer with tables, strikethrough, autolinks and syntax highlighting enabled.
func NewRenderer() *Renderer {
	gmd := goldmark.New(
		goldmark.WithExtensions(
			extension.Table,
			extension.Strikethrough,
			extension.Linkify,
			highlighting.NewHighlighting(
				highlighting.WithStyle("github-dark"),
				highlighting.WithFormatOptions(
					chromahtml.WithClasses(true),
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

	policy := bluemonday.UGCPolicy()
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

	err := r.md.Convert([]byte(content), &buf)
	if err != nil {
		return "", fmt.Errorf("render markdown: %w", err)
	}

	return r.policy.Sanitize(buf.String()), nil
}
