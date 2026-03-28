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

// StaticHandler serves embedded static assets under /static/.
//
//nolint:gochecknoglobals // package-level FS handler is intentional
var StaticHandler = http.FileServer(http.FS(staticFS))

// NoteManager is the interface the HTTP adapter requires from the business logic layer.
type NoteManager interface {
	Create(ctx context.Context, note *domain.Note) (*domain.Note, error)
	View(ctx context.Context, id string) (*domain.Note, error)
	GetRendered(ctx context.Context, id string) (*domain.Note, string, error)
	Update(ctx context.Context, id string, note *domain.Note) (*domain.Note, error)
	Delete(ctx context.Context, id string) error
}

// Pinger checks database connectivity.
type Pinger interface {
	PingContext(ctx context.Context) error
}

// Handler holds dependencies for all HTTP handlers.
type Handler struct {
	manager   NoteManager
	log       *slog.Logger
	pinger    Pinger
	noteTmpl  *template.Template
	indexTmpl *template.Template
}

// NewHandler creates a Handler with required dependencies.
func NewHandler(manager NoteManager, log *slog.Logger) *Handler {
	return &Handler{
		manager:   manager,
		log:       log,
		noteTmpl:  template.Must(template.New("note").Parse(noteTmplSrc)),
		indexTmpl: template.Must(template.New("index").Parse(indexTmplSrc)),
	}
}

// WithPinger attaches a readiness probe to the handler.
func (h *Handler) WithPinger(p Pinger) *Handler {
	h.pinger = p
	return h
}
