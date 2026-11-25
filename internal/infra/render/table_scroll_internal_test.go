package render

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestWrapTables checks the token step on sanitized HTML, also on HTML that Markdown cannot make.
func TestWrapTables(t *testing.T) {
	t.Parallel()

	const (
		boxOpen  = `<div class="table-scroll">`
		boxClose = `</div>`
	)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "no table", in: `<p>a &lt;table&gt; b</p>`, want: `<p>a &lt;table&gt; b</p>`},
		{
			name: "one table",
			in:   `<p>a</p><table><tr><td>1</td></tr></table><p>b</p>`,
			want: `<p>a</p>` + boxOpen + `<table><tr><td>1</td></tr></table>` + boxClose + `<p>b</p>`,
		},
		{
			name: "attributes stay",
			in:   `<table summary="s" width="50%"><tr><td>1</td></tr></table>`,
			want: boxOpen + `<table summary="s" width="50%"><tr><td>1</td></tr></table>` + boxClose,
		},
		{
			name: "nested table stays in the outer box",
			in:   `<table><tr><td><table><tr><td>in</td></tr></table></td></tr></table>`,
			want: boxOpen + `<table><tr><td><table><tr><td>in</td></tr></table></td></tr></table>` + boxClose,
		},
		{
			name: "two tables",
			in:   `<table></table><table></table>`,
			want: boxOpen + `<table></table>` + boxClose + boxOpen + `<table></table>` + boxClose,
		},
		{
			name: "upper case tags",
			in:   `<TABLE><tr><td>1</td></tr></TABLE>`,
			want: boxOpen + `<TABLE><tr><td>1</td></tr></TABLE>` + boxClose,
		},
		{
			name: "self-closing tag opens a table",
			in:   `<table/><tr><td>1</td></tr></table>`,
			want: boxOpen + `<table/><tr><td>1</td></tr></table>` + boxClose,
		},
		{
			name: "unclosed tables are closed at the end",
			in:   `<table><tr><td><table><tr><td>x`,
			want: boxOpen + `<table><tr><td><table><tr><td>x</table></table>` + boxClose,
		},
		{name: "end tag without a table", in: `<p>a</p></table>`, want: `<p>a</p></table>`},
		{
			name: "text with a table word",
			in:   `<pre><code>&lt;table&gt;</code></pre><table></table>`,
			want: `<pre><code>&lt;table&gt;</code></pre>` + boxOpen + `<table></table>` + boxClose,
		},
		{name: "tablex is not a table", in: `<tablex></tablex><table`, want: `<tablex></tablex><table`},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, testCase.want, wrapTables(testCase.in))
		})
	}
}

// TestWrapTablesKeepsOtherBytes checks that the step copies everything else without a change.
func TestWrapTablesKeepsOtherBytes(t *testing.T) {
	t.Parallel()

	in := `<p title="a &amp; b">x &lt; y</p><table><tr><td>é &#34;</td></tr></table>`
	out := wrapTables(in)

	assert.Equal(t, in, strings.NewReplacer(`<div class="table-scroll">`, "", "</div>", "").Replace(out))
}
