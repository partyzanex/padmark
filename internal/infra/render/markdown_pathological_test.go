package render_test

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/partyzanex/padmark/internal/infra/render"
)

// Pathological-input cost of Renderer.Render (audit L-11).
//
// maxContentBytes mirrors server.DefaultMaxBodyBytes (--max-body-bytes, 4 MiB), the only bound on
// note content: the domain caps only the title, the OpenAPI schema puts no maxLength on content,
// and notes.Manager renders on every HTML view (GetRendered → renderNote) with no cache. Render
// takes no context, so a slow render keeps burning its goroutine after the client goes away.
//
// Measured (Apple Silicon, no -race): most inputs scale linearly (worst: many chroma-highlighted
// fences, ~15 s per 4 MiB render), but stock goldmark v1.8.6 is quadratic on several inputs that
// are tiny in absolute terms — e.g. 16 KiB of empty headings took ~3.6 s (ids.Generate probes
// "heading-1", "heading-2", ... from 1 for every duplicate), extrapolating to tens of hours at
// the 4 MiB cap. The parse guards (parse_guards.go, heading_ids.go) make all of them linear.
const maxContentBytes = 4 << 20

// superLinearBudget is what a probe-size render of a once-super-linear case may take. The guarded
// renderer handles every probe input in under 100 ms; stock goldmark took 2–10 s on each of them.
const superLinearBudget = time.Second

// linearWorstCaseBudget bounds the worst linear-time input (fence_many_langs) at the full cap.
// Measured ~15 s; the budget is ~5× that so only a real complexity regression trips it.
const linearWorstCaseBudget = 75 * time.Second

// raceSlowdown scales budgets when the binary is built with -race (roughly 5–10× slower).
const raceSlowdown = 10

// coverSlowdown scales budgets when coverage instrumentation is on: counters in the parser's hot
// loops make each render several times slower, and the budget only has to catch a change in
// complexity, not in constant. With -race as well (make test) the counters are atomic and
// contended across the parallel renders — measured 30–40 s for a 100 ms render — so the budget
// test is skipped there, like TestRenderLinearWorstCaseAtMaxSize.
const coverSlowdown = 10

type pathoCase struct {
	name string
	gen  func(size int) string
	// probe is non-zero for inputs stock goldmark renders in super-linear time: the size used by
	// the budget test and the benchmarks instead of maxContentBytes, chosen so stock goldmark
	// takes several seconds while a linear renderer takes a few hundred ms at most.
	probe int
}

// fill repeats unit until the result is (just under) size bytes.
func fill(unit string, size int) string {
	return strings.Repeat(unit, size/len(unit))
}

// staircase concatenates line(depth) for depth 0, 1, 2, ... restarting at 0 after maxDepth,
// until size bytes are produced.
func staircase(size, maxDepth int, line func(depth int) string) string {
	var builder strings.Builder

	builder.Grow(size)

	for depth := 0; ; depth = (depth + 1) % maxDepth {
		chunk := line(depth)
		if builder.Len()+len(chunk) > size {
			return builder.String()
		}

		builder.WriteString(chunk)
	}
}

func backtickRuns(size int) string {
	return staircase(size, 2000, func(depth int) string {
		return strings.Repeat("`", depth+1) + "a"
	})
}

func linkRefDefs(size int) string {
	var builder strings.Builder

	builder.Grow(size)

	for idx := 0; builder.Len() < size/2; idx++ {
		fmt.Fprintf(&builder, "[r%d]: /u%d\n", idx, idx)
	}

	builder.WriteString("\n")

	for idx := 0; builder.Len() < size-16; idx++ {
		fmt.Fprintf(&builder, "[r%d] [x%d][] ", idx, idx)
	}

	return builder.String()
}

func wideTable(size int) string {
	const cols = 5000

	var builder strings.Builder

	builder.Grow(size)
	builder.WriteString(strings.Repeat("|h", cols) + "|\n")
	builder.WriteString(strings.Repeat("|-", cols) + "|\n")

	row := strings.Repeat("|c", cols) + "|\n"
	for builder.Len()+len(row) <= size {
		builder.WriteString(row)
	}

	return builder.String()
}

