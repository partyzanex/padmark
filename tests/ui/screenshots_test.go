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

func (e *ui) screenshot(name string, masks ...string) {
	e.t.Helper()
	e.eval("() => document.fonts.ready")
	e.eval(`() => {
		for (const tag of document.querySelectorAll('#createdTag')) {
			const icon = tag.querySelector('svg');
			tag.replaceChildren(...(icon ? [icon] : []), document.createTextNode(' Oct 24, 2026, 12:00 PM'));
		}
	}`)
	options := playwright.PageScreenshotOptions{
		FullPage: playwright.Bool(true), Animations: playwright.ScreenshotAnimationsDisabled,
		Scale: playwright.ScreenshotScaleCss, Caret: playwright.ScreenshotCaretHide,
	}
	for _, selector := range masks {
		options.Mask = append(options.Mask, e.page.Locator(selector))
	}
	// Compare two consecutive captures so fonts, transitions and layout have settled.
	actual, err := e.page.Screenshot(options)
	e.must(err)
	deadline := time.Now().Add(5 * time.Second)
	for {
		next, captureErr := e.page.Screenshot(options)
		e.must(captureErr)
		if bytes.Equal(actual, next) {
			actual = next
			break
		}
		require.True(e.t, time.Now().Before(deadline), "screenshot %s did not stabilize", name)
		actual = next
	}
	reference := filepath.Join("testdata", "screenshots", e.screen.name, runtime.GOOS, name)
	if *updateImages {
		e.must(os.MkdirAll(filepath.Dir(reference), 0o755))
		e.must(os.WriteFile(reference, actual, 0o644))
		return
	}
	expected, err := os.ReadFile(reference)
	if err != nil {
		e.must(os.WriteFile(filepath.Join(e.artifacts, name+".actual.png"), actual, 0o600))
		e.t.Fatalf("read screenshot reference %s: %v; review the actual image, then run make test-ui-update", reference, err)
	}
	expectedImage, err := png.Decode(bytes.NewReader(expected))
	e.must(err)
	actualImage, err := png.Decode(bytes.NewReader(actual))
	e.must(err)
	var diffImage image.Image
	count, diffErr := pixelmatch.MatchPixel(expectedImage, actualImage,
		pixelmatch.Threshold(0.2), pixelmatch.WriteTo(&diffImage))
	if diffErr == nil && count <= 100 {
		return
	}
	e.must(os.WriteFile(filepath.Join(e.artifacts, name+".actual.png"), actual, 0o600))
	e.must(os.WriteFile(filepath.Join(e.artifacts, name+".expected.png"), expected, 0o600))
	if diffImage != nil {
		var buffer bytes.Buffer
		e.must(png.Encode(&buffer, diffImage))
		e.must(os.WriteFile(filepath.Join(e.artifacts, name+".diff.png"), buffer.Bytes(), 0o600))
	}
	require.NoError(e.t, diffErr, "screenshot dimensions must match: %s", reference)
	require.LessOrEqual(e.t, count, 100, "screenshot changed: %s; see %s", reference, e.artifacts)
}
