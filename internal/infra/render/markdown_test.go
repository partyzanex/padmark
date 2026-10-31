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

func (s *RendererSuite) TestRenderPlain_EscapesAndWraps() {
	out, err := s.r.RenderPlain("<script>alert(1)</script>")
	s.Require().NoError(err)
	s.Equal("<pre>&lt;script&gt;alert(1)&lt;/script&gt;</pre>", out)
}

func (s *RendererSuite) TestRenderPlain_DoesNotParseMarkdown() {
	out, err := s.r.RenderPlain("# not a heading\n**not bold**")
	s.Require().NoError(err)
	s.Equal("<pre># not a heading\n**not bold**</pre>", out)
}

func (s *RendererSuite) TestHeadings() {
	out, err := s.r.Render("# Title\n## Sub")
	s.Require().NoError(err)
	s.Contains(out, "<h1")
	s.Contains(out, "<h2")
}

// TestHeadingIDsArePrefixed pins DOM-clobbering protection: auto heading IDs live in the
// user-content- namespace, so a heading can never shadow an application element ID.
func (s *RendererSuite) TestHeadingIDsArePrefixed() {
	out, err := s.r.Render("# raw-content\n\n## createdTag\n\n## createdTag")
	s.Require().NoError(err)
	s.Contains(out, `id="user-content-raw-content"`)
	s.Contains(out, `id="user-content-createdtag"`)
	s.Contains(out, `id="user-content-createdtag-1"`, "duplicate headings still get unique IDs")
	s.NotContains(out, `id="raw-content"`)
	s.NotContains(out, `id="createdtag`)
}

// TestRawHTMLIDsAreRestricted verifies a user-supplied id outside the user-content- namespace is
// dropped, while one inside it survives, and that name attributes cannot clobber either.
func (s *RendererSuite) TestRawHTMLIDsAreRestricted() {
	out, err := s.r.Render(`<div id="raw-content">a</div><p id="user-content-ok">b</p>` +
		`<a name="copyRawBtn" href="/x">c</a><img name="createdTag" src="/i.png">`)
	s.Require().NoError(err)
	s.NotContains(out, `id="raw-content"`, "application element IDs must not be user-controllable")
	s.Contains(out, `id="user-content-ok"`)
	s.NotContains(out, `name="`, "name attributes can clobber document properties")
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

func (s *RendererSuite) TestRejectsExecutableURLs() {
	for _, input := range []string{
		`[click](javascript:alert%281%29)`,
		`[click](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)`,
		`<a href="javascript:alert(1)">click</a>`,
		`<a href="JaVaScRiPt:alert(1)">click</a>`,
		`<a href="jav&#x61;script:alert(1)">click</a>`,
		`<a href="java&#x09;script:alert(1)">click</a>`,
		`<a href="data:text/html,<script>alert(1)</script>">click</a>`,
	} {
		out, err := s.r.Render(input)
		s.Require().NoError(err)
		s.NotContains(out, "href=", input)
	}

	out, err := s.r.Render(`[safe](https://example.com) [relative](/guide) [email](mailto:help@example.com)`)
	s.Require().NoError(err)
	s.Contains(out, `href="https://example.com"`)
	s.Contains(out, `href="/guide"`)
	s.Contains(out, `href="mailto:help@example.com"`)
}

// TestRejectsExecutableImageURLs is the <img src> counterpart of TestRejectsExecutableURLs:
// javascript: and data: sources — from markdown image syntax or raw HTML — must not survive
// sanitization, while ordinary http(s) and relative images still render.
func (s *RendererSuite) TestRejectsExecutableImageURLs() {
	for _, input := range []string{
		`![x](javascript:alert%281%29)`,
		`![x](JaVaScRiPt:alert%281%29)`,
		`![x](data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9YWxlcnQoMSk+)`,
		`![x](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)`,
		`<img src="javascript:alert(1)">`,
		`<img src="jav&#x61;script:alert(1)">`,
		`<img src="java&#x09;script:alert(1)">`,
		`<img src="data:image/svg+xml,<svg onload=alert(1)>">`,
		`<img src="data:text/html,<script>alert(1)</script>">`,
	} {
		out, err := s.r.Render(input)
		s.Require().NoError(err)
		s.NotContains(out, "src=", input)
		s.NotContains(strings.ToLower(out), "javascript:", input)
		s.NotContains(out, "data:", input)
	}

	out, err := s.r.Render(`![safe](https://example.com/a.png) ![relative](/static/favicon.svg)`)
	s.Require().NoError(err)
	s.Contains(out, `src="https://example.com/a.png"`)
	s.Contains(out, `src="/static/favicon.svg"`)
}
