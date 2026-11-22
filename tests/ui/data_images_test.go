package ui_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// pixelPNG is a 1x1 PNG image in base64.
const pixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

func TestDataImages(t *testing.T) {
	runScenarios(t, []scenario{
		{name: "PreviewAndNotePage", run: testDataImages, accounts: false, files: false, allowedStatuses: nil},
	})
}

// dataImagesNote embeds images as data: URIs, inline and by reference, as documents with figures
// do. Only the raster images have a source that the page shows, also with an uppercase type
// and with an empty fragment; SVG and HTML lose their source,
// and the preview tells the author why.
func dataImagesNote() string {
	return strings.Join([]string{
		"![png][fig]",
		"![upper](data:IMAGE/PNG;base64," + pixelPNG + ")",
		"![hash](data:image/png;base64," + pixelPNG + "#)",
		"![svg](data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciLz4=)",
		"![html](data:text/html;base64,PGI+eDwvYj4=)",
		"[link](data:image/png;base64," + pixelPNG + ")",
		"[fig]: data:image/png;base64," + pixelPNG,
	}, "\n\n")
}

// imagesScript waits until each image under the root is decoded and reports alt:width, or
// alt:none for an image without a source.
const imagesScript = `async (root) => Promise.all(Array.from(document.querySelectorAll(root + ' img'),
	async (img) => {
		if (!img.hasAttribute('src')) return img.alt + ':none';
		try { await img.decode(); return img.alt + ':' + img.naturalWidth; } catch (e) { return img.alt + ':error'; }
	}))`

// expectDataImages checks the images under the root: alt:width for a shown image, alt:none for
// an image without a source. No link can use a data: URI.
func (sess *uiSession) expectDataImages(root string, want ...any) {
	sess.t.Helper()
	sess.must(sess.expect.Locator(sess.page.Locator(root + " img")).ToHaveCount(len(want)))

	value, err := sess.page.Evaluate(imagesScript, root)
	sess.must(err)
	require.Equal(sess.t, want, value, root)

	sess.must(sess.expect.Locator(sess.page.Locator(root + ` a[href^="data:" i]`)).ToHaveCount(0))
}

// testDataImages checks that the preview shows the images that the saved note shows, and a
// reason in place of each image that the saved note does not show.
func testDataImages(sess *uiSession) {
	sess.gotoPage("/")
	sess.fill("#titleInput", "Figures")
	sess.writeText(dataImagesNote())
	sess.showPreview()
	sess.expectDataImages("#preview", "png:1", "upper:1", "hash:1")

	notices := sess.page.Locator("#preview .preview-image-notice")
	sess.must(sess.expect.Locator(notices).ToHaveCount(2))
	problems, err := notices.EvaluateAll(`(nodes) => nodes.map((node) => node.dataset.problem)`)
	sess.must(err)
	require.Equal(sess.t, []any{"svg", "type"}, problems)

	sess.button("Publish note")
	sess.must(sess.expect.Page(sess.page).ToHaveURL(regexpURL("/success?")))
	sess.link("Open note")
	sess.expectDataImages(".prose.markdown-body", "png:1", "upper:1", "hash:1", "svg:none", "html:none")
	sess.must(sess.expect.Locator(sess.page.Locator(".preview-image-notice")).ToHaveCount(0))
}
