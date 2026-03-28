package http

import (
	"net/http"
	"strings"
)

const (
	mimeJSON     = "application/json"
	mimeHTML     = "text/html"
	mimePlain    = "text/plain"
	mimeMarkdown = "text/markdown"
)

type format uint8

const (
	formatJSON format = iota
	formatHTML
	formatPlain
)

// negotiate parses the Accept header and returns the preferred response format.
// Priority: text/html > text/plain|text/markdown > application/json (default).
func negotiate(r *http.Request) format {
	accept := r.Header.Get("Accept")
	if accept == "" {
		return formatJSON
	}

	for _, part := range strings.Split(accept, ",") {
		mime := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		switch mime {
		case mimeHTML:
			return formatHTML
		case mimePlain, mimeMarkdown:
			return formatPlain
		case mimeJSON, "*/*":
			return formatJSON
		}
	}

	return formatJSON
}
