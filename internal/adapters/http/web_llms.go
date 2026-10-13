package http

import (
	"log/slog"
	"net/http"

	_ "embed"
)

//go:embed spec/llms.txt
var llmsTxt []byte

// LLMsTxt handles GET /llms.txt — a short Markdown overview of the machine-readable API,
// written for agents (llms.txt convention). It points to /api/openapi.yaml for the full
// contract instead of duplicating it.
func LLMsTxt(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")

	_, err := w.Write(llmsTxt)
	if err != nil {
		// Headers are already sent — changing the response is impossible.
		// Only log the error; calling http.Error here would corrupt the body.
		slog.Error("write llms.txt", "err", err)
	}
}
