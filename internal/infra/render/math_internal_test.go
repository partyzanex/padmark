package render

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// newTestInline makes an inline formula node with the source segment tex.
func newTestInline(tex text.Segment, display bool) *mathInline {
	formula := new(mathInline)
	formula.kind = ast.KindText
	formula.tex = tex
	formula.display = display

	return formula
}

// newTestBlock makes a $$ formula block node.
func newTestBlock(closed bool) *mathBlock {
	block := new(mathBlock)
	block.kind = ast.KindText
	block.closed = closed

	return block
}

// noMath is the result of a scan that finds no formula.
func noMath() inlineMath { return inlineMath{start: 0, stop: 0, end: 0, display: false} }

func TestScanInlineMath_Finds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		line string
		tex  string
		rest string
		want bool // display
	}{
		{line: "$x$", tex: "x", rest: "", want: false},
		{line: "$a_1$ and more", tex: "a_1", rest: " and more", want: false},
		{line: "$x$\n", tex: "x", rest: "\n", want: false},
		{line: "$x$y$", tex: "x", rest: "y$", want: false},
		{line: "$x$.", tex: "x", rest: ".", want: false},
		{line: "$a<b$", tex: "a<b", rest: "", want: false},
		{line: `$a\$b$`, tex: `a\$b`, rest: "", want: false},
		{line: `$\$$`, tex: `\$`, rest: "", want: false},
		{line: `$\\$`, tex: `\\`, rest: "", want: false},
		{line: `$a \(b\) c$`, tex: `a \(b\) c`, rest: "", want: false},
		{line: "$Σ_i$", tex: "Σ_i", rest: "", want: false},
		{line: "$$x$$", tex: "x", rest: "", want: true},
		{line: "$$ x $$ tail", tex: " x ", rest: " tail", want: true},
		{line: "$$x$$$", tex: "x", rest: "$", want: true},
		{line: `$$a\$b$$`, tex: `a\$b`, rest: "", want: true},
		{line: `\(x\)`, tex: "x", rest: "", want: false},
		{line: `\( x \)!`, tex: " x ", rest: "!", want: false},
		{line: `\(p \$ q\)`, tex: `p \$ q`, rest: "", want: false},
		{line: `\(a\\\)`, tex: `a\\`, rest: "", want: false},
		{line: `\($x$\)`, tex: "$x$", rest: "", want: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.line, func(t *testing.T) {
			t.Parallel()

			found, ok := scanInlineMath([]byte(testCase.line))
			require.True(t, ok, "a formula must be found")
			assert.Equal(t, testCase.tex, testCase.line[found.start:found.stop])
			assert.Equal(t, testCase.rest, testCase.line[found.end:])
			assert.Equal(t, testCase.want, found.display)
		})
	}
}

func TestScanInlineMath_Refuses(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"empty line":                   "",
		"no trigger":                   "x$a$",
		"lone dollar":                  "$",
		"dollar at line end":           "$\n",
		"empty dollars":                "$$",
		"unclosed dollar":              "$a",
		"unclosed before newline":      "$a\nb$",
		"space after opener":           "$ a$",
		"tab after opener":             "$\ta$",
		"space before closer":          "$a $",
		"digit after closer":           "$a$1",
		"prices":                       "$5 and $10",
		"crosses backtick":             "$a`b$",
		"escaped closer only":          `$a\$`,
		"trailing backslash":           `$a\`,
		"escaped newline":              "$a\\\nb$",
		"four dollars":                 "$$$$",
		"blank display":                "$$ $$",
		"tab display":                  "$$\t$$",
		"display closed by one dollar": "$$a$b$$",
		"unclosed display":             "$$a",
		"display crosses backtick":     "$$a`b$$",
		"display over newline":         "$$a\nb$$",
		"escaped dollar":               `\$a$`,
		"lone backslash":               `\`,
		"bracket is not inline":        `\[a\]`,
		"lone paren opener":            `\(`,
		"blank parens":                 `\( \)`,
		"empty parens":                 `\(\)`,
		"unclosed parens":              `\(a`,
		"parens closed after newline":  "\\(a\n\\)",
		"nested paren opener":          `\(a\(b\)`,
		"parens cross backtick":        "\\(a`\\)",
		"escaped paren closer":         `\(a\\)`,
		"backslash before line end":    "\\(a\\",
		"parens escaped newline":       "\\(a\\\n\\)",
	}

	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			found, ok := scanInlineMath([]byte(line))
			assert.False(t, ok, "%q must stay text", line)
			assert.Equal(t, noMath(), found)
		})
	}
}