const goSnippet = `func handler(w http.ResponseWriter, r *http.Request) {
	ids := []int{1, 2, 3}
	for _, id := range ids { fmt.Fprintf(w, "id=%d\n", id) } // comment
}
`

const htmlSnippet = `<div class="x" id="y"><a href="/p?q=1&r=2">link</a><script>var x = "s";</script></div>
`

func fence(lang, body string, size int) string {
	head := "```" + lang + "\n"

	return head + fill(body, size-len(head)-4) + "```\n"
}

func fenceLangs() []string {
	return []string{"go", "html", "javascript", "python", "sql", "bash", "rust", "yaml", "c", "java"}
}

// manyLangFences is the worst linear-time input: tiny fences cycling through chroma lexers.
func manyLangFences(size int) string {
	var builder strings.Builder

	builder.Grow(size)

	langs := fenceLangs()

	for idx := 0; ; idx++ {
		block := "```" + langs[idx%len(langs)] + "\nx = f(1, \"s\") // c\n```\n"
		if builder.Len()+len(block) > size {
			return builder.String()
		}

		builder.WriteString(block)
	}
}

func realistic(size int) string {
	var builder strings.Builder

	builder.Grow(size)

	for idx := 0; ; idx++ {
		section := fmt.Sprintf(`## Section %d

Some **bold** text, some *emphasis*, a [link](https://example.com/p/%d) and `+"`inline code`"+`.
Another line with www.example.org/%d autolink and ~~strike~~.

- item one
- item two
  - nested item

> a quote line
> continues here

| col a | col b | col c |
|-------|-------|-------|
| 1     | 2     | 3     |

`+"```go\nfunc f%d() int { return %d }\n```\n\n", idx, idx, idx, idx, idx)
		if builder.Len()+len(section) > size {
			return builder.String()
		}

		builder.WriteString(section)
	}
}

// duplicateHeadings is a run of empty ATX headings: every one gets the auto ID "heading", and
// goldmark's ids.Generate probes "heading-1", "heading-2", ... from 1 for each new duplicate, so
// N headings cost O(N²) Sprintf calls plus map lookups (parser.WithAutoHeadingID).
func duplicateHeadings(size int) string { return fill("#\n", size) }

func linearPathoCase(name string, gen func(int) string) pathoCase {
	return pathoCase{name: name, gen: gen, probe: 0}
}

