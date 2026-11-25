package render_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/internal/infra/render"
)

type MathSuite struct {
	suite.Suite

	r *render.Renderer
}

func (s *MathSuite) SetupTest() {
	s.r = render.NewRenderer()
}

func TestMath(t *testing.T) {
	suite.Run(t, new(MathSuite))
}

func (s *MathSuite) render(markdown string) string {
	s.T().Helper()

	out, _, err := s.r.Render(markdown)
	s.Require().NoError(err)

	return out
}

func (s *MathSuite) TestDisplayBlock_KeepsTeXSource() {
	out := s.render("$$\n\\bar x=\\frac{1}{n}\\sum_{i=1}^{n}x_i\n$$")
	s.Equal("<div class=\"math-display\">\\bar x=\\frac{1}{n}\\sum_{i=1}^{n}x_i\n</div>\n", out)
}

func (s *MathSuite) TestDisplayBlock_OneLine() {
	out := s.render("**MSE:**\n\n$$\\mathrm{MSE}=\\frac{1}{n}\\sum_{i=1}^{n}(y_i-\\hat{y}_i)^2$$\n\nnext")
	s.Contains(out, "<div class=\"math-display\">\\mathrm{MSE}=\\frac{1}{n}\\sum_{i=1}^{n}(y_i-\\hat{y}_i)^2</div>")
	s.Contains(out, "<p>next</p>")
}

func (s *MathSuite) TestDisplayBlock_Brackets() {
	out := s.render("Text:\n\\[\nx \\\\ y\n\\]\nafter")
	s.Equal("<p>Text:</p>\n<div class=\"math-display\">x \\\\ y\n</div>\n<p>after</p>\n", out)
}

func (s *MathSuite) TestDisplayBlock_ClosingLineHoldsSource() {
	out := s.render("$$\na = b\n+ c$$")
	s.Equal("<div class=\"math-display\">a = b\n+ c</div>\n", out)
}

func (s *MathSuite) TestDisplayBlock_InListItem() {
	out := s.render("- item\n\n  $$\n  a_1\n  $$\n")
	s.Contains(out, "<li>\n<p>item</p>\n<div class=\"math-display\">a_1\n</div>\n</li>")
}

// TestDisplayBlock_UnclosedRunsToEnd pins the fenced-code behavior: a formula without the
// closing delimiter takes the rest of its container.
func (s *MathSuite) TestDisplayBlock_UnclosedRunsToEnd() {
	out := s.render("$$\nunclosed _a_\n\nstill")
	s.Equal("<div class=\"math-display\">unclosed _a_\n\nstill\n</div>\n", out)
}

func (s *MathSuite) TestDisplayBlock_TextAfterClosingIsParagraph() {
	out := s.render("$$x$$ and text")
	s.Equal("<p><span class=\"math-display\">x</span> and text</p>\n", out)
}

func (s *MathSuite) TestInline_KeepsUnderscoresAndStars() {
	out := s.render("Inline $e_i=y_i-\\hat{y}_i$ and \\(a_1 * b_2 * c\\) end.")
	s.Equal("<p>Inline <span class=\"math-inline\">e_i=y_i-\\hat{y}_i</span> and "+
		"<span class=\"math-inline\">a_1 * b_2 * c</span> end.</p>\n", out)
}

func (s *MathSuite) TestInline_DisplayDollars() {
	out := s.render("Inline $$x^2$$ display.")
	s.Equal("<p>Inline <span class=\"math-display\">x^2</span> display.</p>\n", out)
}

func (s *MathSuite) TestInline_EscapesHTML() {
	out := s.render("$a<b & c>d$")
	s.Equal("<p><span class=\"math-inline\">a&lt;b &amp; c&gt;d</span></p>\n", out)
}

func (s *MathSuite) TestInline_EscapedDollarInsideFormula() {
	out := s.render("$\\$5 + x$")
	s.Equal("<p><span class=\"math-inline\">\\$5 + x</span></p>\n", out)
}