func TestNextDollar(t *testing.T) {
	t.Parallel()

	cases := []struct {
		line string
		from int
		want int
	}{
		{line: "a$", from: 0, want: 1},
		{line: "$a$", from: 1, want: 2},
		{line: `\$a$`, from: 0, want: 3},
		{line: `\\$`, from: 0, want: 2},
		{line: "a\n$", from: 0, want: -1},
		{line: "a`$", from: 0, want: -1},
		{line: "abc", from: 0, want: -1},
		{line: `a\`, from: 0, want: -1},
		{line: "a\\\n$", from: 0, want: -1},
		{line: "$", from: 1, want: -1},
		{line: "", from: 0, want: -1},
	}

	for _, testCase := range cases {
		got := nextDollar([]byte(testCase.line), testCase.from)
		assert.Equal(t, testCase.want, got, "nextDollar(%q, %d)", testCase.line, testCase.from)
	}
}

func TestMathBlockParser_OpenWithoutBlockOffset(t *testing.T) {
	t.Parallel()

	blockParser := &mathBlockParser{kind: ast.KindParagraph}
	reader := text.NewReader([]byte("$$\nx\n$$\n"))

	node, state := blockParser.Open(ast.NewDocument(), reader, parser.NewContext())
	assert.Nil(t, node, "a line without a block offset is not a formula")
	assert.Equal(t, parser.NoChildren, state)
}

func TestMathBlockParser_ContinueClosesOtherNode(t *testing.T) {
	t.Parallel()

	blockParser := &mathBlockParser{kind: ast.KindParagraph}
	reader := text.NewReader([]byte("x\n"))

	state := blockParser.Continue(ast.NewParagraph(), reader, parser.NewContext())
	assert.Equal(t, parser.Close, state)
}

func TestMathBlockParser_Flags(t *testing.T) {
	t.Parallel()

	blockParser := &mathBlockParser{kind: ast.KindParagraph}
	assert.Equal(t, []byte{'$', '\\'}, blockParser.Trigger())
	assert.True(t, blockParser.CanInterruptParagraph())
	assert.False(t, blockParser.CanAcceptIndentedLine())
	assert.Equal(t, []byte{'$', '\\'}, (&mathInlineParser{kind: ast.KindText}).Trigger())

	blockParser.Close(ast.NewParagraph(), text.NewReader(nil), parser.NewContext())
}

func TestMathExtension_KindsDiffer(t *testing.T) {
	t.Parallel()

	first, second := newMathExtension(), newMathExtension()
	assert.NotEqual(t, first.inlineKind, first.blockKind)
	assert.NotEqual(t, first.inlineKind, second.inlineKind, "each renderer registers its own kinds")
	assert.Equal(t, "MathInline", first.inlineKind.String())
	assert.Equal(t, "MathBlock", first.blockKind.String())
}

func TestMathNodes(t *testing.T) {
	t.Parallel()

	inline := newTestInline(text.NewSegment(0, 1), false)
	block := newTestBlock(false)

	assert.Equal(t, ast.KindText, inline.Kind())
	assert.Equal(t, ast.KindText, block.Kind())
	assert.True(t, block.IsRaw(), "the lines of a formula are TeX, not Markdown")
}

// TestMathNodes_Dump covers the debug dump. ast.DumpHelper prints to os.Stdout, so the test
// replaces it and must not run in parallel.
//
//nolint:paralleltest // replaces os.Stdout
func TestMathNodes_Dump(t *testing.T) {
	source := []byte("$x$")
	inline := newTestInline(text.NewSegment(1, 2), false)
	block := newTestBlock(false)

	read, write, err := os.Pipe()
	require.NoError(t, err)

	stdout := os.Stdout
	os.Stdout = write

	inline.Dump(source, 0)
	block.Dump(source, 0)

	os.Stdout = stdout

	require.NoError(t, write.Close())

	out, err := io.ReadAll(read)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(out), "Text {"), "each node prints its kind")
}

func TestOnEnter_SkipsExit(t *testing.T) {
	t.Parallel()

	calls := 0
	render := onEnter(func(util.BufWriter, []byte, ast.Node) error {
		calls++

		return nil
	})

	var out strings.Builder

	status, err := render(bufio.NewWriter(&out), nil, ast.NewText(), false)
	require.NoError(t, err)
	assert.Equal(t, ast.WalkSkipChildren, status)
	assert.Zero(t, calls, "nothing is written when the walk leaves the node")

	status, err = render(bufio.NewWriter(&out), nil, ast.NewText(), true)
	require.NoError(t, err)
	assert.Equal(t, ast.WalkSkipChildren, status, "a formula has no children to walk")
	assert.Equal(t, 1, calls)
}

func TestRenderMathInline_OtherNodeWritesNothing(t *testing.T) {
	t.Parallel()

	var out strings.Builder

	writer := bufio.NewWriter(&out)
	require.NoError(t, renderMathInline(writer, nil, ast.NewText()))
	require.NoError(t, writer.Flush())
	assert.Empty(t, out.String())
}

// errWriter fails every write.
type errWriter struct{}

var errWrite = errors.New("disk full")

func (errWriter) Write([]byte) (int, error) { return 0, errWrite }

func TestRenderMath_WriteErrorIsReturned(t *testing.T) {
	t.Parallel()

	source := []byte(strings.Repeat("x", 64))
	inline := newTestInline(text.NewSegment(0, len(source)), true)

	block := newTestBlock(true)
	block.Lines().Append(text.NewSegment(0, len(source)))

	// A buffer smaller than the HTML makes the write reach errWriter.
	err := renderMathInline(bufio.NewWriterSize(errWriter{}, 16), source, inline)
	require.ErrorIs(t, err, errWrite)
	assert.Contains(t, err.Error(), "write formula")

	err = renderMathBlock(bufio.NewWriterSize(errWriter{}, 16), source, block)
	require.ErrorIs(t, err, errWrite)
}
