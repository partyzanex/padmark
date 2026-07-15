package render

import "html"

// RenderPlain escapes content and wraps it in <pre>, producing safe HTML for
// domain.ContentTypePlain notes without running it through markdown parsing/sanitization.
func (r *Renderer) RenderPlain(content string) (string, error) {
	return "<pre>" + html.EscapeString(content) + "</pre>", nil
}
