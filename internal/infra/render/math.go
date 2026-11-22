package render

import (
	"bytes"
	"fmt"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// TeX formulas use the delimiters of GitHub, Pandoc and most LLM (large language model) output:
//
//   - $…$ and \(…\) in a line of text;
//   - $$…$$ in a line of text, shown as a display formula;
//   - a block that starts with $$ or \[ at the start of a line and ends with $$ or \].
//
// The parsers keep the TeX source away from Markdown (x_i is not emphasis, \\ keeps both
// backslashes) and the renderer writes it, HTML-escaped, into an element with the class
// math-inline or math-display. The browser draws the formulas with KaTeX (static/math.js).
//
// Each scan for a closing delimiter stops at a backtick or at the next character that could open
// a formula of the same kind, so every byte is scanned a bounded number of times and render time stays linear
// (see parse_guards.go). The rules for $ follow Pandoc, so "$5 and $10" stays text: the opening $
// is not followed by a space, and the closing $ is not after a space and not before a digit.

// Classes of the elements that hold a formula. static/math.js and the sanitizer use the same names.
const (
	mathInlineClass  = "math-inline"
	mathDisplayClass = "math-display"
)

// Priorities of the math parsers and renderer. The block parser runs before the paragraph
// parser and after the fenced-code parser (700); the inline triggers '$' and '\' are not used
// by other inline parsers.
const (
	mathBlockPriority    = 750
	mathInlinePriority   = 600
	mathRendererPriority = 500
)

// Delimiters of the formulas.
const (
	dollars      = "$$"
	openParen    = `\(`
	closeParen   = `\)`
	openBracket  = `\[`
	closeBracket = `\]`
)

// mathExtension adds the math parsers and renderer to goldmark. The node kinds are fields, not
// package variables: each NewRenderer registers its own kinds.
type mathExtension struct {
	inlineKind ast.NodeKind
	blockKind  ast.NodeKind
}

func newMathExtension() mathExtension {
	return mathExtension{inlineKind: ast.NewNodeKind("MathInline"), blockKind: ast.NewNodeKind("MathBlock")}
}

// Extend registers the math parsers and the math renderer.
func (e mathExtension) Extend(md goldmark.Markdown) {
	md.Parser().AddOptions(
		parser.WithBlockParsers(util.Prioritized(&mathBlockParser{kind: e.blockKind}, mathBlockPriority)),
		parser.WithInlineParsers(util.Prioritized(&mathInlineParser{kind: e.inlineKind}, mathInlinePriority)),
	)
	md.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(&mathRenderer{inlineKind: e.inlineKind, blockKind: e.blockKind}, mathRendererPriority),
	))
}

// mathInline is a formula in a line of text.
type mathInline struct {
	ast.BaseInline

	kind    ast.NodeKind
	tex     text.Segment
	display bool
}

// Kind implements ast.Node.
func (n *mathInline) Kind() ast.NodeKind { return n.kind }

// Dump implements ast.Node.
func (n *mathInline) Dump(source []byte, level int) { ast.DumpHelper(n, source, level, nil, nil) }

// mathBlock is a display formula on its own lines. Its lines hold the TeX source.
type mathBlock struct {
	ast.BaseBlock

	kind ast.NodeKind
	// brackets is true for a formula that opens with \[ and closes with \], false for $$.
	brackets bool
	// closed is true when the opening line also held the closing delimiter.
	closed bool
}

// Kind implements ast.Node.
func (n *mathBlock) Kind() ast.NodeKind { return n.kind }

// IsRaw implements ast.Node: the lines are TeX, not Markdown.
func (*mathBlock) IsRaw() bool { return true }

// Dump implements ast.Node.
func (n *mathBlock) Dump(source []byte, level int) { ast.DumpHelper(n, source, level, nil, nil) }

// closer returns the closing delimiter of the formula.
func (n *mathBlock) closer() string {
	if n.brackets {
		return closeBracket
	}

	return dollars
}

// mathInlineParser parses $…$, $$…$$ and \(…\) in a line of text.
type mathInlineParser struct {
	kind ast.NodeKind
}

// Trigger returns the first bytes of the inline delimiters.
func (*mathInlineParser) Trigger() []byte { return []byte{'$', '\\'} }

