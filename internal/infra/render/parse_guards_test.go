package render

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	highlighting "github.com/yuin/goldmark-highlighting/v2"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/stretchr/testify/suite"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

type ParseGuardsSuite struct {
	suite.Suite

	guarded *Renderer
	stock   goldmark.Markdown
}

func TestParseGuards(t *testing.T) {
	suite.Run(t, new(ParseGuardsSuite))
}

func (s *ParseGuardsSuite) SetupTest() {
	s.guarded = NewRenderer()
	// The configuration NewRenderer had before the guards: stock parsers and extensions.
	s.stock = goldmark.New(
		goldmark.WithExtensions(
			extension.Table,
			extension.Strikethrough,
			extension.Linkify,
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
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(html.WithHardWraps(), html.WithXHTML(), html.WithUnsafe()),
	)
}

// guardedHTML is the unsanitized HTML of the guarded parser, comparable with stockHTML.
func (s *ParseGuardsSuite) guardedHTML(src string) string {
	var buf bytes.Buffer

	pctx := parser.NewContext(parser.WithIDs(newHeadingIDs()))
	s.Require().NoError(s.guarded.md.Convert([]byte(src), &buf, parser.WithContext(pctx)))

	return buf.String()
}

func (s *ParseGuardsSuite) stockHTML(src string) string {
	var buf bytes.Buffer

	s.Require().NoError(s.stock.Convert([]byte(src), &buf))

	return buf.String()
}

// TestOrdinaryMarkdownRendersLikeStockGoldmark pins that the guards are invisible to real notes.
func (s *ParseGuardsSuite) TestOrdinaryMarkdownRendersLikeStockGoldmark() {
	docs := map[string]string{
		"duplicate_headings": "# A\n# A\n# A-1\n# A\n## a 1\n#\n#\n# Привет\n# Привет\n# x_y z\n",
		"many_duplicates":    strings.Repeat("## Step\n\ntext\n\n", 300) + "# Step-7\n# Step\n",
		"links": "[inline](https://e.com/a_(b)) [title](/p \"t\") [paren title](/p (t)) [pointy](</a b>)\n" +
			"[full][ref] [collapsed][] [ref] ![img](/i.png) [broken](/x y z) [[nested]](/n)\n" +
			"[unclosed]( and [a](b (c and [x](<y\n\n[ref]: /target \"T\"\n",
		"emphasis": "*a* _b_ **c** __d__ ***e*** *f _g_ h* ~~strike~~ ~one~ * not * a*b*c snake_case_name\n" +
			"**unclosed _mixed *runs ~~here\n",
		"linkify": "see https://e.com/x?y=1, www.example.org. mail user.name+tag@example.co.uk or " +
			"(http://e.com/p) *www.e.com* _a@b.co_ ~ftp://f.example.com~ not@email x@y\n",
		"nesting": "> q\n> > qq\n> > > qqq\n\n- a\n  - b\n    - c\n      1. d\n         > e\n",
		"table":   "| a | *b* |\n|---|:---:|\n| [l](/x) | `c` |\n| ~~s~~ | x@y.io |\n",
		"code":    "```go\nfunc f() {}\n```\n\n    indented\n\n`span` ``a`b``\n",
		"html":    "<div>\n*raw*\n</div>\n\n<span>inline</span> &amp; &copy;\n",
	}

	for name, src := range docs {
		s.Equal(s.stockHTML(src), s.guardedHTML(src), name)
	}
}

// refDefParagraph is one paragraph of count link reference definitions in single- and multi-line
// forms (destination or title on the next line, a title spanning lines, a destination that opens
// with '['), then text that uses them. Varying count moves every form across a window boundary.
func refDefParagraph(count int) string {
	forms := []string{
		"[l%[1]d]: /u%[1]d",
		"[l%[1]d]:\n/u%[1]d",
		"[l%[1]d]: /u%[1]d\n\"t%[1]d\"",
		"[l%[1]d]: /u%[1]d 'title %[1]d\nspans lines'",
		"[l%[1]d]:\n[x%[1]d]",
		"  [l%[1]d]: </u %[1]d> (t)",
	}

	var builder strings.Builder

	for idx := range count {
		fmt.Fprintf(&builder, forms[idx%len(forms)]+"\n", idx)
	}

	for idx := range count {
		fmt.Fprintf(&builder, "[l%d] ", idx)
	}

	return builder.String()
}

func (s *ParseGuardsSuite) TestLongLinkReferenceParagraphsRenderLikeStock() {
	for count := refDefWindowLines - 8; count < 2*refDefWindowLines+8; count++ {
		src := refDefParagraph(count)
		s.Equal(s.stockHTML(src), s.guardedHTML(src), "count %d", count)
	}

	text := strings.Repeat("plain line\n", 3*refDefWindowLines)
	broken := "[ok]: /ok\n[broken]: \n" + strings.Repeat("[x]: /x\n", 2*refDefWindowLines) + "[ok]"

	for name, src := range map[string]string{
		"no_definitions":           text,
		"definitions_then_text":    refDefParagraph(refDefWindowLines) + "\n" + text,
		"text_then_bracket_lines":  text + strings.Repeat("[x]: /x\n", refDefWindowLines),
		"failed_definition_midway": broken,
		"in_blockquote":            "> " + strings.ReplaceAll(refDefParagraph(2*refDefWindowLines), "\n", "\n> "),
		"in_list_item":             "- " + strings.ReplaceAll(refDefParagraph(2*refDefWindowLines), "\n", "\n  "),
	} {
		s.Equal(s.stockHTML(src), s.guardedHTML(src), name)
	}
}

func (s *ParseGuardsSuite) TestContainersDeeperThanCapRenderAsText() {
	src := strings.Repeat(">", maxContainerDepth+8) + " deep"

	out := s.guardedHTML(src)

	s.Equal(maxContainerDepth, strings.Count(out, "<blockquote>"))
	s.Contains(out, strings.Repeat("&gt;", 8)+" deep")
}

func (s *ParseGuardsSuite) TestNestingUpToCapMatchesStock() {
	src := strings.Repeat("> ", maxContainerDepth) + "deep\n\n" + strings.Repeat("- ", maxContainerDepth/2) + "item\n"

	s.Equal(s.stockHTML(src), s.guardedHTML(src))
}

func (s *ParseGuardsSuite) TestEmailLocalPartLongerThanRFCLimitIsNotLinked() {
	fits := strings.Repeat("a", maxEmailLocalPart) + "@example.com"
	tooLong := strings.Repeat("b", maxEmailLocalPart+1) + "@example.com"

	out := s.guardedHTML("x " + fits + " y " + tooLong)

	s.Contains(out, `<a href="mailto:`+fits+`">`)
	s.NotContains(out, "mailto:"+tooLong)
}

// TestSpentLinkTailBudgetStaysInItsBlock: once a block's failed "](" tails spend its budget, a
// later valid inline link in that block is left as text, but other blocks are unaffected and
// shortcut references still resolve.
func (s *ParseGuardsSuite) TestSpentLinkTailBudgetStaysInItsBlock() {
	src := "[a]: /ref\n\n" + strings.Repeat("[a](", 64) + "[ok](/spent)\n\n[next](/fine)\n"

	s.Contains(s.stockHTML(src), `href="/spent"`)

	out := s.guardedHTML(src)

	s.NotContains(out, `href="/spent"`)
	s.Contains(out, "[ok](/spent)")
	s.Contains(out, `<a href="/fine">next</a>`)
	s.Contains(out, `<a href="/ref">a</a>(`)
}

func (s *ParseGuardsSuite) TestFewFailedLinkTailsKeepLaterLinks() {
	src := strings.Repeat("[a](", 8) + " [ok](/kept)\n"

	s.Equal(s.stockHTML(src), s.guardedHTML(src))
	s.Contains(s.guardedHTML(src), `href="/kept"`)
}

func (s *ParseGuardsSuite) TestUnopenedLinkClosersDoNotSpendBudget() {
	src := strings.Repeat("]( ", 256) + "[ok](/kept)\n"

	s.Equal(s.stockHTML(src), s.guardedHTML(src))
	s.Contains(s.guardedHTML(src), `href="/kept"`)
}

// TestDelimiterRunsBeyondCapRenderAsText: past maxOpenDelimiters pending runs in a block,
// further '*', '_' and '~' are literal; the next block starts with an empty stack.
func (s *ParseGuardsSuite) TestDelimiterRunsBeyondCapRenderAsText() {
	src := strings.Repeat("_a ", maxOpenDelimiters) + "*b* ~~c~~\n\n*d* ~~e~~\n"

	out := s.guardedHTML(src)

	s.Contains(out, "*b* ~~c~~")
	s.Contains(out, "<em>d</em> <del>e</del>")
}

func (s *ParseGuardsSuite) TestDelimiterRunsUpToCapMatchStock() {
	src := strings.Repeat("_a ", maxOpenDelimiters-4) + "*b* ~~c~~\n"

	s.Equal(s.stockHTML(src), s.guardedHTML(src))
}

func (s *ParseGuardsSuite) TestHeadingIDsAreUniquePerRender() {
	first, err := s.guarded.Render("# Same\n# Same\n")
	s.Require().NoError(err)

	second, err := s.guarded.Render("# Same\n")
	s.Require().NoError(err)

	s.Contains(first, `id="user-content-same-1"`)
	s.Contains(second, `id="user-content-same"`)
	s.NotContains(second, `id="user-content-same-1"`)
}