func (s *MathSuite) TestNotMath() {
	cases := map[string]string{
		"prices":          "Prices $5 and $10.",
		"space after":     "a $ b$ c",
		"space before":    "a $b $ c",
		"digit after":     "$x$5",
		"escaped dollars": "\\$x\\$",
		"escaped paren":   "\\\\(x\\\\)",
		"empty":           "$$ $$ and \\( \\)",
		"crosses code":    "$10 and `$x$`",
	}

	for name, markdown := range cases {
		s.Run(name, func() {
			s.NotContains(s.render(markdown), "math-")
		})
	}
}

func (s *MathSuite) TestCodeKeepsDollars() {
	out := s.render("`$x$`\n\n```\n$$\ny\n$$\n```")
	s.Contains(out, "<code>$x$</code>")
	s.Contains(out, "$$\ny\n$$")
	s.NotContains(out, "math-")
}

func (s *MathSuite) TestIndentedDollarsAreCode() {
	out := s.render("    $$\n    x\n    $$")
	s.Contains(out, "<pre><code>$$\nx\n$$\n</code></pre>")
}

// TestSanitizerAllowsOnlyFormulaClasses pins the class allow-list: raw HTML can use a formula
// class, but no other class next to it.
func (s *MathSuite) TestSanitizerAllowsOnlyFormulaClasses() {
	out := s.render(`<span class="math-inline evil">x</span><span class="math-display">y</span>`)
	s.Equal("<p><span>x</span><span class=\"math-display\">y</span></p>\n", out)
}

func (s *MathSuite) TestDisplayBlock_Forms() {
	cases := map[string]struct {
		markdown string
		want     string
	}{
		// The lines keep their indent; TeX ignores spaces at the start of a line.
		"indented opener": {
			markdown: "   $$\n   x\n   $$",
			want:     "<div class=\"math-display\">   x\n</div>\n",
		},
		"empty formula": {
			markdown: "$$\n$$",
			want:     "<div class=\"math-display\"></div>\n",
		},
		"spaces after delimiters": {
			markdown: "$$   \nx\n$$   \nafter",
			want:     "<div class=\"math-display\">x\n</div>\n<p>after</p>\n",
		},
		"one-line brackets": {
			markdown: "\\[ \\frac{a}{b} \\]",
			want:     "<div class=\"math-display\"> \\frac{a}{b} </div>\n",
		},
		"unclosed brackets": {
			markdown: "\\[\nx",
			want:     "<div class=\"math-display\">x\n</div>\n",
		},
		"brackets do not close dollars": {
			markdown: "$$\nx\n\\]\n$$",
			want:     "<div class=\"math-display\">x\n\\]\n</div>\n",
		},
		"dollars do not close brackets": {
			markdown: "\\[\nx\n$$\n\\]",
			want:     "<div class=\"math-display\">x\n$$\n</div>\n",
		},
		"crlf lines": {
			markdown: "$$\r\nx\r\n$$\r\n",
			want:     "<div class=\"math-display\">x\n</div>\n",
		},
		"in blockquote": {
			markdown: "> $$\n> q_1\n> $$",
			want:     "<blockquote>\n<div class=\"math-display\">q_1\n</div>\n</blockquote>\n",
		},
		"two blocks": {
			markdown: "$$a$$\n$$b$$",
			want:     "<div class=\"math-display\">a</div>\n<div class=\"math-display\">b</div>\n",
		},
		"escapes html": {
			markdown: "$$\n</div><script>alert(1)</script>\n$$",
			want:     "<div class=\"math-display\">&lt;/div&gt;&lt;script&gt;alert(1)&lt;/script&gt;\n</div>\n",
		},
	}

	for name, tc := range cases {
		s.Run(name, func() {
			s.Equal(tc.want, s.render(tc.markdown))
		})
	}
}

