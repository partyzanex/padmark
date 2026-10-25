package render

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// goldmark's default parsers rescan the rest of a line or block on inputs that are tiny in
// absolute terms, making render time quadratic in note size (audit L-11): with only the 4 MiB
// body cap bounding content, one stored note could pin a CPU core for hours per HTML view. The
// guards below wrap those parsers so each byte is rescanned a bounded number of times. They
// change output only on inputs no real note contains: nesting deeper than maxContainerDepth,
// e-mail local parts over 64 bytes, a block with more than maxOpenDelimiters unpaired '*', '_'
// or '~' runs, or with enough unclosed "](" to spend its link-tail budget.

// maxContainerDepth caps how deeply blockquotes and lists nest. Opening a container recomputes
// the line offset from the line head, so N levels on one line cost O(N²); 128 KiB of '>' took
// seconds. Deeper markers are rendered as text.
const maxContainerDepth = 32

// maxEmailLocalPart is the RFC 5321 limit on the local part of an address. goldmark's linkify
// scans the whole run of address characters after every '*', '_', '~', '(' or space looking for
// an '@', so "*a*a*a…" costs O(N²); a longer run cannot be an address anyway.
const maxEmailLocalPart = 64

// linkTailScanFactor bounds the bytes goldmark may rescan on failed inline-link tails ("](…"
// not closed by ')') to this multiple of the block's size. Every failed tail rescans up to the end
// of its block, so "[a](" repeated costs O(N²); once a block's budget is spent, "](" is no longer
// tried as an inline link in the rest of that block (shortcut references still resolve).
const linkTailScanFactor = 16

// maxOpenDelimiters caps the emphasis and strikethrough delimiter runs ('*', '_', '~') pending in
// one block. goldmark's ProcessDelimiters walks every closer back over all pending delimiters
// looking for an opener, so runs that never pair ("*a_b**c__d~~e…") cost O(N²); with the cap a
// walk is bounded. Further runs in the block are rendered as text.
const maxOpenDelimiters = 256

// refDefWindowLines is how many paragraph lines goldmark's link-reference transformer sees at
// once. It removes each "[label]: dest" definition by copying every line after it, so a paragraph
// of N definitions costs O(N²): 512 KiB of "[a]:b" lines took 7 s.
const refDefWindowLines = 128

// Priorities extension.Strikethrough and extension.Linkify register their parsers at.
const (
	strikethroughPriority = 500
	linkifyPriority       = 999
)

// guardedParserOptions configures goldmark's default parsers with the blockquote, list, link and
// emphasis parsers wrapped by the guards above.
func guardedParserOptions() []parser.Option {
	blockParsers := parser.DefaultBlockParsers()
	for idx, prioritized := range blockParsers {
		switch prioritized.Value {
		case parser.NewBlockquoteParser():
			blockParsers[idx].Value = &depthLimitedParser{BlockParser: parser.NewBlockquoteParser()}
		case parser.NewListParser():
			blockParsers[idx].Value = &depthLimitedParser{BlockParser: parser.NewListParser()}
		}
	}

	inlineParsers := parser.DefaultInlineParsers()
	for idx, prioritized := range inlineParsers {
		switch prioritized.Value {
		case parser.NewLinkParser():
			inlineParsers[idx].Value = &linkTailBudgetParser{inner: parser.NewLinkParser()}
		case parser.NewEmphasisParser():
			inlineParsers[idx].Value = &delimiterStackGuard{InlineParser: parser.NewEmphasisParser()}
		}
	}

	transformers := parser.DefaultParagraphTransformers()
	for idx, prioritized := range transformers {
		if prioritized.Value == parser.LinkReferenceParagraphTransformer {
			transformers[idx].Value = &windowedLinkReferences{inner: parser.LinkReferenceParagraphTransformer}
		}
	}

	return []parser.Option{
		parser.WithBlockParsers(blockParsers...),
		parser.WithInlineParsers(inlineParsers...),
		parser.WithParagraphTransformers(transformers...),
	}
}

// depthLimitedParser refuses to open a container block nested deeper than maxContainerDepth.
type depthLimitedParser struct {
	parser.BlockParser
}

// Open opens the container unless parent already sits maxContainerDepth levels deep.
//
//nolint:ireturn // implements goldmark's parser.BlockParser
func (p *depthLimitedParser) Open(parent ast.Node, reader text.Reader, pctx parser.Context) (ast.Node, parser.State) {
	depth := 0
	for node := parent; node != nil; node = node.Parent() {
		depth++
		if depth > maxContainerDepth {
			return nil, parser.NoChildren
		}
	}

	return p.BlockParser.Open(parent, reader, pctx)
}

// windowedLinkReferences feeds goldmark's link-reference transformer a long paragraph in windows
// of about refDefWindowLines lines, each cut before a line that opens with '[' (where the next
// definition would start). Definitions only lead a paragraph, so the windows run until one
// leaves lines over: those lines are retried with the next window, in case the definition they
// start was cut, and once nothing more parses they and the rest of the paragraph stay text.
type windowedLinkReferences struct {
	inner parser.ParagraphTransformer
}

