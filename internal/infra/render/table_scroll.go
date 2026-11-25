package render

import (
	"strings"

	"golang.org/x/net/html"
)

// tableScrollClass is the class of the box that scrolls a wide table. style.css and
// static/editor-preview.js use the same name. The sanitizer removes this class from user HTML
// (reContentClass), so only wrapTables can make such a box.
const tableScrollClass = "table-scroll"

// The tags of the scroll box. They are constants: wrapTables adds no user data to them.
const (
	tableScrollOpen  = `<div class="` + tableScrollClass + `">`
	tableScrollClose = `</div>`
	tableTag         = "table"
	tableEndTag      = "</table>"
)

// wrapTables puts each outermost table of sanitized HTML in a scroll box, so a wide table scrolls
// in its box and does not make the page wider. A table in a table cell stays in the box of the
// outer table. The tables come from Markdown and from raw HTML in the note.
//
// The step runs after the sanitizer. It copies each token of the sanitized HTML without a change
// and only adds the constant box tags, so it cannot let unsafe HTML through. A table that the note
// does not close gets its end tags at the end of the HTML, so the box contains the full table. The
// tokenizer reads each byte once: the cost is linear in the size of the HTML.
func wrapTables(sanitized string) string {
	if !hasTableTag(sanitized) {
		return sanitized
	}

	boxes := tableBoxes{out: make([]byte, 0, len(sanitized)+len(tableScrollOpen)+len(tableScrollClose)), depth: 0}
	tokenizer := html.NewTokenizer(strings.NewReader(sanitized))

	// raw keeps a copy of the token: TagName changes the case of the tokenizer buffer in place.
	var raw []byte

	for {
		tokenType := tokenizer.Next()
		raw = append(raw[:0], tokenizer.Raw()...)

		if tokenType == html.ErrorToken {
			// A strings.Reader gives only io.EOF; the remaining bytes, if any, stay as they are.
			boxes.out = append(boxes.out, raw...)

			break
		}

		boxes.add(tokenizer, tokenType, raw)
	}

	if boxes.depth > 0 {
		boxes.out = append(boxes.out, strings.Repeat(tableEndTag, boxes.depth)...)
		boxes.out = append(boxes.out, tableScrollClose...)
	}

	return string(boxes.out)
}

// tableBoxes collects the HTML with the scroll boxes. depth is the number of open tables.
type tableBoxes struct {
	out   []byte
	depth int
}

// add appends one token with the box tags that it needs.
func (b *tableBoxes) add(tokenizer *html.Tokenizer, tokenType html.TokenType, raw []byte) {
	if tokenType != html.StartTagToken && tokenType != html.SelfClosingTagToken && tokenType != html.EndTagToken {
		b.out = append(b.out, raw...)

		return
	}

	name, _ := tokenizer.TagName()
	if string(name) != tableTag {
		b.out = append(b.out, raw...)

		return
	}

	// The HTML parser of the browser ignores the "/" of <table/>: it opens a table.
	if tokenType != html.EndTagToken {
		if b.depth == 0 {
			b.out = append(b.out, tableScrollOpen...)
		}

		b.out = append(b.out, raw...)
		b.depth++

		return
	}

	b.out = append(b.out, raw...)

	switch b.depth {
	case 0:
		// An end tag without a table: the browser ignores it, and it gets no box.
	case 1:
		b.out = append(b.out, tableScrollClose...)
		b.depth = 0
	default:
		b.depth--
	}
}

// hasTableTag reports whether the HTML has "<table" in any letter case. The sanitizer escapes "<"
// in text and attributes, so a note without a table skips the tokenizer.
func hasTableTag(sanitized string) bool {
	for rest := sanitized; ; {
		idx := strings.IndexByte(rest, '<')
		if idx < 0 {
			return false
		}

		rest = rest[idx+1:]
		if len(rest) >= len(tableTag) && strings.EqualFold(rest[:len(tableTag)], tableTag) {
			return true
		}
	}
}
