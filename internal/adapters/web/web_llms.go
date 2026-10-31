package web

import (
	_ "embed"
	"log/slog"
	"net/http"
	"strings"

	"github.com/partyzanex/padmark/internal/buildinfo"
)

//go:embed spec/llms.txt
var llmsTxt []byte

//go:embed spec/api-reference.md
var apiReference []byte

// LLMsTxt returns the handler for GET /llms.txt, the navigation index for agent-facing
// documentation.
func LLMsTxt(log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { writeAgentDocument(w, r, log, llmsTxt) }
}

// APIReference returns the handler for GET /api/reference.md, the detailed agent-facing API guide.
func APIReference(log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { writeAgentDocument(w, r, log, apiReference) }
}

func writeAgentDocument(w http.ResponseWriter, r *http.Request, log *slog.Logger, source []byte) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")

	_, err := w.Write([]byte(strings.ReplaceAll(string(source), "{{VERSION}}", buildinfo.Version())))
	if err != nil {
		// Headers are already sent — changing the response is impossible.
		// Only log the error; calling http.Error here would corrupt the body.
		log.ErrorContext(r.Context(), "write agent documentation", "err", err)
	}
}
