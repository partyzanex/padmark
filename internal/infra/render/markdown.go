package render

import (
	"bytes"
	"fmt"

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

// NewRenderer returns a Renderer with tables, strikethrough, and autolinks enabled.
func NewRenderer() *Renderer {
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.Table,
			extension.Strikethrough,
			extension.Linkify,
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			html.WithHardWraps(),
			html.WithXHTML(),
		),
	)

	return &Renderer{
		md:     md,
		policy: bluemonday.UGCPolicy(),
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
