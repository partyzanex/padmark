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

	// acceptMIMEParts is the maximum number of parts when splitting a MIME type on ";".
	acceptMIMEParts = 2
)

type format uint8

const (
	formatJSON format = iota
	formatHTML
	formatPlain
)

// negotiate parses the Accept header and returns the preferred response format.
// A ?raw=1 query parameter always selects formatPlain, overriding the Accept header.
// Priority: ?raw=1 > text/html > text/plain|text/markdown > application/json (default).
func negotiate(r *http.Request) format {
	if r.URL.Query().Get("raw") == "1" {
		return formatPlain
	}

	accept := r.Header.Get("Accept")
	if accept == "" {
		return formatJSON
	}

	for part := range strings.SplitSeq(accept, ",") {
		mime := strings.TrimSpace(strings.SplitN(part, ";", acceptMIMEParts)[0])
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