// Parse returns a formula node, or nil when the text at the trigger is not a formula.
//
//nolint:ireturn // implements goldmark's parser.InlineParser
func (p *mathInlineParser) Parse(_ ast.Node, block text.Reader, _ parser.Context) ast.Node {
	line, segment := block.PeekLine()

	found, ok := scanInlineMath(line)
	if !ok {
		return nil
	}

	base := segment.Start - segment.Padding

	block.Advance(found.end)

	formula := new(mathInline)
	formula.kind = p.kind
	formula.tex = text.NewSegment(base+found.start, base+found.stop)
	formula.display = found.display

	return formula
}

// inlineMath gives the place of a formula in a line: the TeX source is line[start:stop], and the
// formula with its delimiters is line[:end].
type inlineMath struct {
	start   int
	stop    int
	end     int
	display bool
}

func scanInlineMath(line []byte) (inlineMath, bool) {
	switch {
	case bytes.HasPrefix(line, []byte(dollars)):
		return scanDisplayDollars(line)
	case len(line) > 0 && line[0] == '$':
		return scanDollar(line)
	case bytes.HasPrefix(line, []byte(openParen)):
		return scanParens(line)
	default:
		return inlineMath{start: 0, stop: 0, end: 0, display: false}, false
	}
}

// nextDollar returns the index of the first '$' at or after from that a backslash does not
// escape, or -1 when the line has none before its end or a backtick. A formula does not cross
// a backtick, so "$10 and `$x$`" leaves the code span to the code-span parser.
func nextDollar(line []byte, from int) int {
	for idx := from; idx < len(line); idx++ {
		switch line[idx] {
		case '\n', '`':
			return -1
		case '\\':
			idx++ // The next byte is escaped, but a formula does not continue on the next line.
			if idx < len(line) && line[idx] == '\n' {
				return -1
			}
		case '$':
			return idx
		default:
			// TeX source.
		}
	}

	return -1
}

// scanDollar finds $…$: the TeX source is not empty, does not start or end with a space and
// contains no '$'; the closing '$' is not followed by a digit.
func scanDollar(line []byte) (inlineMath, bool) {
	const start = 1

	closing := nextDollar(line, start)
	if closing <= start || util.IsSpace(line[start]) || util.IsSpace(line[closing-1]) {
		return inlineMath{start: 0, stop: 0, end: 0, display: false}, false
	}

	if closing+1 < len(line) && util.IsNumeric(line[closing+1]) {
		return inlineMath{start: 0, stop: 0, end: 0, display: false}, false
	}

	return inlineMath{start: start, stop: closing, end: closing + 1, display: false}, true
}

// scanDisplayDollars finds $$…$$ in a line: the TeX source is not blank and contains no '$'.
func scanDisplayDollars(line []byte) (inlineMath, bool) {
	start := len(dollars)

	closing := nextDollar(line, start)
	if closing < 0 || !bytes.HasPrefix(line[closing:], []byte(dollars)) || util.IsBlank(line[start:closing]) {
		return inlineMath{start: 0, stop: 0, end: 0, display: false}, false
	}

	return inlineMath{start: start, stop: closing, end: closing + len(dollars), display: true}, true
}

// scanParens finds \(…\): the TeX source is not blank and contains no other \( and no backtick.
func scanParens(line []byte) (inlineMath, bool) {
	start := len(openParen)

	for idx := start; idx+1 < len(line) && line[idx] != '\n' && line[idx] != '`'; idx++ {
		if line[idx] != '\\' {
			continue
		}

		switch line[idx+1] {
		case '(', '\n':
			return inlineMath{start: 0, stop: 0, end: 0, display: false}, false
		case ')':
			if util.IsBlank(line[start:idx]) {
				return inlineMath{start: 0, stop: 0, end: 0, display: false}, false
			}

			return inlineMath{start: start, stop: idx, end: idx + len(closeParen), display: false}, true
		default:
			idx++ // The next byte is escaped.
		}
	}

	return inlineMath{start: 0, stop: 0, end: 0, display: false}, false
}

// mathBlockParser parses a display formula that starts a line with $$ or \[. When the opening
// line holds only the delimiter, the formula continues to the line that ends with $$ or \], or,
// like a fenced code block, to the end of its container.
type mathBlockParser struct {
	kind ast.NodeKind
}

// Trigger returns the first bytes of the block delimiters.
func (*mathBlockParser) Trigger() []byte { return []byte{'$', '\\'} }

