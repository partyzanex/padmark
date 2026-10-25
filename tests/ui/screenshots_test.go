package ui_test

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/mxschmitt/playwright-go"
	"github.com/orisano/pixelmatch"
	"github.com/stretchr/testify/require"
)

func (sess *uiSession) screenshot(name string, masks ...string) {
	sess.t.Helper()

	// References are captured by headless Chromium (chromium-headless-shell). A headed run uses the
	// full browser, which renders fonts and scrollbars differently, so it only watches the flow.
	if *withBrowser {
		sess.t.Logf("headed run: screenshot %s is not compared", name)

		return
	}

	sess.eval("() => document.fonts.ready")
	sess.eval(`() => {
		for (const tag of document.querySelectorAll('#createdTag')) {
			const icon = tag.querySelector('svg');
			tag.replaceChildren(...(icon ? [icon] : []), document.createTextNode(' Oct 24, 2026, 12:00 PM'));
		}
	}`)

	options := playwright.PageScreenshotOptions{
		FullPage: new(true), Animations: playwright.ScreenshotAnimationsDisabled,
		Scale: playwright.ScreenshotScaleCss, Caret: playwright.ScreenshotCaretHide,
	}
	for _, selector := range masks {
		options.Mask = append(options.Mask, sess.page.Locator(selector))
	}

	actual := sess.stableScreenshot(name, options)
	reference := filepath.Join("testdata", "screenshots", sess.screen.name, runtime.GOOS, name)

	if *updateImages {
		//nolint:gosec // G301: committed reference images must stay readable by every developer and CI
		sess.must(os.MkdirAll(filepath.Dir(reference), 0o755))
		//nolint:gosec // G306: committed reference images must stay readable by every developer and CI
		sess.must(os.WriteFile(reference, actual, 0o644))

		return
	}

	sess.compareScreenshot(name, reference, actual)
}

// stableScreenshot compares two consecutive captures so fonts, transitions and layout have settled.
func (sess *uiSession) stableScreenshot(name string, options playwright.PageScreenshotOptions) []byte {
	sess.t.Helper()

	actual, err := sess.page.Screenshot(options)
	sess.must(err)

	deadline := time.Now().Add(5 * time.Second)

	for {
		next, captureErr := sess.page.Screenshot(options)
		sess.must(captureErr)

		if bytes.Equal(actual, next) || sess.sameScreenshot(actual, next) {
			return next
		}

		if !time.Now().Before(deadline) {
			sess.must(os.WriteFile(filepath.Join(sess.artifacts, name+".unstable-before.png"), actual, 0o600))
			sess.must(os.WriteFile(filepath.Join(sess.artifacts, name+".unstable-after.png"), next, 0o600))
		}

		require.True(sess.t, time.Now().Before(deadline), "screenshot %s did not stabilize", name)

		actual = next
	}
}

// sameScreenshot ignores only the antialiasing noise already excluded by snapshot comparisons.
// Full-page mobile captures can rasterize a rounded border a little differently between frames.
func (sess *uiSession) sameScreenshot(previous, next []byte) bool {
	sess.t.Helper()

	previousImage, err := png.Decode(bytes.NewReader(previous))
	sess.must(err)
	nextImage, err := png.Decode(bytes.NewReader(next))
	sess.must(err)

	if previousImage.Bounds() != nextImage.Bounds() {
		return false
	}

	count, err := pixelmatch.MatchPixel(previousImage, nextImage, pixelmatch.Threshold(0.2))
	sess.must(err)

	return count == 0
}

// compareScreenshot fails the test when the capture differs from the reference image.
func (sess *uiSession) compareScreenshot(name, reference string, actual []byte) {
	sess.t.Helper()

	//nolint:gosec // G304: the path is built from fixed test data, not from user input
	expected, err := os.ReadFile(reference)
	if err != nil {
		sess.must(os.WriteFile(filepath.Join(sess.artifacts, name+".actual.png"), actual, 0o600))
		sess.t.Fatalf("read screenshot reference %s: %v; review the actual image, then run make test-ui-update",
			reference, err)
	}

	expectedImage, err := png.Decode(bytes.NewReader(expected))
	sess.must(err)
	actualImage, err := png.Decode(bytes.NewReader(actual))
	sess.must(err)

	var diffImage image.Image

	count, diffErr := pixelmatch.MatchPixel(expectedImage, actualImage,
		pixelmatch.Threshold(0.2), pixelmatch.WriteTo(&diffImage))
	if diffErr == nil && count <= 100 {
		return
	}

	sess.must(os.WriteFile(filepath.Join(sess.artifacts, name+".actual.png"), actual, 0o600))
	//nolint:gosec // G703: the artifact path is built from fixed test names, not from user input
	sess.must(os.WriteFile(filepath.Join(sess.artifacts, name+".expected.png"), expected, 0o600))

	if diffImage != nil {
		var buffer bytes.Buffer
		sess.must(png.Encode(&buffer, diffImage))
		sess.must(os.WriteFile(filepath.Join(sess.artifacts, name+".diff.png"), buffer.Bytes(), 0o600))
	}

	require.NoError(sess.t, diffErr, "screenshot dimensions must match: %s", reference)
	require.LessOrEqual(sess.t, count, 100, "screenshot changed: %s; see %s", reference, sess.artifacts)
}
