package render_test

import (
	"strings"
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
	s.False(strings.Contains(out, "<script>"), "script tag must be stripped")
}

func (s *RendererSuite) TestSanitizesOnclickAttribute() {
	out, err := s.r.Render(`<a href="/" onclick="evil()">click</a>`)
	s.Require().NoError(err)
	s.False(strings.Contains(out, "onclick"), "onclick must be stripped")
}

func (s *RendererSuite) TestEmptyInput() {
	out, err := s.r.Render("")
	s.Require().NoError(err)
	s.Equal("", out)
}
