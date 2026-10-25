package http

import (
	"log/slog"
	"net/http"
	"strings"

	_ "embed"

	"github.com/partyzanex/padmark/internal/buildinfo"
)

//go:embed spec/llms.txt
var llmsTxt []byte

//go:embed spec/api-reference.md
var apiReference []byte

// LLMsTxt handles GET /llms.txt, the navigation index for agent-facing documentation.
func LLMsTxt(w http.ResponseWriter, _ *http.Request) {
	writeAgentDocument(w, llmsTxt)
}

// APIReference handles GET /api/reference.md, the detailed agent-facing API guide.
func APIReference(w http.ResponseWriter, _ *http.Request) {
	writeAgentDocument(w, apiReference)
}

func writeAgentDocument(w http.ResponseWriter, source []byte) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")

	_, err := w.Write([]byte(strings.ReplaceAll(string(source), "{{VERSION}}", buildinfo.Version())))
	if err != nil {
		// Headers are already sent — changing the response is impossible.
		// Only log the error; calling http.Error here would corrupt the body.
		slog.Error("write agent documentation", "err", err)
	}
}