// Transform behaves like the wrapped transformer, in time linear in the paragraph's length.
func (w *windowedLinkReferences) Transform(node *ast.Paragraph, reader text.Reader, pctx parser.Context) {
	lines := node.Lines()
	if lines.Len() <= refDefWindowLines {
		w.inner.Transform(node, reader, pctx)

		return
	}

	all := lines.Sliced(0, lines.Len())
	blankBefore := node.HasBlankPreviousLines()
	pending := []text.Segment{}

	for next := 0; ; {
		stop := refDefWindowStop(all, reader.Source(), next+refDefWindowLines)
		window := text.NewSegments()
		window.AppendAll(pending)
		window.AppendAll(all[next:stop])
		next = stop

		parent, sibling := node.Parent(), node.NextSibling()
		node.SetLines(window)
		w.inner.Transform(node, reader, pctx)

		if next == len(all) {
			break // the wrapped transformer finished the paragraph
		}

		if node.Parent() == nil {
			// The whole window was definitions: put the paragraph back after them for the rest.
			reattach(parent, sibling, node)
			node.SetBlankPreviousLines(false) // as goldmark: only the first definition inherits it

			pending = pending[:0]

			continue
		}

		left := node.Lines()
		if left.Len() == window.Len() {
			// Nothing parsed even with another window: the rest of the paragraph is text.
			node.SetLines(appendSegments(left, all[next:]))

			break
		}

		pending = left.Sliced(0, left.Len())
	}

	node.SetBlankPreviousLines(blankBefore)
}

// refDefWindowStop returns the index of the first line at or after from that opens with up to
// three spaces and '[', or len(lines) when there is none.
func refDefWindowStop(lines []text.Segment, source []byte, from int) int {
	for idx := from; idx < len(lines); idx++ {
		line := util.TrimLeftSpace(lines[idx].Value(source))
		if indent := lines[idx].Len() - len(line); indent <= 3 && len(line) > 0 && line[0] == '[' {
			return idx
		}
	}

	return len(lines)
}

func reattach(parent, sibling, node ast.Node) {
	if sibling == nil {
		parent.AppendChild(parent, node)

		return
	}

	parent.InsertBefore(parent, sibling, node)
}

func appendSegments(lines *text.Segments, rest []text.Segment) *text.Segments {
	merged := text.NewSegments()
	merged.AppendAll(lines.Sliced(0, lines.Len()))
	merged.AppendAll(rest)

	return merged
}

// linkTailBudgetParser wraps goldmark's link parser and charges every failed inline-link tail
// with the bytes it may have scanned, against a budget of linkTailScanFactor × the block's size.
type linkTailBudgetParser struct {
	inner parser.InlineParser
}

// linkTailBudget is the scan budget left for the block being inline-parsed.
type linkTailBudget struct {
	block     ast.Node
	remaining int
}

var linkTailBudgetKey = parser.NewContextKey() //nolint:gochecknoglobals // allocated once, like goldmark's own keys

// Trigger returns the link parser's triggers ('!', '[', ']').
func (p *linkTailBudgetParser) Trigger() []byte { return p.inner.Trigger() }

// Parse delegates to the link parser; a closing "](" either goes through (and is charged if the
// tail fails) or, with the budget spent, is shown to the parser as if '(' were not there.
//
//nolint:ireturn // implements goldmark's parser.InlineParser
func (p *linkTailBudgetParser) Parse(parent ast.Node, block text.Reader, pctx parser.Context) ast.Node {
	line, segment := block.PeekLine()
	if len(line) < 2 || line[0] != ']' || line[1] != '(' {
		return p.inner.Parse(parent, block, pctx)
	}

	budget, ok := pctx.Get(linkTailBudgetKey).(*linkTailBudget)
	if !ok {
		budget = &linkTailBudget{block: nil, remaining: 0}
		pctx.Set(linkTailBudgetKey, budget)
	}

	// Inline parsers receive the block being parsed as parent; blocks are parsed one at a time.
	if budget.block != parent {
		budget.block = parent
		budget.remaining = linkTailScanFactor * (blockStop(parent) - blockStart(parent))
	}

	paren := segment.Start + 1
	if budget.remaining <= 0 {
		return p.inner.Parse(parent, &hiddenParenReader{Reader: block, paren: paren}, pctx)
	}

	startLine, _ := block.Position()
	node := p.inner.Parse(parent, block, pctx)

	// A tail that parsed consumed everything it scanned. A failed one rewinds to just past ']'
	// (then tries a shortcut reference) after scanning at most the rest of the block. A ']'
	// without an open '[' is rejected unread and stays put, so it costs nothing.
	if endLine, end := block.Position(); endLine == startLine && end.Start == paren {
		budget.remaining -= blockStop(parent) - segment.Start
	}

	return node
}