func pathoCases() []pathoCase {
	return []pathoCase{
		linearPathoCase("realistic_baseline", realistic),
		// Super-linear on stock goldmark (exponent ≈ 2 between the last two measured sizes).
		{name: "headings_duplicate", gen: duplicateHeadings, probe: 16 << 10},
		{name: "brackets_link_pointy", gen: func(n int) string { return fill("[a](<b", n) }, probe: 256 << 10},
		{name: "brackets_link_balanced", gen: func(n int) string { return fill("[](a(b)", n) }, probe: 128 << 10},
		{name: "brackets_link_shortcut_ref", gen: func(n int) string {
			return "[a]: /x\n\n" + fill("[a](", n)
		}, probe: 128 << 10},
		{name: "heading_link_prefix", gen: func(n int) string { return "# " + fill("[a](", n) }, probe: 128 << 10},
		{name: "strikethrough_mixed", gen: func(n int) string { return fill("~~a~b", n) }, probe: 256 << 10},
		{name: "emphasis_underscore_star", gen: func(n int) string { return fill("_a*", n) }, probe: 128 << 10},
		{name: "blockquote_nested_spaced", gen: func(n int) string { return fill("> ", n) }, probe: 256 << 10},
		// Every removed "[label]: dest" line copies the rest of the paragraph (link_ref.go).
		{name: "link_ref_defs_flood", gen: func(n int) string { return fill("[a]:b\n", n) }, probe: 512 << 10},
		{name: "brackets_link_prefix", gen: func(n int) string { return fill("[a](", n) }, probe: 128 << 10},
		{name: "emphasis_unclosed_star", gen: func(n int) string { return fill("*a", n) }, probe: 256 << 10},
		{name: "emphasis_mixed", gen: func(n int) string { return fill("*a_b**c__d~~e", n) }, probe: 256 << 10},
		{name: "backtick_unmatched", gen: func(n int) string { return fill("`a``a", n) }, probe: 512 << 10},
		// One line of N '>' re-scans the line prefix per level (text.reader.LineOffset).
		{name: "blockquote_nested_1line", gen: func(n int) string { return fill(">", n) }, probe: 128 << 10},
		// Mildly super-linear on stock goldmark (exponent 1.4–1.8): measured, not budget-tested.
		linearPathoCase("list_nested_indent", func(n int) string {
			return staircase(n, 1000, func(d int) string { return strings.Repeat("  ", d) + "- x\n" })
		}),
		linearPathoCase("link_ref_defs", linkRefDefs),
		// Linear.
		linearPathoCase("blockquote_nested_lines", func(n int) string { return fill(strings.Repeat(">", 1000)+"x\n", n) }),
		linearPathoCase("emphasis_openers_closers", func(n int) string { return fill("*a ", n/2) + fill(" a*", n/2) }),
		linearPathoCase("brackets_open", func(n int) string { return fill("[", n) }),
		linearPathoCase("brackets_close_paren", func(n int) string { return fill("](", n) }),
		linearPathoCase("brackets_image_open", func(n int) string { return fill("![", n) }),
		linearPathoCase("backtick_runs", backtickRuns),
		linearPathoCase("fence_go_huge", func(n int) string { return fence("go", goSnippet, n) }),
		linearPathoCase("fence_html_huge", func(n int) string { return fence("html", htmlSnippet, n) }),
		linearPathoCase("fence_go_many_small", func(n int) string {
			return fill("```go\nfunc f() int { return 1 }\n```\n", n)
		}),
		linearPathoCase("fence_many_langs", manyLangFences),
		linearPathoCase("html_div_nested_1line", func(n int) string { return fill("<div>", n) }),
		linearPathoCase("html_div_nested_lines", func(n int) string { return fill("<div>\n", n) }),
		linearPathoCase("html_inline_span", func(n int) string { return fill("a<span>", n) }),
		linearPathoCase("table_wide", wideTable),
		linearPathoCase("table_long", func(n int) string { return "|a|b|c|\n|-|-|-|\n" + fill("|1|2|3|\n", n-16) }),
		linearPathoCase("single_line_words", func(n int) string { return fill("lorem ", n) }),
		linearPathoCase("single_line_nospace", func(n int) string { return fill("a", n) }),
		linearPathoCase("entities_named", func(n int) string { return fill("&amp;", n) }),
		linearPathoCase("entities_numeric_invalid", func(n int) string { return fill("&#x1F600;&#99999999;&bogus;", n) }),
		linearPathoCase("linkify_urls", func(n int) string { return fill("www.example.com ", n) }),
		linearPathoCase("linkify_partial", func(n int) string { return fill("http://", n) }),
		linearPathoCase("hard_wraps", func(n int) string { return fill("a\n", n) }),
		linearPathoCase("headings_unique", func(n int) string {
			return staircase(n, 1<<30, func(d int) string { return fmt.Sprintf("# h%d\n", d) })
		}),
	}
}

func raceEnabled() bool {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}

	for _, setting := range info.Settings {
		if setting.Key == "-race" {
			return setting.Value == "true"
		}
	}

	return false
}

// renderWithin renders input in a separate goroutine and reports whether it finished within
// budget. On timeout the goroutine is abandoned (Render takes no context) and keeps running until
// the test binary exits; the probe sizes keep that bounded to seconds.
func renderWithin(rnd *render.Renderer, input string, budget time.Duration) (time.Duration, bool) {
	done := make(chan time.Duration, 1)

	go func() {
		start := time.Now()
		_, _ = rnd.Render(input)

		done <- time.Since(start)
	}()

	timer := time.NewTimer(budget)
	defer timer.Stop()

	select {
	case elapsed := <-done:
		return elapsed, elapsed <= budget
	case <-timer.C:
		return budget, false
	}
}

