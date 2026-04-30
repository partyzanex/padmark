//go:generate go run go.uber.org/mock/mockgen@latest -source=handler.go -destination=handler_mocks_test.go -package=http_test

package http

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
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

//nolint:gochecknoglobals // computed once from embedded static content
var styleVersion = staticAssetVersion("static/style.css")

func staticAssetVersion(path string) string {
	data, err := staticFS.ReadFile(path)
	if err != nil {
		return "dev"
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])[:12]
}

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
	manager       NoteManager
	log           *slog.Logger
	pinger        Pinger
	noteTmpl      *template.Template
	indexTmpl     *template.Template
	loginTmpl     *template.Template
	apidocsTmpl   *template.Template
	successTmpl   *template.Template
	errorTmpl     *template.Template
	allowedTokens map[string]struct{}
}

// parseTmpl parses a page template together with the shared header partial.
func parseTmpl(name, src string) *template.Template {
	return template.Must(
		template.Must(template.New(name).Funcs(template.FuncMap{
			"styleVersion": func() string { return styleVersion },
		}).Parse(src)).Parse(headerTmplSrc),
	)
}

// NewHandler creates a Handler with required dependencies.
func NewHandler(manager NoteManager, log *slog.Logger) *Handler {
	return &Handler{
		manager:     manager,
		log:         log,
		noteTmpl:    parseTmpl("note", noteTmplSrc),
		indexTmpl:   parseTmpl("index", indexTmplSrc),
		loginTmpl:   parseTmpl("login", loginTmplSrc),
		apidocsTmpl: parseTmpl("apidocs", apidocsTmplSrc),
		successTmpl: parseTmpl("success", successTmplSrc),
		errorTmpl:   parseTmpl("error", errorTmplSrc),
	}
}

// WithPinger attaches a readiness probe to the handler.
func (h *Handler) WithPinger(p Pinger) *Handler {
	h.pinger = p
	return h
}

// WithAuth attaches server auth tokens to the handler.
// When tokens are configured, private notes require a valid token to view.
func (h *Handler) WithAuth(tokens []string) *Handler {
	if len(tokens) > 0 {
		h.allowedTokens = makeTokenSet(tokens)
	}

	return h
}

// isAuthenticated reports whether the request carries a valid server auth token.
// When no auth tokens are configured (h.allowedTokens is nil) all requests are
// considered authenticated, so private notes are never locked.
func (h *Handler) isAuthenticated(r *http.Request) bool {
	if h.allowedTokens == nil {
		return true
	}

	token := extractToken(r)
	_, ok := h.allowedTokens[token]

	return ok
}
