package render

import (
	"testing"

	"github.com/microcosm-cc/bluemonday"
	"github.com/stretchr/testify/assert"
)

// TestPolicyMatchesUpstreamUGC guards the vendored copy of bluemonday.UGCPolicy: on a sample
// covering the allow-listed elements and attributes (without id) both policies must agree, so
// a bluemonday upgrade that widens or narrows the upstream set surfaces here.
func TestPolicyMatchesUpstreamUGC(t *testing.T) {
	t.Parallel()

	const sample = `<article><aside>a</aside></article>` +
		`<details open><summary>s</summary>x</details>` +
		`<h1 title="t" lang="en" dir="ltr">h</h1><hgroup><h2>g</h2></hgroup>` +
		`<blockquote cite="https://example.com/q">q</blockquote>` +
		`<p>a<br>b<wbr><span>c</span></p><hr>` +
		`<a href="https://example.com" name="n">l</a>` +
		`<map name="m"><area shape="rect" coords="1,2,3,4" href="/x" alt="x" rel="nofollow"></map>` +
		`<img src="/i.png" alt="i" width="10" height="10" align="left" usemap="#m">` +
		`<abbr>a</abbr><q cite="https://example.com">q</q><time datetime="2024-01-02">t</time>` +
		`<del cite="c" datetime="2024-01-02">d</del><ins>i</ins><ruby>r<rp>(</rp><rt>t</rt></ruby>` +
		`<ul><li>1</li></ul><ol start="2"><li>2</li></ol><dl><dt>t</dt><dd>d</dd></dl>` +
		`<table><caption>c</caption><thead><tr><th colspan="2">h</th></tr></thead>` +
		`<tbody><tr><td>1</td></tr></tbody></table>` +
		`<meter value="1" min="0" max="2"></meter><progress value="1" max="2"></progress>` +
		`<script>x()</script><iframe src="/"></iframe><form><input name="q"></form>` +
		`<div onclick="x()" style="color:red" class="foo">d</div><svg></svg><style>p{}</style>`

	upstream := bluemonday.UGCPolicy()

	assert.Equal(t, upstream.Sanitize(sample), newUGCPolicy().Sanitize(sample))
}
