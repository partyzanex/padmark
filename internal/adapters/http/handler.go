//go:generate go run go.uber.org/mock/mockgen@latest -source=handler.go -destination=handler_mocks_test.go -package=http_test

package http

import (
	"context"
	"embed"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/partyzanex/padmark/internal/domain"
)

//go:embed static
var staticFS embed.FS

//go:embed templates/header.html
var headerTmplSrc string

// StaticHandler serves embedded static assets under /static/.
//
//nolint:gochecknoglobals // package-level FS handler is intentional
var StaticHandler = http.FileServer(http.FS(staticFS))

// NoteManager is the interface the HTTP adapter requires from the business logic layer.
type NoteManager interface {
	Create(ctx context.Context, note *domain.Note) (*domain.Note, error)
	Peek(ctx context.Context, id string) (*domain.Note, error)
	View(ctx context.Context, id string) (*domain.Note, error)
	GetRendered(ctx context.Context, id string) (*domain.Note, string, error)
	Update(ctx context.Context, id, editCode string, note *domain.Note) (*domain.Note, error)
	Delete(ctx context.Context, id, editCode string) error
}

// Pinger checks database connectivity.
type Pinger interface {
	PingContext(ctx context.Context) error
}

// Handler holds dependencies for all HTTP handlers.
type Handler struct {
	manager     NoteManager
	log         *slog.Logger
	pinger      Pinger
	noteTmpl    *template.Template
	indexTmpl   *template.Template
	successTmpl *template.Template
	errorTmpl   *template.Template
}

// parseTmpl parses a page template together with the shared header partial.
func parseTmpl(name, src string) *template.Template {
	return template.Must(
		template.Must(template.New(name).Parse(src)).Parse(headerTmplSrc),
	)
}

// NewHandler creates a Handler with required dependencies.
func NewHandler(manager NoteManager, log *slog.Logger) *Handler {
	return &Handler{
		manager:     manager,
		log:         log,
		noteTmpl:    parseTmpl("note", noteTmplSrc),
		indexTmpl:   parseTmpl("index", indexTmplSrc),
		successTmpl: parseTmpl("success", successTmplSrc),
		errorTmpl:   parseTmpl("error", errorTmplSrc),
	}
}

// WithPinger attaches a readiness probe to the handler.
func (h *Handler) WithPinger(p Pinger) *Handler {
	h.pinger = p
	return h
}
