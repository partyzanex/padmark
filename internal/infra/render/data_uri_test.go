package render_test

import (
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/padmark/internal/infra/render"
)

// pixelPNG is a 1x1 PNG image in base64.
const pixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

type DataURISuite struct {
	suite.Suite

	r *render.Renderer
}

func (s *DataURISuite) SetupTest() {
	s.r = render.NewRenderer()
}

func TestDataURI(t *testing.T) {
	suite.Run(t, new(DataURISuite))
}

func (s *DataURISuite) render(markdown string) string {
	s.T().Helper()

	out, _, err := s.r.Render(markdown)
	s.Require().NoError(err)

	return out
}

// TestRasterImagesKeepSource covers the image types that a note page shows from a data: URI.
func (s *DataURISuite) TestRasterImagesKeepSource() {
	cases := map[string]string{
		"png":         "data:image/png;base64," + pixelPNG,
		"jpeg":        "data:image/jpeg;base64,/9j/4AAQSkZJRg==",
		"gif":         "data:image/gif;base64,R0lGODlhAQABAAAAACw=",
		"webp":        "data:image/webp;base64,UklGRhoAAABXRUJQ",
		"empty image": "data:image/png;base64,",
		"upper type":  "data:IMAGE/PNG;base64," + pixelPNG,
	}

	for name, uri := range cases {
		s.Run(name, func() {
			s.Equal(`<p><img src="`+uri+`" alt="i"/></p>`+"\n", s.render("![i]("+uri+")"))
		})
	}
}

// TestReferenceStyleImage is the form that documents with embedded figures use.
func (s *DataURISuite) TestReferenceStyleImage() {
	uri := "data:image/png;base64," + pixelPNG
	out := s.render("Text\n\n![Figure][fig]\n\n[fig]: " + uri + "\n")
	s.Contains(out, `<img src="`+uri+`" alt="Figure"/>`)
}

func (s *DataURISuite) TestUppercaseSchemeIsKept() {
	out := s.render(`<img src="DATA:image/png;base64,` + pixelPNG + `" alt="i">`)
	s.Contains(out, `src="data:image/png;base64,`+pixelPNG+`"`)
}

// TestImagesWithoutSource covers data: URIs that the page does not show, in Markdown and in raw
// HTML: the img element stays with its alt text, but without src.
func (s *DataURISuite) TestImagesWithoutSource() {
	cases := map[string]string{
		"svg":              "data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=",
		"svg as text":      "data:image/svg+xml,%3Csvg%3E%3C/svg%3E",
		"html":             "data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==",
		"plain text":       "data:text/plain;base64,aGVsbG8=",
		"png without b64":  "data:image/png,abc",
		"broken base64":    "data:image/png;base64,***",
		"unpadded base64":  "data:image/png;base64,YQ",
		"with fragment":    "data:image/png;base64," + pixelPNG + "#x",
		"with query":       "data:image/png;base64," + pixelPNG + "?x",
		"with empty query": "data:image/png;base64," + pixelPNG + "?",
		"parameter before": "data:image/png;charset=utf-8;base64," + pixelPNG,
		"control char":     "data:image/png;base64,YQ==\x01",
	}

	for name, uri := range cases {
		s.Run(name, func() {
			for _, markdown := range []string{"![i](" + uri + ")", `<img src="` + uri + `" alt="i">`} {
				out := s.render(markdown)
				s.Contains(out, `alt="i"`, markdown)
				s.NotContains(out, "src=", markdown)
			}
		})
	}
}

// TestLinksCannotUseDataURIs keeps the image rule out of links and quotation sources: there a
// data: URI would be a navigation target, not an image.
func (s *DataURISuite) TestLinksCannotUseDataURIs() {
	uri := "data:image/png;base64," + pixelPNG
	cases := map[string]string{
		"markdown link":    "[l](" + uri + ")",
		"html link":        `<a href="` + uri + `">l</a>`,
		"spaced link":      `<a href="  ` + uri + `">l</a>`,
		"uppercase scheme": `<a href="DATA:image/png;base64,` + pixelPNG + `">l</a>`,
		"area":             `<map name="m"><area shape="rect" coords="1,2,3,4" href="` + uri + `"></map>`,
		"blockquote cite":  `<blockquote cite="` + uri + `">q</blockquote>`,
		"q cite":           `<q cite="` + uri + `">q</q>`,
		"del cite":         `<del cite="` + uri + `">d</del>`,
		"ins cite":         `<ins cite="` + uri + `">i</ins>`,
	}

	for name, markdown := range cases {
		s.Run(name, func() {
			s.NotContains(s.render(markdown), "data:", markdown)
		})
	}
}