// Open starts a formula block, or returns nil when the line is not one.
//
//nolint:ireturn // implements goldmark's parser.BlockParser
func (p *mathBlockParser) Open(_ ast.Node, reader text.Reader, pctx parser.Context) (ast.Node, parser.State) {
	line, segment := reader.PeekLine()

	pos := pctx.BlockOffset()
	if pos < 0 {
		return nil, parser.NoChildren
	}

	node := new(mathBlock)
	node.kind = p.kind

	var opener string

	switch {
	case bytes.HasPrefix(line[pos:], []byte(dollars)):
		opener = dollars
	case bytes.HasPrefix(line[pos:], []byte(openBracket)):
		opener, node.brackets = openBracket, true
	default:
		return nil, parser.NoChildren
	}

	start := pos + len(opener)
	rest := util.TrimRightSpace(line[start:])

	if len(util.TrimLeftSpace(rest)) == 0 {
		reader.AdvanceToEOL()

		return node, parser.NoChildren
	}

	// One line: the line ends with the closing delimiter, with TeX source before it. Otherwise
	// the line is text, and the inline parser can still find $$…$$ in it.
	closer := node.closer()
	stop := len(rest) - len(closer)

	if stop < 0 || !bytes.HasSuffix(rest, []byte(closer)) || util.IsBlank(rest[:stop]) {
		return nil, parser.NoChildren
	}

	base := segment.Start - segment.Padding
	node.Lines().Append(text.NewSegment(base+start, base+start+stop))
	node.closed = true

	reader.AdvanceToEOL()

	return node, parser.NoChildren
}

// Continue adds a line to the formula, and closes it at the line that ends with the delimiter.
func (*mathBlockParser) Continue(node ast.Node, reader text.Reader, _ parser.Context) parser.State {
	block, ok := node.(*mathBlock)
	if !ok || block.closed {
		return parser.Close
	}

	line, segment := reader.PeekLine()
	rest := util.TrimRightSpace(line)

	if closer := block.closer(); bytes.HasSuffix(rest, []byte(closer)) {
		stop := len(rest) - len(closer)
		if !util.IsBlank(rest[:stop]) {
			base := segment.Start - segment.Padding
			block.Lines().Append(text.NewSegment(base, base+stop))
		}

		reader.AdvanceToEOL()

		return parser.Close
	}

	segment.ForceNewline = true
	block.Lines().Append(segment)
	reader.AdvanceToEOL()

	return parser.Continue | parser.NoChildren
}

// Close implements parser.BlockParser; the formula needs no work at close.
func (*mathBlockParser) Close(ast.Node, text.Reader, parser.Context) {}

// CanInterruptParagraph lets a formula follow a text line without a blank line, as LLM output does.
func (*mathBlockParser) CanInterruptParagraph() bool { return true }

// CanAcceptIndentedLine reports false: an indented $$ line is code, as in CommonMark.
func (*mathBlockParser) CanAcceptIndentedLine() bool { return false }

// mathRenderer writes a formula as its HTML-escaped TeX source in a math-inline or math-display
// element.
type mathRenderer struct {
	inlineKind ast.NodeKind
	blockKind  ast.NodeKind
}

// RegisterFuncs implements renderer.NodeRenderer.
func (r *mathRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(r.inlineKind, onEnter(renderMathInline))
	reg.Register(r.blockKind, onEnter(renderMathBlock))
}

// onEnter adapts a formula writer to goldmark: a formula has no child nodes, so the writer runs
// once, when the walk enters the node.
func onEnter(write func(w util.BufWriter, source []byte, node ast.Node) error) renderer.NodeRendererFunc {
	return func(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkSkipChildren, nil
		}

		return ast.WalkSkipChildren, write(w, source, node)
	}
}

func renderMathInline(w util.BufWriter, source []byte, node ast.Node) error {
	formula, ok := node.(*mathInline)
	if !ok {
		return nil
	}

	class := mathInlineClass
	if formula.display {
		class = mathDisplayClass
	}

	html := append([]byte(`<span class="`+class+`">`), util.EscapeHTML(formula.tex.Value(source))...)

	return writeHTML(w, append(html, "</span>"...))
}

func renderMathBlock(w util.BufWriter, source []byte, node ast.Node) error {
	html := []byte(`<div class="` + mathDisplayClass + `">`)

	lines := node.Lines()
	for idx := range lines.Len() {
		segment := lines.At(idx)
		html = append(html, util.EscapeHTML(segment.Value(source))...)
	}

	return writeHTML(w, append(html, "</div>\n"...))
}

func writeHTML(w util.BufWriter, html []byte) error {
	_, err := w.Write(html)
	if err != nil {
		return fmt.Errorf("write formula: %w", err)
	}

	return nil
}