// CloseBlock forwards to the link parser, which drops unmatched '[' openers at block end.
func (p *linkTailBudgetParser) CloseBlock(parent ast.Node, block text.Reader, pctx parser.Context) {
	if closer, ok := p.inner.(parser.CloseBlocker); ok {
		closer.CloseBlock(parent, block, pctx)
	}
}

// blockStart is the source offset where the block being inline-parsed starts.
func blockStart(node ast.Node) int {
	lines := node.Lines()
	if lines.Len() == 0 {
		return 0
	}

	return lines.At(0).Start
}

// blockStop is the source offset where the block being inline-parsed ends.
func blockStop(node ast.Node) int {
	lines := node.Lines()
	if lines.Len() == 0 {
		return 0
	}

	return lines.At(lines.Len() - 1).Stop
}

// hiddenParenReader reports no '(' at offset paren, so the link parser skips the inline-link
// tail and goes straight to the shortcut-reference fallback, exactly as after a failed tail.
type hiddenParenReader struct {
	text.Reader

	paren int
}

// Peek returns 0 at the hidden '(' and the real byte everywhere else.
func (r *hiddenParenReader) Peek() byte {
	if _, pos := r.Position(); pos.Start == r.paren && pos.Padding == 0 {
		return 0
	}

	return r.Reader.Peek()
}

// delimiterStackGuard refuses a new delimiter run once maxOpenDelimiters are pending.
type delimiterStackGuard struct {
	parser.InlineParser
}

// Parse delegates unless the block already holds maxOpenDelimiters pending delimiter runs.
//
//nolint:ireturn // implements goldmark's parser.InlineParser
func (g *delimiterStackGuard) Parse(parent ast.Node, block text.Reader, pctx parser.Context) ast.Node {
	pending := 0
	for delim := pctx.LastDelimiter(); delim != nil; delim = delim.PreviousDelimiter {
		pending++
		if pending >= maxOpenDelimiters {
			return nil
		}
	}

	return g.InlineParser.Parse(parent, block, pctx)
}

// strikethroughExtension is extension.Strikethrough with its parser wrapped by delimiterStackGuard.
type strikethroughExtension struct{}

// Extend registers the guarded strikethrough parser and its renderer at the extension's priority.
func (strikethroughExtension) Extend(md goldmark.Markdown) {
	md.Parser().AddOptions(parser.WithInlineParsers(
		util.Prioritized(&delimiterStackGuard{InlineParser: extension.NewStrikethroughParser()}, strikethroughPriority),
	))
	md.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(extension.NewStrikethroughHTMLRenderer(), strikethroughPriority),
	))
}

// linkifyExtension is extension.Linkify with its parser wrapped by linkifyGuard.
type linkifyExtension struct{}

// Extend registers the guarded linkify parser at extension.Linkify's priority.
func (linkifyExtension) Extend(md goldmark.Markdown) {
	md.Parser().AddOptions(parser.WithInlineParsers(
		util.Prioritized(newLinkifyGuard(extension.NewLinkifyParser()), linkifyPriority),
	))
}

// linkifyGuard skips goldmark's linkify parser where it could only fail: text that starts
// neither with a URL scheme or "www." nor with an e-mail local part (≤ maxEmailLocalPart bytes)
// followed by '@'.
type linkifyGuard struct {
	parser.InlineParser

	// emailLocal is goldmark's own set of e-mail local-part bytes, derived from
	// util.FindEmailIndex so the guard never disagrees with the parser it wraps.
	emailLocal [256]bool
}

func newLinkifyGuard(inner parser.InlineParser) *linkifyGuard {
	guard := &linkifyGuard{InlineParser: inner, emailLocal: [256]bool{}}

	for char := range guard.emailLocal {
		guard.emailLocal[char] = util.FindEmailIndex([]byte{byte(char), '@', 'x'}) > 0
	}

	return guard
}

// Parse runs linkify only when the text at the trigger can start a link.
//
//nolint:ireturn // implements goldmark's parser.InlineParser
func (g *linkifyGuard) Parse(parent ast.Node, block text.Reader, pctx parser.Context) ast.Node {
	line, _ := block.PeekLine()
	if !g.mayLinkify(line) {
		return nil
	}

	return g.InlineParser.Parse(parent, block, pctx)
}

func (g *linkifyGuard) mayLinkify(line []byte) bool {
	if len(line) == 0 {
		return false
	}

	// Like linkify, look past the trigger character itself.
	switch line[0] {
	case ' ', '*', '_', '~', '(':
		line = line[1:]
	}

	// The starts linkify tries as URLs (a superset is harmless: linkify itself decides).
	for _, prefix := range [...]string{"http:", "https:", "ftp:", "www."} {
		if bytes.HasPrefix(line, []byte(prefix)) {
			return true
		}
	}

	for idx := 0; idx < len(line) && idx <= maxEmailLocalPart; idx++ {
		switch {
		case line[idx] == '@':
			return idx > 0
		case !g.emailLocal[line[idx]]:
			return false
		}
	}

	return false
}
