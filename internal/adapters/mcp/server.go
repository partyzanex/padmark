package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/partyzanex/padmark/internal/errorcontext"
)

// version is reported in the MCP initialize result. A non-empty version is required by the
// SDK; when APP_VERSION is absent (go run / test builds), "dev" is used.
func version() string {
	if build := os.Getenv("APP_VERSION"); build != "" {
		return build
	}

	return "dev"
}

// Manager holds the shared state of the stdio MCP server: the padmark client every tool call
// goes through and the session's edit-code vault. The tool surface and its transport are owned
// by the MCP SDK; this type is only the composition and shared state.
type Manager struct {
	client PadmarkClient
	files  FileClient
	vault  *editCodeVault
	log    *slog.Logger
	srv    *mcpsdk.Server
	// uploadRoot is the only directory that upload_file reads from. Empty turns uploads off.
	uploadRoot string
	// uploadHidden permits upload paths with names that start with a dot.
	uploadHidden hiddenPaths
	// uploadSlug permits the slug field of upload_file.
	uploadSlug bool
	// hasToken is true when the proxy sends a bearer token. upload_file then uses "owner" as
	// its default privacy.
	hasToken bool
}

// Option sets an optional Manager setting.
type Option func(*Manager)

// WithUploadRoot lets upload_file read regular files under root, also through symbolic links
// that stay under root. Without this option, upload_file refuses every path.
func WithUploadRoot(root string) Option {
	return func(m *Manager) {
		m.uploadRoot = root
	}
}

// WithUploadHidden lets upload_file read paths with names that start with a dot, such as .env.
// Without this option, upload_file refuses them, because such names often hold secrets.
func WithUploadHidden() Option {
	return func(m *Manager) {
		m.uploadHidden = hiddenPathsAllowed
	}
}

// WithUploadSlug lets upload_file send the slug that the agent gives. Without this option,
// upload_file refuses a slug, so injected text cannot select the address of a file.
func WithUploadSlug() Option {
	return func(m *Manager) {
		m.uploadSlug = true
	}
}

// WithAccountToken tells the manager that the clients send a bearer token. upload_file then
// makes "owner" its default privacy: only the account of the token can read a file that the
// agent uploads without a privacy field.
func WithAccountToken() Option {
	return func(m *Manager) {
		m.hasToken = true
	}
}

// NewManager builds an MCP server exposing padmark's REST surface as tools, targeting the
// padmark instance the client is pointed at. The generated client is usually both client and
// files. No connection is opened here (no eager ping):
// server/network failures surface per tool call as structured results with remediation hints
// (fail-soft) — a stdio process that dies on a missing server just breaks the agent's whole
// tool set, and padmark may legitimately be started later.
func NewManager(client PadmarkClient, files FileClient, log *slog.Logger, opts ...Option) *Manager {
	m := &Manager{
		client:       client,
		files:        files,
		vault:        newEditCodeVault(),
		log:          log,
		srv:          nil,
		uploadRoot:   DefaultUploadRoot,
		uploadHidden: hiddenPaths(DefaultUploadAllowHidden),
		uploadSlug:   DefaultUploadAllowSlug,
		hasToken:     false,
	}

	for _, opt := range opts {
		opt(m)
	}

	srv := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:        "padmark-mcp",
		Version:     version(),
		Title:       "",
		Description: "",
		WebsiteURL:  "",
		Icons:       nil,
	}, nil)
	m.registerTools(srv)
	m.srv = srv

	return m
}

// Run serves MCP over stdio until stdin closes, the context is cancelled, or the SDK reports a
// transport error. A client disconnecting (stdin EOF) is a session ending normally, not an error.
func (m *Manager) Run(ctx context.Context) error {
	return errorcontext.Wrap(m.Serve(ctx, &mcpsdk.StdioTransport{MaxLineLength: 0}), "serve")
}

// Serve serves MCP over an explicit transport — the seam for alternative transports (stdio is
// Run). Error semantics match Run.
func (m *Manager) Serve(ctx context.Context, transport mcpsdk.Transport) error {
	err := m.srv.Run(ctx, transport)
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, mcpsdk.ErrConnectionClosed) {
		// The normal way a session ends. A nil must short-circuit here: fmt.Errorf with %w on
		// nil still yields a non-nil error, which turned every clean shutdown into exit 1.
		return nil
	}

	return fmt.Errorf("mcp server run: %w", err)
}

// ConnectSession connects one raw MCP server session on the given transport — the seam tests
// use in place of Serve, exercising the same registered tool set without owning a process.
func (m *Manager) ConnectSession(ctx context.Context, transport mcpsdk.Transport) (*mcpsdk.ServerSession, error) {
	srvSession, err := m.srv.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect mcp session: %w", err)
	}

	return srvSession, nil
}

// registerTools wires every tool of the surface onto the SDK server.
func (m *Manager) registerTools(srv *mcpsdk.Server) {
	registerNoteTools(srv, m)
	registerLinkTools(srv, m)
	registerFileTools(srv, m)

	mcpsdk.AddTool[checkServerInput, checkServerOutput](srv, checkServerTool(), m.checkServer)
}