// TestRenderSuperLinearInputsWithinBudget guards audit L-11: on stock goldmark v1.8.6 each input
// below is a small fraction of the 4 MiB content cap yet takes seconds to render, and its cost
// grows ~quadratically, so one stored note could pin a CPU core for minutes to hours per HTML
// view. The parse guards keep each render linear; a budget overrun means a guard regressed or a
// goldmark upgrade bypassed one. Each subtest gives up after its budget.
func TestRenderSuperLinearInputsWithinBudget(t *testing.T) {
	t.Parallel()

	if testing.CoverMode() != "" && raceEnabled() {
		t.Skip("atomic coverage counters under -race make render timing meaningless")
	}

	budget := superLinearBudget
	if raceEnabled() {
		budget *= raceSlowdown
	}

	if testing.CoverMode() != "" {
		budget *= coverSlowdown
	}

	rnd := render.NewRenderer()

	for _, testCase := range pathoCases() {
		if testCase.probe == 0 {
			continue
		}

		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			input := testCase.gen(testCase.probe)

			elapsed, ok := renderWithin(rnd, input, budget)
			t.Logf("%s: %d KiB rendered in %s (budget %s)", testCase.name, len(input)>>10,
				elapsed.Round(time.Millisecond), budget)

			if !ok {
				t.Errorf("rendering %d KiB of %s took >= %s (budget %s); content cap is %d KiB, "+
					"so render cost is unbounded by the body limit", len(input)>>10, testCase.name,
					elapsed.Round(time.Millisecond), budget, maxContentBytes>>10)
			}
		})
	}
}

// TestRenderLinearWorstCaseAtMaxSize guards the worst linear-time input (many chroma-highlighted
// fences) at the real max content size against a complexity regression. Heavy (~15 s), so it is
// skipped under -short and -race.
func TestRenderLinearWorstCaseAtMaxSize(t *testing.T) {
	if testing.Short() {
		t.Skip("renders 4 MiB of highlighted fences (~15 s)")
	}

	if raceEnabled() {
		t.Skip("-race slows rendering ~10×; timing budget is meaningless")
	}

	rnd := render.NewRenderer()
	input := manyLangFences(maxContentBytes)

	elapsed, ok := renderWithin(rnd, input, linearWorstCaseBudget)
	if !ok {
		t.Fatalf("rendering %d KiB of fence_many_langs took >= %s (budget %s)",
			len(input)>>10, elapsed.Round(time.Millisecond), linearWorstCaseBudget)
	}

	t.Logf("fence_many_langs %d KiB rendered in %s", len(input)>>10, elapsed.Round(time.Millisecond))
}

