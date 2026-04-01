package render

import (
	"bytes"
	"fmt"

	highlighting "github.com/yuin/goldmark-highlighting/v2"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

// Renderer converts Markdown to sanitized HTML.
type Renderer struct {
	md     goldmark.Markdown
	policy *bluemonday.Policy
}

// NewRenderer returns a Renderer with tables, strikethrough, autolinks and syntax highlighting enabled.
func NewRenderer() *Renderer {
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.Table,
			extension.Strikethrough,
			extension.Linkify,
			highlighting.NewHighlighting(
				highlighting.WithStyle("github-dark"),
				highlighting.WithFormatOptions(
					chromahtml.WithClasses(true),
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
		),
	)

	policy := bluemonday.UGCPolicy()
	policy.RequireNoFollowOnLinks(true)
	policy.AddTargetBlankToFullyQualifiedLinks(true)
	policy.AllowAttrs("class").OnElements("span", "code", "pre", "div")

	return &Renderer{
		md:     md,
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
