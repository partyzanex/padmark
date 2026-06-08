package render_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/internal/infra/render"
)

type RendererSuite struct {
	suite.Suite

	r *render.Renderer
}

func (s *RendererSuite) SetupTest() {
	s.r = render.NewRenderer()
}

func TestRenderer(t *testing.T) {
	suite.Run(t, new(RendererSuite))
}

func (s *RendererSuite) TestParagraph() {
	out, err := s.r.Render("hello world")
	s.Require().NoError(err)
	s.Contains(out, "hello world")
}

func (s *RendererSuite) TestHeadings() {
	out, err := s.r.Render("# Title\n## Sub")
	s.Require().NoError(err)
	s.Contains(out, "<h1")
	s.Contains(out, "<h2")
}

func (s *RendererSuite) TestTable() {
	md := "| A | B |\n|---|---|\n| 1 | 2 |"
	out, err := s.r.Render(md)
	s.Require().NoError(err)
	s.Contains(out, "<table>")
	s.Contains(out, "<td>1</td>")
}

func (s *RendererSuite) TestStrikethrough() {
	out, err := s.r.Render("~~gone~~")
	s.Require().NoError(err)
	s.Contains(out, "<del>gone</del>")
}

func (s *RendererSuite) TestAutolink() {
	out, err := s.r.Render("visit https://example.com today")
	s.Require().NoError(err)
	s.Contains(out, `href="https://example.com"`)
}

func (s *RendererSuite) TestSanitizesScriptTags() {
	out, err := s.r.Render("<script>alert('xss')</script>")
	s.Require().NoError(err)
	s.NotContains(out, "<script>", "script tag must be stripped")
}

func (s *RendererSuite) TestSanitizesOnclickAttribute() {
	out, err := s.r.Render(`<a href="/" onclick="evil()">click</a>`)
	s.Require().NoError(err)
	s.NotContains(out, "onclick", "onclick must be stripped")
}

func (s *RendererSuite) TestEmptyInput() {
	out, err := s.r.Render("")
	s.Require().NoError(err)
	s.Empty(out)
}

// TestSyntaxHighlightingUsesPrefixedClasses verifies fenced code blocks are highlighted with
// chroma-* prefixed classes, which the sanitizer allowlist and style.css both depend on.
func (s *RendererSuite) TestSyntaxHighlightingUsesPrefixedClasses() {
	out, err := s.r.Render("```go\nfunc main() {}\n```\n")
	s.Require().NoError(err)
	s.Contains(out, `class="chroma-chroma chroma-dark"`, "wrapper must carry prefixed classes")
	s.Contains(out, "chroma-kd", "keyword token must be highlighted with a prefixed class")
	s.NotContains(out, `class="chroma"`, "unprefixed chroma class must not appear")
}

// TestStripsArbitraryClassAttribute verifies that a user-supplied class outside the chroma-*
// vocabulary is removed (cosmetic UI-spoofing protection), since the sanitizer only allows
// class values matching the chroma allowlist.
func (s *RendererSuite) TestStripsArbitraryClassAttribute() {
	out, err := s.r.Render(`<span class="admin-banner">x</span>`)
	s.Require().NoError(err)
	s.NotContains(out, "admin-banner", "arbitrary class must be stripped")
}

// TestStripsChromaLookalikeClass verifies that a class merely starting like chroma but mixed
// with a forbidden token is rejected as a whole (the regex anchors the full attribute value).
func (s *RendererSuite) TestStripsChromaLookalikeClass() {
	out, err := s.r.Render(`<span class="chroma-k evil-class">x</span>`)
	s.Require().NoError(err)
	s.NotContains(out, "evil-class", "mixed class value must be rejected as a whole")
	s.NotContains(out, "chroma-k", "the whole attribute is dropped when any token is invalid")
}