// BenchmarkRenderPathological renders every input at 1/4, 1/2 and 1× of its benchmark size: the
// real max content size for linear inputs, the probe size for super-linear ones (their full-size
// render takes minutes to hours). Compare ns/op across the three sizes to read the scaling.
//
//	go test -run xxx -bench RenderPathological -benchmem ./internal/infra/render/
func BenchmarkRenderPathological(b *testing.B) {
	rnd := render.NewRenderer()

	for _, testCase := range pathoCases() {
		size := maxContentBytes
		if testCase.probe > 0 {
			size = testCase.probe
		}

		for _, div := range []int{4, 2, 1} {
			input := testCase.gen(size / div)

			b.Run(fmt.Sprintf("%s/%dKiB", testCase.name, len(input)>>10), func(b *testing.B) {
				b.SetBytes(int64(len(input)))

				for b.Loop() {
					_, err := rnd.Render(input)
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkRenderPlainMaxSize is the text/plain path (html.EscapeString) at the max size.
func BenchmarkRenderPlainMaxSize(b *testing.B) {
	rnd := render.NewRenderer()
	input := fill("<a&\"b>", maxContentBytes)

	b.SetBytes(int64(len(input)))

	for b.Loop() {
		_, err := rnd.RenderPlain(input)
		if err != nil {
			b.Fatal(err)
		}
	}
}

type measurement struct {
	elapsed time.Duration
	allocMB float64
}

func measureRender(tb testing.TB, input string, renderFn func(string) (string, error)) measurement {
	tb.Helper()

	var before, after runtime.MemStats

	runtime.GC()
	runtime.ReadMemStats(&before)

	start := time.Now()
	_, err := renderFn(input)
	elapsed := time.Since(start)

	runtime.ReadMemStats(&after)

	if err != nil {
		tb.Fatalf("render: %v", err)
	}

	const mib = 1 << 20

	return measurement{elapsed: elapsed, allocMB: float64(after.TotalAlloc-before.TotalAlloc) / mib}
}

// measureSizes are probed in ascending order; a super-linear case stops growing once one render
// exceeds measureStopAfter, and its full-size cost is extrapolated from the last two points.
func measureSizes() []int {
	return []int{1 << 10, 4 << 10, 16 << 10, 64 << 10, 256 << 10, 1 << 20, 2 << 20, maxContentBytes}
}

const measureStopAfter = time.Second

// TestRenderPathologicalMeasure prints, per input kind, the render time at growing sizes up to the
// real max, the scaling exponent between the last two points, the (extrapolated) full-size time
// and allocations, and the CPU-seconds per second one IP can trigger at the default 10 rps. It is
// a measurement harness, not an assertion: opt in with PADMARK_RENDER_MEASURE=1 (takes minutes).
func TestRenderPathologicalMeasure(t *testing.T) {
	if os.Getenv("PADMARK_RENDER_MEASURE") == "" {
		t.Skip("set PADMARK_RENDER_MEASURE=1 to print the render cost table")
	}

	rnd := render.NewRenderer()

	for _, testCase := range pathoCases() {
		t.Run(testCase.name, func(t *testing.T) {
			logScaling(t, testCase.name, func(size int) measurement {
				return measureRender(t, testCase.gen(size), rnd.Render)
			})
		})
	}

	t.Run("render_plain_escape", func(t *testing.T) {
		logScaling(t, "render_plain_escape", func(size int) measurement {
			return measureRender(t, fill("<a&\"b>", size), rnd.RenderPlain)
		})
	})
}

func logScaling(t *testing.T, name string, run func(size int) measurement) {
	t.Helper()

	const rps = 10 // server.DefaultRateLimit

	var (
		points []string
		prev   measurement
		last   measurement
		prevSz int
		lastSz int
	)

	for _, size := range measureSizes() {
		prev, prevSz = last, lastSz
		last, lastSz = run(size), size
		points = append(points, fmt.Sprintf("%dK=%s", size>>10, last.elapsed.Round(time.Millisecond)))

		// Keep growing roughly linear inputs to the real max; stop super-linear ones early.
		if last.elapsed > measureStopAfter && scalingExponent(prev, last, prevSz, lastSz) > 1.4 {
			break
		}
	}

	exponent := scalingExponent(prev, last, prevSz, lastSz)
	scale := math.Pow(float64(maxContentBytes)/float64(lastSz), exponent)
	fullTime := time.Duration(float64(last.elapsed) * scale)

	t.Logf("%-26s full=%-12s alloc=%.0fMB exp=%.2f cpu@10rps=%.1fs/s extrapolated=%t [%s]", name,
		fullTime.Round(time.Millisecond), last.allocMB*scale, exponent, fullTime.Seconds()*rps,
		lastSz != maxContentBytes, strings.Join(points, " "))
}

// scalingExponent is k in time ∝ size^k between two measurements (1 when there is no prior point).
func scalingExponent(prev, last measurement, prevSz, lastSz int) float64 {
	if prevSz == 0 || prev.elapsed <= 0 {
		return 1
	}

	return math.Log(last.elapsed.Seconds()/prev.elapsed.Seconds()) / math.Log(float64(lastSz)/float64(prevSz))
}
