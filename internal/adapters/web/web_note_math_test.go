package web

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/partyzanex/padmark/internal/domain"
)

func mathTestNote() *domain.Note {
	return &domain.Note{
		ID: "formulas", Title: "Formulas", Content: "$x$", ContentType: new(domain.ContentTypeMarkdown),
		CreatedAt: time.Now(), UpdatedAt: time.Now(),

		ExpiresAt: nil, Privacy: nil, OwnerID: nil, EditCode: "",
		Views: 0, BurnTTL: 0, BurnAfterReading: false, ClearExpiry: false, ShowContents: nil,
	}
}

// TestToNoteViewData_HasMath checks when the note page loads KaTeX: only for a formula element
// that the renderer wrote, not for text that only looks like one.
func TestToNoteViewData_HasMath(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		rendered string
		want     bool
	}{
		"inline formula":  {rendered: `<p><span class="math-inline">x</span></p>`, want: true},
		"display formula": {rendered: `<div class="math-display">x</div>`, want: true},
		"no formula":      {rendered: "<p>$5 and $10</p>", want: false},
		"empty note":      {rendered: "", want: false},
		// A plain-text note escapes its content, so its quotes cannot form the marker.
		"plain text":         {rendered: `<pre>&lt;span class=&#34;math-inline&#34;&gt;</pre>`, want: false},
		"class word in text": {rendered: "<p>class=math-inline</p>", want: false},
		"code block":         {rendered: "<pre><code>class=&#34;math-</code></pre>", want: false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, toNoteViewData(mathTestNote(), tc.rendered).HasMath)
		})
	}
}

func TestToBurnInterstitialViewData_NoMath(t *testing.T) {
	t.Parallel()

	data := toBurnInterstitialViewData(mathTestNote(), "token")
	assert.False(t, data.HasMath, "the interstitial does not show the note, so it loads no KaTeX")
}