// TestLinksKeepTheirTargets pins the link pattern against the URL forms that notes use.
func (s *DataURISuite) TestLinksKeepTheirTargets() {
	cases := map[string]string{
		"https":          "https://example.com/a?b=c#d",
		"http":           "http://example.com",
		"mailto":         "mailto:a@example.com",
		"absolute path":  "/notes/abc",
		"relative path":  "notes/abc",
		"dot path":       "./a:b",
		"fragment":       "#user-content-x",
		"query":          "?page=2",
		"colon in path":  "a/b:c",
		"colon in query": "a?b=c:d",
	}

	for name, href := range cases {
		s.Run(name, func() {
			s.Contains(s.render(`<a href="`+href+`">l</a>`), `href="`+href+`"`, href)
		})
	}
}

func (s *DataURISuite) TestUppercaseLinkSchemeIsKept() {
	s.Contains(s.render(`<a href="HTTPS://example.com">l</a>`), `href="https://example.com"`)
}

func (s *DataURISuite) TestLinksWithOtherSchemesAreRemoved() {
	for _, href := range []string{"javascript:alert(1)", "vbscript:x", "file:///etc/passwd", "ftp://x"} {
		s.NotContains(s.render(`<a href="`+href+`">l</a>`), "href=", href)
	}
}

func (s *DataURISuite) dropped(markdown string) []string {
	s.T().Helper()

	_, reasons, err := s.r.Render(markdown)
	s.Require().NoError(err)

	return reasons
}

// TestDroppedImageReasons pins the reason that Render reports for each image without a source.
func (s *DataURISuite) TestDroppedImageReasons() {
	cases := map[string]struct {
		markdown string
		want     []string
	}{
		"svg":           {"![i](data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=)", []string{render.DataImageSVG}},
		"svg as text":   {`<img src="data:image/svg+xml,%3Csvg%3E" alt="i">`, []string{render.DataImageSVG}},
		"html":          {"![i](data:text/html;base64,PGI+eDwvYj4=)", []string{render.DataImageType}},
		"no type":       {"![i](data:;base64,YQ==)", []string{render.DataImageType}},
		"not base64":    {"![i](data:image/png,abc)", []string{render.DataImageEncoding}},
		"parameter":     {"![i](data:image/png;charset=utf-8;base64," + pixelPNG + ")", []string{render.DataImageEncoding}},
		"broken base64": {"![i](data:image/png;base64,***)", []string{render.DataImageBase64}},
		"unpadded":      {"![i](data:image/png;base64,YQ)", []string{render.DataImageBase64}},
		"fragment":      {"![i](data:image/png;base64," + pixelPNG + "#x)", []string{render.DataImageAddress}},
		"query":         {"![i](data:image/png;base64," + pixelPNG + "?x)", []string{render.DataImageAddress}},
		"empty query":   {"![i](data:image/png;base64," + pixelPNG + "?)", []string{render.DataImageAddress}},
		"invalid":       {"<img src=\"data:image/png;base64,YQ==\x01\" alt=\"i\">", []string{render.DataImageInvalid}},
		"spaced source": {`<img src="  data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=" alt="i">`, []string{render.DataImageSVG}},
		"uppercase":     {`<img src="DATA:image/svg+xml;base64,PHN2Zz48L3N2Zz4=" alt="i">`, []string{render.DataImageSVG}},
		"by reference": {
			"![a][svg] ![b][png]\n\n[svg]: data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=\n" +
				"[png]: data:image/png;base64," + pixelPNG + "\n",
			[]string{render.DataImageSVG},
		},
		"in page order": {
			"![a](data:image/png;base64,***) ![b](data:text/plain;base64,YQ==)",
			[]string{render.DataImageBase64, render.DataImageType},
		},
	}

	for name, tc := range cases {
		s.Run(name, func() {
			s.Equal(tc.want, s.dropped(tc.markdown), tc.markdown)
		})
	}
}

// TestNothingDropped covers notes whose images the page shows, and text that only mentions data:.
func (s *DataURISuite) TestNothingDropped() {
	cases := map[string]string{
		"raster image":     "![i](data:image/png;base64," + pixelPNG + ")",
		"uppercase type":   "![i](data:IMAGE/PNG;base64," + pixelPNG + ")",
		"web image":        "![i](https://example.com/a.svg)",
		"text":             "The data: field and data:image/svg+xml in prose.",
		"code":             "`![i](data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=)`",
		"link, not image":  "[l](data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=)",
		"other attribute":  `<img src="/a.png" alt="data:image/svg+xml">`,
		"no note contents": "",
	}

	for name, markdown := range cases {
		s.Run(name, func() {
			s.Empty(s.dropped(markdown), markdown)
		})
	}
}