// TestDisplayBlock_NotBlock covers opening lines that are text: the paragraph keeps them.
func (s *MathSuite) TestDisplayBlock_NotBlock() {
	cases := map[string]struct {
		markdown string
		want     string
	}{
		"three dollars":       {markdown: "$$$", want: "<p>$$$</p>\n"},
		"blank one-line":      {markdown: "$$ $$", want: "<p>$$ $$</p>\n"},
		"blank brackets":      {markdown: `\[ \]`, want: "<p>[ ]</p>\n"},
		"unclosed one-line":   {markdown: "$$x", want: "<p>$$x</p>\n"},
		"text after brackets": {markdown: `\[x\] and text`, want: "<p>[x] and text</p>\n"},
		"wrong closer":        {markdown: `\[x$$`, want: "<p>[x$$</p>\n"},
		"escaped dollars":     {markdown: "\\$$x$$", want: "<p>$<span class=\"math-inline\">x</span>$</p>\n"},
		"in fenced code":      {markdown: "```\n\\[\nx\n\\]\n```", want: "<pre><code>\\[\nx\n\\]\n</code></pre>\n"},
	}

	for name, tc := range cases {
		s.Run(name, func() {
			s.Equal(tc.want, s.render(tc.markdown))
		})
	}
}

func (s *MathSuite) TestInline_Contexts() {
	cases := map[string]struct {
		markdown string
		want     string
	}{
		"heading": {
			markdown: "## Mean $\\bar x$",
			want:     "<span class=\"math-inline\">\\bar x</span></h2>",
		},
		"emphasis": {
			markdown: "*see $x_i$ here*",
			want:     "<em>see <span class=\"math-inline\">x_i</span> here</em>",
		},
		"link text": {
			markdown: "[$x$](https://example.com)",
			want:     "<span class=\"math-inline\">x</span></a>",
		},
		"table cell": {
			markdown: "| a |\n| - |\n| $c_1$ |",
			want:     "<td><span class=\"math-inline\">c_1</span></td>",
		},
		"list item": {
			markdown: "- $a$ and \\(b\\)",
			want: "<li><span class=\"math-inline\">a</span> and " +
				"<span class=\"math-inline\">b</span></li>",
		},
		"two in a row": {
			markdown: "$x$$y$",
			want:     "<span class=\"math-inline\">x</span><span class=\"math-inline\">y</span>",
		},
		"display then dollar": {
			markdown: "a $$x$$$ b",
			want:     "a <span class=\"math-display\">x</span>$ b",
		},
		"dollar inside parens": {
			markdown: "\\(p \\$ q\\)",
			want:     "<span class=\"math-inline\">p \\$ q</span>",
		},
		"parens inside dollars": {
			markdown: "$a \\(b\\) c$",
			want:     "<span class=\"math-inline\">a \\(b\\) c</span>",
		},
		"script stays text": {
			markdown: "$<script>alert(1)</script>$",
			want:     "<span class=\"math-inline\">&lt;script&gt;alert(1)&lt;/script&gt;</span>",
		},
		"quote stays text": {
			markdown: `$a" onmouseover="x$`,
			want:     `<span class="math-inline">a&#34; onmouseover=&#34;x</span>`,
		},
	}

	for name, tc := range cases {
		s.Run(name, func() {
			s.Contains(s.render(tc.markdown), tc.want)
		})
	}
}

func (s *MathSuite) TestInline_NotMathMore() {
	cases := map[string]string{
		"lone dollar":             "cost $",
		"lone parens opener":      "open \\( only",
		"lone parens closer":      "close \\) only",
		"over two lines":          "$a\nb$",
		"parens over two lines":   "\\(a\nb\\)",
		"escaped backslash paren": "\\\\(x\\\\)",
		"escaped dollar pair":     "\\$a\\$",
		"backtick inside":         "$a`b$",
		"space inside closer":     "$x $ and $ y$",
		"currency list":           "$1, $2, $3",
		"inline code":             "`\\(x\\)` and `$$y$$`",
		"autolink":                "<https://example.com/$a$>",
		"html attribute":          `<a href="/$a$">x</a>`,
	}

	for name, markdown := range cases {
		s.Run(name, func() {
			s.NotContains(s.render(markdown), "math-", markdown)
		})
	}
}
